package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/akhenakh/sshjump/debounce"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/activeterm"
	"github.com/charmbracelet/wish/bubbletea"
	"github.com/fsnotify/fsnotify"
	gossh "golang.org/x/crypto/ssh"
	"google.golang.org/grpc/health"
	"k8s.io/client-go/kubernetes"
)

type Server struct {
	logger       *slog.Logger
	healthServer *health.Server

	*ssh.Server
	clientset     *kubernetes.Clientset
	configWatcher *fsnotify.Watcher

	mu          sync.RWMutex
	permissions map[string]Permission
}

// direct-tcpip data struct as specified in RFC4254, Section 7.2.
type localForwardChannelData struct {
	DestAddr string
	DestPort uint32

	OriginAddr string
	OriginPort uint32
}

// TODO: parametrize.
var (
	IdleTimeout    = 2 * time.Minute
	portContextKey contextKey
)

type contextKey int

func NewServer(
	logger *slog.Logger,
	healthServer *health.Server,
	privateKey gossh.Signer,
	keys map[string]Permission,
	clientset *kubernetes.Clientset,
) *Server {
	jumps := &Server{
		logger:       logger,
		healthServer: healthServer,
		permissions:  keys,
		clientset:    clientset,
	}

	sshServer, _ := wish.NewServer(
		wish.WithMiddleware(
			bubbletea.Middleware(jumps.teaHandler),
			activeterm.Middleware(), // Bubble Tea apps usually require a PTY.
			StructuredMiddlewareWithLogger(logger),
		),
		func(s *ssh.Server) error {
			s.LocalPortForwardingCallback = func(ctx ssh.Context, bindHost string, bindPort uint32) bool {
				// TODO: prevalidation ?

				slog.Debug("Accepted forward", "host", bindHost, "port", bindPort, "user", ctx.User())

				return true
			}

			s.ChannelHandlers = map[string]ssh.ChannelHandler{
				"direct-tcpip": jumps.DirectTCPIPHandler,
				"session":      ssh.DefaultSessionHandler,
			}

			// Host private key
			s.IdleTimeout = IdleTimeout
			s.AddHostKey(privateKey)

			// Users public keys
			publicKeyOption := ssh.PublicKeyAuth(jumps.PublicKeyHandler)
			s.SetOption(publicKeyOption)

			return nil
		},
	)

	jumps.Server = sshServer

	return jumps
}

// PermsForUser returns the permissions for a given user.
// It locks the server mutex to ensure safe access to the permissions map.
func (srv *Server) PermsForUser(user string) *Permission {
	srv.mu.RLock()
	defer srv.mu.RUnlock()

	// looking for a matching username
	perm, ok := srv.permissions[user]
	if !ok {
		return nil
	}

	return &perm
}

func (srv *Server) PublicKeyHandler(ctx ssh.Context, key ssh.PublicKey) bool {
	perms := srv.PermsForUser(ctx.User())
	if perms == nil {
		srv.logger.Warn("no such username", "username", ctx.User(), "ip", ctx.RemoteAddr().String())

		return false
	}

	// validate key
	if ssh.KeysEqual(key, perms.Key) {
		return true
	}

	srv.logger.Warn("not matching key", "username", ctx.User(), "ip", ctx.RemoteAddr().String())

	return false
}

// DirectTCPIPHandler handles TCP forward.
func (srv *Server) DirectTCPIPHandler(
	s *ssh.Server,
	conn *gossh.ServerConn,
	newChan gossh.NewChannel,
	ctx ssh.Context,
) {
	logger := srv.logger.With(
		slog.String("user", ctx.User()),
	)

	d := localForwardChannelData{}
	if err := gossh.Unmarshal(newChan.ExtraData(), &d); err != nil {
		newChan.Reject(gossh.ConnectionFailed, "error parsing forward data: "+err.Error())

		return
	}

	logger = srv.logger.With(
		slog.String("host", d.DestAddr),
		slog.Int("port", int(d.DestPort)),
	)

	logger.Debug("tcp fwd request")

	if s.LocalPortForwardingCallback == nil || !s.LocalPortForwardingCallback(ctx, d.DestAddr, d.DestPort) {
		newChan.Reject(gossh.Prohibited, "port forwarding is disabled")

		return
	}

	ports, err := srv.KubernetesPortsForUser(ctx, ctx.User())
	if err != nil {
		newChan.Reject(gossh.ConnectionFailed, "error querying Kubernetes api: "+err.Error())

		logger.Debug("error querying Kubernetes API",
			"error", err.Error(),
		)

		return
	}

	var addr string
	var ok bool

	ds := strings.Split(d.DestAddr, ".")

	switch {
	case strings.HasPrefix(d.DestAddr, "svc.") && len(ds) == 3:
		addr, ok = ports.MatchingService(ds[2], ds[1], int32(d.DestPort))
	case strings.HasPrefix(d.DestAddr, "pod.") && len(ds) == 3:
		addr, ok = ports.MatchingPod(ds[2], ds[1], int32(d.DestPort))
	case len(ds) == 2:
		addr, ok = ports.MatchingPod(ds[1], ds[0], int32(d.DestPort))
	default:
		newChan.Reject(gossh.ConnectionFailed, "invalid kubernetes format destination")

		return
	}

	if !ok {
		newChan.Reject(gossh.ConnectionFailed, "kubernetes destination not authorized")
		logger.Warn("destination not authorized")

		return
	}

	logger.Debug("forward in progress",
		"user", ctx.User(),
		"addr", addr,
	)

	userTunnels.WithLabelValues(ctx.User()).Inc()

	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var dialer net.Dialer
	dconn, err := dialer.DialContext(dctx, "tcp", addr)
	if err != nil {
		_ = newChan.Reject(gossh.ConnectionFailed, fmt.Sprintf("%s while connecting to %s", err.Error(), addr))

		srv.logger.Warn("connection error",
			"error", err.Error(),
			"addr", addr,
		)

		return
	}

	ch, reqs, err := newChan.Accept()
	if err != nil {
		dconn.Close()

		return
	}

	go gossh.DiscardRequests(reqs)

	go func() {
		defer ch.Close()
		defer dconn.Close()
		_, _ = io.Copy(ch, dconn)
	}()
	go func() {
		defer ch.Close()
		defer dconn.Close()
		_, _ = io.Copy(dconn, ch)
	}()
}

// StartWatchConfig starts a file watcher to monitor for config file changes, will reload on changes.
func (srv *Server) StartWatchConfig(ctx context.Context, path string) error {
	// watch for file changes and issue a reload on changes
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("can't create file watcher: %w", err)
	}
	srv.mu.Lock()
	srv.configWatcher = watcher
	srv.mu.Unlock()

	debouncer := debounce.NewDebouncer(200 * time.Millisecond)

	go func() {
		for {
			select {
			case <-ctx.Done():
				srv.configWatcher.Close()

				return
			case event, ok := <-srv.configWatcher.Events:
				if !ok {
					return
				}

				// we watch the parent directory then filter
				var found bool
				if path == filepath.Base(event.Name) {
					found = true
				}
				if !found {
					continue
				}

				if event.Has(fsnotify.Write) || event.Has(fsnotify.Rename) {
					debouncer.Debounce(event, func(e fsnotify.Event) {
						srv.logger.Info("Reloading config, on file change")
						perms, err := readPermission(srv.logger, path)
						if err != nil {
							srv.logger.Error("can't reload config after change, still running on old config",
								"error", err)

							return
						}
						srv.mu.Lock()
						srv.permissions = perms
						srv.mu.Unlock()
					})
				}

			case err, ok := <-srv.configWatcher.Errors:
				if !ok {
					return
				}
				srv.logger.Error("error watching config file", "error", err.Error())
			}
		}
	}()

	// watch parent directory for atomic updates
	err = watcher.Add(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("error adding config file to watcher: %w", err)
	}

	return nil
}

// StopWatchConfig stop watching for config file changes.
func (srv *Server) StopWatchConfig() {
	if srv.configWatcher != nil {
		_ = srv.configWatcher.Close()
	}
}

// readKeys reads all the keys from the config file
// it does not break if some keys are invalid because
// the same function is used to reload to file it changes
// and the system needs to keep running.
func readKeys(logger *slog.Logger, cfg SSHJumpConfig) map[string]Permission {
	m := make(map[string]Permission)
	for i, perm := range cfg.Permissions {
		if perm.Username == "" {
			logger.Warn("invalid config no username", "index", i)

			continue
		}

		// testing for username duplicates
		if _, exist := m[perm.Username]; exist {
			logger.Warn("duplicate username entry",
				"username", perm.Username,
				"index", i)

			continue
		}

		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(perm.AuthorizedKey))
		if err != nil {
			logger.Warn("invalid key",
				"error", err,
				"username", perm.Username,
				"key", perm.AuthorizedKey,
				"index", i)

			continue
		}

		perm.Key = key
		m[perm.Username] = perm
	}

	return m
}
