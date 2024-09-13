package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/gliderlabs/ssh"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"gopkg.in/yaml.v3"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"tailscale.com/tsnet"
)

var (
	grpcHealthServer  *grpc.Server
	sshServer         *ssh.Server
	httpMetricsServer *http.Server
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
		LogLevel        string `env:"LOG_LEVEL" envDefault:"INFO"`
		ConfigPath      string `env:"CONFIG_PATH" envDefault:"sshjump.yaml"`
		Host            string `env:"HOST" envDefault:"0.0.0.0"`
		Port            int    `env:"PORT" envDefault:"2222"`
		PrivateKeyPath  string `env:"PRIVATE_KEY_PATH" envDefault:"ssh_host_rsa_key"`
		HealthPort      int    `env:"HEALTH_PORT" envDefault:"6666"`
		HTTPMetricsPort int    `env:"METRICS_PORT" envDefault:"8888"`
		KubeConfigPath  string `env:"KUBE_CONFIG_PATH"` // Set the path of a kubeconfig file if sshjump is running outside of a cluster
		TSAuthKeyPath   string `env:"TS_AUTHKEY_PATH"`
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

	f, err := os.ReadFile(envCfg.ConfigPath)
	if err != nil {
		logger.Error("can't open config file", "err", err, "path", envCfg.ConfigPath)
		os.Exit(1)
	}

	var cfg SSHJumpConfig

	if err := yaml.Unmarshal(f, &cfg); err != nil {
		logger.Error("can't unmarshal yaml config file", "err", err, "path", envCfg.ConfigPath)
		os.Exit(1)
	}

	keys := readKeys(logger, cfg)

	if len(keys) == 0 {
	}

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)

	kubeConfig := &rest.Config{}
	if envCfg.KubeConfigPath != "" {
		// use the current context in kubeconfig
		config, err := clientcmd.BuildConfigFromFlags("", envCfg.KubeConfigPath)
		if err != nil {
			logger.Error("can't create kubernetes config from path",
				"error", err,
				"path", envCfg.KubeConfigPath,
			)
			os.Exit(1)
		}
		kubeConfig = config
	} else {
		// creates the in-cluster Kubernetes config
		config, err := rest.InClusterConfig()
		if err != nil {
			logger.Error("can't create kubernetes config from within cluster", "error", err)
			os.Exit(1)
		}
		kubeConfig = config
	}

	// creates a new clientset
	clientset, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		logger.Error("can't create kubernetes client", "error", err)
		os.Exit(1)
	}

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

		haddr := fmt.Sprintf("%s:%d", envCfg.Host, envCfg.HealthPort)
		hln, err := net.Listen("tcp", haddr)
		if err != nil {
			logger.Error("gRPC Health server: failed to listen", "error", err)
			os.Exit(2)
		}
		logger.Info(fmt.Sprintf("gRPC health server listening at %s", haddr))

		return grpcHealthServer.Serve(hln)
	})

	// reading private key
	pemBytes, err := os.ReadFile(envCfg.PrivateKeyPath)
	if err != nil {
		logger.Error("can't read private key", "error", err)
		os.Exit(2)
	}

	key, err := gossh.ParsePrivateKey(pemBytes)
	if err != nil {
		logger.Error("can't parse private key", "error", err)
		os.Exit(2)
	}

	s := NewServer(logger, key, keys, clientset)

	// web server metrics
	g.Go(func() error {
		httpMetricsServer = &http.Server{
			Addr:         fmt.Sprintf(":%d", envCfg.HTTPMetricsPort),
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
		}
		logger.Info(fmt.Sprintf("HTTP Metrics server listening at :%d", envCfg.HTTPMetricsPort))

		// Register Prometheus metrics handler.
		http.Handle("/metrics", promhttp.Handler())

		if err := httpMetricsServer.ListenAndServe(); err != http.ErrServerClosed {
			return err
		}

		return nil
	})

	// ssh server
	g.Go(func() error {
		var ln net.Listener

		// Starting Tailscale if key is present
		if envCfg.TSAuthKeyPath != "" {
			key, err := ioutil.ReadFile(envCfg.TSAuthKeyPath)
			if err != nil {
				return err
			}

			host := fmt.Sprintf("sshjump-%s", "todo-generate-clustername")
			srv := &tsnet.Server{
				AuthKey:   string(key),
				Ephemeral: true,
				Hostname:  host,
			}

			logger.Info("starting ssh server", "port", envCfg.Port, "tail", host)

			l, err := srv.Listen("tcp", fmt.Sprintf(":%d", envCfg.Port))
			if err != nil {
				return err
			}
			ln = l
		} else {
			l, err := net.Listen("tcp", fmt.Sprintf(":%d", envCfg.Port))
			if err != nil {
				return err
			}
			ln = l
		}

		logger.Info("starting ssh server", "port", envCfg.Port)

		if err := s.Serve(ln); err != nil && err != ssh.ErrServerClosed {
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

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if httpMetricsServer != nil {
		_ = httpMetricsServer.Shutdown(shutdownCtx)
	}

	if grpcHealthServer != nil {
		grpcHealthServer.GracefulStop()
	}

	if sshServer != nil {
		sshServer.Shutdown(ctx) //nolint:errcheck
	}

	err = g.Wait()
	if err != nil {
		slog.Error("server returning an error", "error", err)
		os.Exit(1)
	}
}
