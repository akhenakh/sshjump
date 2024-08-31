package main

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"

	"github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
)

type Server struct {
	logger *slog.Logger
	*ssh.Server
	mu   sync.Mutex
	keys map[string]Permission
}

// direct-tcpip data struct as specified in RFC4254, Section 7.2
type localForwardChannelData struct {
	DestAddr string
	DestPort uint32

	OriginAddr string
	OriginPort uint32
}

func NewServer(logger *slog.Logger, keys map[string]Permission) *Server {
	s := &Server{
		logger: logger,
		keys:   keys,
	}
	sshServer = &ssh.Server{
		Handler: s.Handler,
		Banner:  "SSHJump\n",
		LocalPortForwardingCallback: ssh.LocalPortForwardingCallback(func(ctx ssh.Context, dhost string, dport uint32) bool {
			slog.Debug("Accepted forward", "host", dhost, "port", dport)

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
	srv.logger.Info("login", "username", s.User(), "ip", s.RemoteAddr().String())
	io.WriteString(s, fmt.Sprintf("user %s\n", s.User()))
}

func (srv *Server) PublicKeyHandler(ctx ssh.Context, key ssh.PublicKey) bool {
	srv.mu.Lock()
	defer srv.mu.Unlock()

	// looking for a matching username
	perm, ok := srv.keys[ctx.User()]
	if !ok {
		srv.logger.Warn("no such username", "username", ctx.User(), "ip", ctx.RemoteAddr().String())

		return false
	}

	// validate key
	if ssh.KeysEqual(key, perm.Key) {
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

	if s.LocalPortForwardingCallback == nil || !s.LocalPortForwardingCallback(ctx, d.DestAddr, d.DestPort) {
		newChan.Reject(gossh.Prohibited, "port forwarding is disabled")
		return
	}

	dest := net.JoinHostPort(d.DestAddr, strconv.FormatInt(int64(d.DestPort), 10))

	var dialer net.Dialer
	dconn, err := dialer.DialContext(ctx, "tcp", dest)
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
