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
	"github.com/charmbracelet/keygen"
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

type EnvConfig struct {
	LogLevel        string `env:"LOG_LEVEL" envDefault:"INFO"`
	ConfigPath      string `env:"CONFIG_PATH" envDefault:"sshjump.yaml"`
	Host            string `env:"HOST" envDefault:"0.0.0.0"`
	Port            int    `env:"PORT" envDefault:"2222"`
	PrivateKeyPath  string `env:"PRIVATE_KEY_PATH"`
	HealthPort      int    `env:"HEALTH_PORT" envDefault:"6666"`
	HTTPMetricsPort int    `env:"METRICS_PORT" envDefault:"8888"`
	KubeConfigPath  string `env:"KUBE_CONFIG_PATH"` // Set the path of a kubeconfig file when running outside a cluster
	TSAuthKeyPath   string `env:"TS_AUTHKEY_PATH"`
}

var (
	grpcHealthServer  *grpc.Server
	sshServer         *ssh.Server
	httpMetricsServer *http.Server
)

func main() {
	var envCfg EnvConfig
	if err := env.Parse(&envCfg); err != nil {
		fmt.Println(err)
	}

	logger := createLogger(envCfg)

	keys, err := readPermission(logger, envCfg.ConfigPath)
	if err != nil {
		logger.Error("can't read permissions, aborting", "path", envCfg.ConfigPath, "error", err)
		os.Exit(1)
	}

	if len(keys) == 0 {
		logger.Error("configuration is empty, aborting", "path", envCfg.ConfigPath)
		os.Exit(1)
	}

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)

	clientset, err := createKubeClient(envCfg, logger)
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

			return err
		}
		logger.Info(fmt.Sprintf("gRPC health server listening at %s", haddr))

		return grpcHealthServer.Serve(hln)
	})

	signer, err := createSigner(logger, envCfg)
	if err != nil {
		logger.Error("can't get valid host key", "error", err)
		os.Exit(1)
	}

	s := NewServer(logger, healthServer, signer, keys, clientset)

	// start watching for config change
	if err := s.StartWatchConfig(ctx, envCfg.ConfigPath); err != nil {
		logger.Error("failed to start watch config", "error", err)
		os.Exit(1)
	}

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

	if sshServer != nil {
		sshServer.Shutdown(ctx) //nolint:errcheck
	}

	if grpcHealthServer != nil {
		grpcHealthServer.GracefulStop()
	}

	s.StopWatchConf()

	err = g.Wait()
	if err != nil {
		slog.Error("server returning an error", "error", err)
		os.Exit(2)
	}
}

func readPermission(logger *slog.Logger, path string) (map[string]Permission, error) {
	f, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("can't open config file %w", err)
	}

	var cfg SSHJumpConfig

	if err := yaml.Unmarshal(f, &cfg); err != nil {
		return nil, fmt.Errorf("can't unmarshal yaml config file %w", err)
	}

	return readKeys(logger, cfg), nil
}

func createSigner(logger *slog.Logger, envCfg EnvConfig) (gossh.Signer, error) {
	// no key provided generate one
	if envCfg.PrivateKeyPath == "" {
		k, err := keygen.New("id_ed25519", keygen.WithKeyType(keygen.Ed25519), keygen.WithWrite())
		if err != nil {
			return nil, fmt.Errorf("can't generate private key: %w", err)
		}

		gens, err := gossh.ParsePrivateKey(k.RawPrivateKey())
		if err != nil {
			return nil, fmt.Errorf("can't parse generated private key: %w", err)
		}

		logger.Warn("no host private key provided, generating ephemeral key")

		return gens, nil
	}
	// reading private key
	pemBytes, err := os.ReadFile(envCfg.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("can't read private key: %w", err)
	}

	locals, err := gossh.ParsePrivateKey(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("can't parse private key: %w", err)
	}

	return locals, nil
}

func createKubeClient(envCfg EnvConfig, logger *slog.Logger) (*kubernetes.Clientset, error) {
	var kubeConfig *rest.Config
	if envCfg.KubeConfigPath != "" {
		// use the current context in kubeconfig
		config, err := clientcmd.BuildConfigFromFlags("", envCfg.KubeConfigPath)
		if err != nil {
			logger.Error("can't create kubernetes config from path",
				"error", err,
				"path", envCfg.KubeConfigPath,
			)

			return nil, err
		}
		kubeConfig = config
	} else {
		// creates the in-cluster Kubernetes config
		config, err := rest.InClusterConfig()
		if err != nil {
			logger.Error("can't create kubernetes config from within cluster", "error", err)

			return nil, err
		}
		kubeConfig = config
	}

	// creates a new clientset
	return kubernetes.NewForConfig(kubeConfig)
}

func createLogger(envCfg EnvConfig) *slog.Logger {
	programLevel := new(slog.LevelVar)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource: true,
		Level:     programLevel,
	}))
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

	return logger
}
