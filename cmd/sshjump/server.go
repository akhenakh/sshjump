package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/activeterm"
	"github.com/charmbracelet/wish/bubbletea"
	"github.com/charmbracelet/wish/logging"
	gossh "golang.org/x/crypto/ssh"
	"k8s.io/client-go/kubernetes"
)

var (
	// TODO: parametrize.
	DeadlineTimeout = 30 * time.Second
	IdleTimeout     = 10 * time.Second
)

type Server struct {
	logger *slog.Logger
	*ssh.Server
	clientset *kubernetes.Clientset

	mu          sync.Mutex
	permissions map[string]Permission
}

// direct-tcpip data struct as specified in RFC4254, Section 7.2.
type localForwardChannelData struct {
	DestAddr string
	DestPort uint32

	OriginAddr string
	OriginPort uint32
}

func NewServer(
	logger *slog.Logger,
	privateKey gossh.Signer,
	keys map[string]Permission,
	clientset *kubernetes.Clientset,
) *Server {
	jumps := &Server{
		logger:      logger,
		permissions: keys,
		clientset:   clientset,
	}

	sshServer, _ := wish.NewServer(
		wish.WithMiddleware(
			bubbletea.Middleware(jumps.teaHandler),
			activeterm.Middleware(), // Bubble Tea apps usually require a PTY.
			logging.Middleware(),
		),
		func(s *ssh.Server) error {
			s.LocalPortForwardingCallback = func(ctx ssh.Context, bindHost string, bindPort uint32) bool {
				slog.Debug("Accepted forward", "host", bindHost, "port", bindPort, "user", ctx.User())

				return true
			}
			s.ChannelHandlers = map[string]ssh.ChannelHandler{
				"direct-tcpip": jumps.DirectTCPIPHandler,
				"session":      ssh.DefaultSessionHandler,
			}

			s.MaxTimeout = DeadlineTimeout
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

func (srv *Server) Handler(s ssh.Session) {
	srv.logger.Info("login",
		"username", s.User(),
		"ip", s.RemoteAddr().String(),
	)
	userConnections.WithLabelValues(s.User()).Inc()
	io.WriteString(s, fmt.Sprintf("user %s\n", s.User()))
	select {}
}

func (srv *Server) PermsForUser(user string) *Permission {
	srv.mu.Lock()
	defer srv.mu.Unlock()

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
	d := localForwardChannelData{}
	if err := gossh.Unmarshal(newChan.ExtraData(), &d); err != nil {
		newChan.Reject(gossh.ConnectionFailed, "error parsing forward data: "+err.Error())

		return
	}

	srv.logger.Debug("tcp fwd request", "user", ctx.User(), "host", d.DestAddr, "port", d.DestPort)

	if s.LocalPortForwardingCallback == nil || !s.LocalPortForwardingCallback(ctx, d.DestAddr, d.DestPort) {
		newChan.Reject(gossh.Prohibited, "port forwarding is disabled")

		return
	}

	ports, err := srv.KubernetesPortsForUser(ctx, ctx.User())
	if err != nil {
		newChan.Reject(gossh.ConnectionFailed, "error querying Kubernetes api: "+err.Error())

		srv.logger.Debug("error querying Kubernetes API",
			"error", err.Error(),
			"user", ctx.User(),
			"host", d.DestAddr,
			"port", d.DestPort,
		)

		return
	}

	var addr string
	// test for service request: srv.namespace.servicename
	if strings.HasPrefix(d.DestAddr, "svc.") {
		ds := strings.Split(d.DestAddr, ".")
		if len(ds) != 3 {
			newChan.Reject(gossh.ConnectionFailed, "invalid kubernetes format destination")

			return
		}
		namespace := ds[1]
		service := ds[2]
		daddr, ok := ports.MatchingService(service, namespace, int32(d.DestPort))
		if !ok {
			newChan.Reject(gossh.ConnectionFailed, "kubernetes destination not authorized")

			srv.logger.Warn("destination not authorized",
				"user", ctx.User(),
				"host", d.DestAddr,
				"port", d.DestPort,
			)

			return
		}
		addr = daddr
	} else {
		ds := strings.Split(d.DestAddr, ".")
		if len(ds) != 2 {
			newChan.Reject(gossh.ConnectionFailed, "invalid kubernetes format destination")

			return
		}
		namespace := ds[0]
		service := ds[1]
		daddr, ok := ports.MatchingService(service, namespace, int32(d.DestPort))
		if !ok {
			newChan.Reject(gossh.ConnectionFailed, "kubernetes destination not authorized")

			srv.logger.Warn("destination not authorized",
				"user", ctx.User(),
				"host", d.DestAddr,
				"port", d.DestPort,
			)

			return
		}
		addr = daddr
	}

	srv.logger.Debug("forward in progress",
		"user", ctx.User(),
		"host", d.DestAddr,
		"port", d.DestPort,
		"addr", addr,
	)

	userTunnels.WithLabelValues(ctx.User()).Inc()

	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var dialer net.Dialer
	dconn, err := dialer.DialContext(dctx, "tcp", addr)
	if err != nil {
		newChan.Reject(gossh.ConnectionFailed, fmt.Sprintf("%s while connecting to %s", err.Error(), addr))

		srv.logger.Warn("connection error",
			"error", err.Error(),
			"user", ctx.User(),
			"host", d.DestAddr,
			"port", d.DestPort,
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
