package main

import (
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/gliderlabs/ssh"
)

type Server struct {
	logger *slog.Logger

	mu   sync.Mutex
	keys map[string]Permission
}

func (srv *Server) Handler(s ssh.Session) {
	srv.logger.Info("login", "username", s.User(), "ip", s.RemoteAddr().String())
	io.WriteString(s, fmt.Sprintf("user %s\n", s.User()))
	select {}
}

func (srv *Server) PublicKeyHandler(ctx ssh.Context, key ssh.PublicKey) bool {
	srv.mu.Lock()
	defer srv.mu.Unlock()

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
