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

var (
	IdleTimeout = 30 * time.Minute
)

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
			activeterm.Middleware(),
			StructuredMiddlewareWithLogger(logger),
		),
		func(s *ssh.Server) error {
			// Allow all local port forwarding requests initially; validation happens in handler
			s.LocalPortForwardingCallback = func(ctx ssh.Context, bindHost string, bindPort uint32) bool {
				return true
			}

			s.ChannelHandlers = map[string]ssh.ChannelHandler{
				"direct-tcpip": jumps.DirectTCPIPHandler,
				"session":      ssh.DefaultSessionHandler,
			}

			s.IdleTimeout = IdleTimeout
			s.AddHostKey(privateKey)
			publicKeyOption := ssh.PublicKeyAuth(jumps.PublicKeyHandler)
			s.SetOption(publicKeyOption)

			return nil
		},
	)

	jumps.Server = sshServer

	return jumps
}

func (srv *Server) PermsForUser(user string) *Permission {
	srv.mu.RLock()
	defer srv.mu.RUnlock()
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
	d := localForwardChannelData{}
	if err := gossh.Unmarshal(newChan.ExtraData(), &d); err != nil {
		newChan.Reject(gossh.ConnectionFailed, "error parsing forward data: "+err.Error())
		return
	}

	if d.DestPort == 1 {
		srv.handleDynamicForward(newChan, ctx, d)
		return
	}

	// Standard Static Forwarding
	srv.handleStaticForward(newChan, ctx, d)
}

func (srv *Server) handleDynamicForward(newChan gossh.NewChannel, ctx ssh.Context, d localForwardChannelData) {
	logger := srv.logger.With(slog.String("type", "dynamic"), slog.String("user", ctx.User()))

	resolver := GetTargetResolver(ctx)

	// We must accept the channel first, or the SSH client might timeout
	// while the user is picking a target in the TUI.
	ch, reqs, err := newChan.Accept()
	if err != nil {
		logger.Error("failed to accept channel", "error", err)
		return
	}
	go gossh.DiscardRequests(reqs)

	// Wait for the TUI to populate the target
	select {
	case <-resolver.Resolved:
		// Target is ready
	case <-ctx.Done():
		ch.Close()
		return
	case <-time.After(2 * time.Minute):
		logger.Warn("timeout waiting for TUI selection")
		ch.Close()
		return
	}

	// Read the target safely
	resolver.mu.RLock()
	targetAddr := resolver.target
	resolver.mu.RUnlock()

	if targetAddr == "" {
		logger.Error("resolved target is empty")
		ch.Close()
		return
	}

	userTunnels.WithLabelValues(ctx.User()).Inc()
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var dialer net.Dialer
	dconn, err := dialer.DialContext(dctx, "tcp", targetAddr)
	if err != nil {
		logger.Error("failed to dial target", "target", targetAddr, "error", err)
		ch.Close()
		return
	}

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

func (srv *Server) handleStaticForward(newChan gossh.NewChannel, ctx ssh.Context, d localForwardChannelData) {
	logger := srv.logger.With(
		slog.String("user", ctx.User()),
		slog.String("host", d.DestAddr),
		slog.Int("port", int(d.DestPort)),
	)

	ports, err := srv.KubernetesPortsForUser(ctx, ctx.User())
	if err != nil {
		newChan.Reject(gossh.ConnectionFailed, "error querying Kubernetes api")
		return
	}

	var addr string
	var ok bool
	var displayName string

	ds := strings.Split(d.DestAddr, ".")

	switch {
	case strings.HasPrefix(d.DestAddr, "svc.") && len(ds) == 3:
		displayName = fmt.Sprintf("Service %s/%s:%d", ds[1], ds[2], d.DestPort)
		addr, ok = ports.MatchingService(ds[2], ds[1], int32(d.DestPort))
	case strings.HasPrefix(d.DestAddr, "pod.") && len(ds) == 3:
		displayName = fmt.Sprintf("Pod %s/%s:%d", ds[1], ds[2], d.DestPort)
		addr, ok = ports.MatchingPod(ds[2], ds[1], int32(d.DestPort))
	case len(ds) == 2:
		displayName = fmt.Sprintf("Pod %s/%s:%d", ds[0], ds[1], d.DestPort)
		addr, ok = ports.MatchingPod(ds[1], ds[0], int32(d.DestPort))
	default:
		newChan.Reject(gossh.ConnectionFailed, "invalid kubernetes format destination")
		return
	}

	if !ok {
		newChan.Reject(gossh.ConnectionFailed, "destination not authorized")
		logger.Warn("destination not authorized")
		return
	}

	logger.Info("forwarding", "addr", addr)
	userTunnels.WithLabelValues(ctx.User()).Inc()

	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var dialer net.Dialer
	dconn, err := dialer.DialContext(dctx, "tcp", addr)
	if err != nil {
		newChan.Reject(gossh.ConnectionFailed, "upstream connection failed")
		return
	}

	ch, reqs, err := newChan.Accept()
	if err != nil {
		dconn.Close()
		return
	}
	go gossh.DiscardRequests(reqs)

	// Notify TUI if listening
	statusCh := GetStatusChannel(ctx)
	select {
	case statusCh <- displayName:
	default:
		// Channel full or no listener, ignore
	}

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

func (srv *Server) StartWatchConfig(ctx context.Context, path string) error {
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
				if filepath.Base(event.Name) != filepath.Base(path) {
					continue
				}
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Rename) {
					debouncer.Debounce(event, func(e fsnotify.Event) {
						srv.logger.Info("Reloading config, on file change")
						perms, err := readPermission(srv.logger, path)
						if err != nil {
							srv.logger.Error("can't reload config", "error", err)
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
	return watcher.Add(filepath.Dir(path))
}

func (srv *Server) StopWatchConfig() {
	if srv.configWatcher != nil {
		_ = srv.configWatcher.Close()
	}
}

func readKeys(logger *slog.Logger, cfg SSHJumpConfig) map[string]Permission {
	m := make(map[string]Permission)
	for _, perm := range cfg.Permissions {
		if perm.Username == "" {
			continue
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(perm.AuthorizedKey))
		if err != nil {
			logger.Warn("invalid key", "username", perm.Username)
			continue
		}
		perm.Key = key
		m[perm.Username] = perm
	}
	return m
}
