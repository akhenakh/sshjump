package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/caarlos0/env/v11"
	"github.com/gliderlabs/ssh"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"gopkg.in/yaml.v3"
)

var (
	grpcHealthServer *grpc.Server
	sshServer        *ssh.Server
)

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

func main() {
	type EnvConfig struct {
		LogLevel   string `env:"LOG_LEVEL" envDefault:"INFO"`
		ConfigFile string `env:"CONFIG_FILE" envDefault:"sshjump.yaml"`
		Port       int    `env:"PORT" envDefault:"2222"`
		HealthPort int    `env:"HEALTH_PORT" envDefault:"6666"`
	}

	// TODO: reload config on changes

	var envCfg EnvConfig
	if err := env.Parse(&envCfg); err != nil {
		fmt.Println(err)
	}

	programLevel := new(slog.LevelVar)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: programLevel}))
	slog.SetDefault(logger)

	switch strings.ToUpper(envCfg.LogLevel) {
	case "DEBUG":
		programLevel.Set(slog.LevelDebug)
	case "INFO":
		programLevel.Set(slog.LevelInfo)
	case "WARN":
		programLevel.Set(slog.LevelWarn)
	case "ERROR":
		programLevel.Set(slog.LevelError)
	}

	f, err := os.ReadFile(envCfg.ConfigFile)
	if err != nil {
		logger.Error("can't open config file", "err", err, "path", envCfg.ConfigFile)
		os.Exit(1)
	}

	var cfg SSHJumpConfig

	if err := yaml.Unmarshal(f, &cfg); err != nil {
		logger.Error("can't unmarshal yaml config file", "err", err, "path", envCfg.ConfigFile)
		os.Exit(1)
	}

	keys := readKeys(logger, cfg)

	if len(keys) == 0 {
		logger.Error("no authorized keys provided")
		os.Exit(1)
	}

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)

	// catch termination
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(interrupt)

	g, ctx := errgroup.WithContext(ctx)

	// gRPC Health Server
	healthServer := health.NewServer()
	g.Go(func() error {
		grpcHealthServer = grpc.NewServer()

		healthpb.RegisterHealthServer(grpcHealthServer, healthServer)

		haddr := fmt.Sprintf(":%d", envCfg.HealthPort)
		hln, err := net.Listen("tcp", haddr)
		if err != nil {
			logger.Error("gRPC Health server: failed to listen", "error", err)
			os.Exit(2)
		}
		logger.Info(fmt.Sprintf("gRPC health server listening at %s", haddr))

		return grpcHealthServer.Serve(hln)
	})

	s := &Server{
		logger: logger,
		keys:   keys,
	}

	// ssh server
	g.Go(func() error {
		forwardHandler := &ssh.ForwardedTCPHandler{}

		sshServer = &ssh.Server{
			Handler: s.Handler,
			LocalPortForwardingCallback: ssh.LocalPortForwardingCallback(
				func(ctx ssh.Context, dhost string, dport uint32) bool {
					slog.Debug("Accepted forward", "host", dhost, "port", dport)

					return true
				}),
			ReversePortForwardingCallback: ssh.ReversePortForwardingCallback(
				func(ctx ssh.Context, host string, port uint32) bool {
					slog.Debug("attempt to bind granted", "host", host, "port", port)

					return true
				}),
			RequestHandlers: map[string]ssh.RequestHandler{
				"tcpip-forward":        forwardHandler.HandleSSHRequest,
				"cancel-tcpip-forward": forwardHandler.HandleSSHRequest,
			},
		}

		publicKeyOption := ssh.PublicKeyAuth(s.PublicKeyHandler)
		if err := sshServer.SetOption(publicKeyOption); err != nil {
			return err
		}

		l, err := net.Listen("tcp", fmt.Sprintf(":%d", envCfg.Port))
		if err != nil {
			return err
		}
		logger.Info("starting ssh server", "port", envCfg.Port)

		if err := sshServer.Serve(l); err != nil && err != ssh.ErrServerClosed {
			return err
		}

		return nil
	})

	select {
	case <-interrupt:
		cancel()

		break
	case <-ctx.Done():
		break
	}

	slog.Warn("received shutdown signal")

	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)

	if grpcHealthServer != nil {
		grpcHealthServer.GracefulStop()
	}

	if sshServer != nil {
		sshServer.Shutdown(ctx)
	}

	err = g.Wait()
	if err != nil {
		slog.Error("server returning an error", "error", err)
		os.Exit(1)
	}
}
