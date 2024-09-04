package main

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"

	"github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
	"k8s.io/client-go/kubernetes"
)

type Server struct {
	logger *slog.Logger
	*ssh.Server
	clientset *kubernetes.Clientset

	mu          sync.Mutex
	permissions map[string]Permission
}

// direct-tcpip data struct as specified in RFC4254, Section 7.2
type localForwardChannelData struct {
	DestAddr string
	DestPort uint32

	OriginAddr string
	OriginPort uint32
}

func NewServer(logger *slog.Logger, keys map[string]Permission, clientset *kubernetes.Clientset) *Server {
	s := &Server{
		logger:      logger,
		permissions: keys,
		clientset:   clientset,
	}
	sshServer = &ssh.Server{
		Handler: s.Handler,
		Banner:  "SSHJump\n",
		LocalPortForwardingCallback: ssh.LocalPortForwardingCallback(func(ctx ssh.Context, dhost string, dport uint32) bool {
			slog.Debug("Accepted forward", "host", dhost, "port", dport, "user", ctx.User())

			return true
		}),
		ChannelHandlers: map[string]ssh.ChannelHandler{
			"direct-tcpip": s.DirectTCPIPHandler,
			"session":      ssh.DefaultSessionHandler,
		},
	}

	publicKeyOption := ssh.PublicKeyAuth(s.PublicKeyHandler)
	sshServer.SetOption(publicKeyOption)

	s.Server = sshServer

	return s
}

func (srv *Server) Handler(s ssh.Session) {
	srv.logger.Info("login",
		"username", s.User(),
		"ip", s.RemoteAddr().String(),
	)
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

// DirectTCPIPHandler handles TCP forward
func (srv *Server) DirectTCPIPHandler(s *ssh.Server, conn *gossh.ServerConn, newChan gossh.NewChannel, ctx ssh.Context) {
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

		return
	}

	var addr string
	// test for service request
	if strings.HasPrefix(d.DestAddr, "srv.") {
		ds := strings.Split(d.DestAddr, ".")
		if len(ds) != 4 {
			newChan.Reject(gossh.ConnectionFailed, "invalid kubernetes destination")

			return
		}
		namespace := ds[1]
		service := ds[2]
		addr, ok := ports.MatchingService(service, namespace, int32(d.DestPort))
		if !ok {
			newChan.Reject(gossh.ConnectionFailed, "kubernetes destination not authorized")

			return
		}
		addr = addr
	} else {

		ds := strings.Split(d.DestAddr, ".")
		if len(ds) != 3 {
			newChan.Reject(gossh.ConnectionFailed, "invalid kubernetes destination")

			return
		}
		namespace := ds[0]
		service := ds[1]
		addr, ok := ports.MatchingService(service, namespace, int32(d.DestPort))
		if !ok {
			newChan.Reject(gossh.ConnectionFailed, "kubernetes destination not authorized")

			return
		}
		addr = addr
	}

	var dialer net.Dialer
	dconn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		newChan.Reject(gossh.ConnectionFailed, err.Error())
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
		io.Copy(ch, dconn)
	}()
	go func() {
		defer ch.Close()
		defer dconn.Close()
		io.Copy(dconn, ch)
	}()
}
