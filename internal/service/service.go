// Package service wires the durable git-ci control plane.
package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sanix-darker/git-ci/internal/auth"
	"github.com/sanix-darker/git-ci/internal/execution"
	"github.com/sanix-darker/git-ci/internal/httpapi"
	"github.com/sanix-darker/git-ci/internal/projects"
	"github.com/sanix-darker/git-ci/internal/runnerinventory"
	"github.com/sanix-darker/git-ci/internal/scheduler"
	"github.com/sanix-darker/git-ci/internal/secrets"
	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/internal/triggers"
	"github.com/sanix-darker/git-ci/internal/webhooks"
)

type Config struct {
	Listen          string
	StateDir        string
	StaticDir       string
	ProjectRoots    []string
	RunnerLabels    []string
	RunnerTags      []string
	RunnerGroup     string
	AdminTokenFile  string
	SessionKeyFile  string
	SecretKeyFile   string
	SessionTTL      time.Duration
	MaxBodyBytes    int64
	Version         string
	ShutdownTimeout time.Duration
}

type Service struct {
	config         Config
	store          *store.Store
	execution      *execution.Manager
	scheduler      *scheduler.Manager
	commitTriggers *triggers.Manager
	handler        http.Handler
	bootstrapToken string
	closeOnce      sync.Once
	closeErr       error
}

func New(ctx context.Context, config Config) (*Service, error) {
	config, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(config.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("service: create state directory: %w", err)
	}
	if err := os.Chmod(config.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("service: secure state directory: %w", err)
	}

	registry, err := projects.NewRegistry(config.ProjectRoots)
	if err != nil {
		return nil, fmt.Errorf("service: project registry: %w", err)
	}
	managerOptions := []auth.Option{}
	if config.SessionTTL > 0 {
		managerOptions = append(managerOptions, auth.WithSessionTTL(config.SessionTTL))
	}
	manager, bootstrapToken, err := auth.NewManager(config.AdminTokenFile, config.SessionKeyFile, managerOptions...)
	if err != nil {
		return nil, fmt.Errorf("service: authentication: %w", err)
	}
	database, err := store.Open(ctx, filepath.Join(config.StateDir, "gci.db"))
	if err != nil {
		return nil, fmt.Errorf("service: persistence: %w", err)
	}
	secretManager, err := secrets.NewManager(database, config.SecretKeyFile)
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("service: secret manager: %w", err)
	}
	executionManager, err := execution.NewManager(database,
		execution.WithSecretResolver(secretManager),
		execution.WithWorkspaceRoot(filepath.Join(config.StateDir, "workspaces")),
		execution.WithDataRoot(filepath.Join(config.StateDir, "data")),
		execution.WithRunnerInventory(runnerinventory.Local(runnerinventory.Config{
			Labels: config.RunnerLabels, Tags: config.RunnerTags, Group: config.RunnerGroup,
		})),
	)
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("service: execution manager: %w", err)
	}
	scheduleManager, err := scheduler.NewManager(database, executionManager)
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("service: scheduler: %w", err)
	}
	webhookManager, err := webhooks.NewManager(database, executionManager)
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("service: webhook manager: %w", err)
	}
	commitTriggerManager, err := triggers.NewManager(database, executionManager)
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("service: commit trigger manager: %w", err)
	}
	handler, err := httpapi.New(httpapi.Config{
		Auth:           manager,
		Store:          database,
		Projects:       registry,
		StaticDir:      config.StaticDir,
		Version:        config.Version,
		MaxBodyBytes:   config.MaxBodyBytes,
		Execution:      executionManager,
		Secrets:        secretManager,
		Scheduler:      scheduleManager,
		Webhooks:       webhookManager,
		CommitTriggers: commitTriggerManager,
	})
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("service: HTTP API: %w", err)
	}
	return &Service{
		config:         config,
		store:          database,
		execution:      executionManager,
		scheduler:      scheduleManager,
		commitTriggers: commitTriggerManager,
		handler:        handler,
		bootstrapToken: bootstrapToken,
	}, nil
}

func (s *Service) Handler() http.Handler {
	return s.handler
}

func (s *Service) BootstrapToken() string {
	return s.bootstrapToken
}

func (s *Service) Run(ctx context.Context) error {
	if s == nil || s.handler == nil {
		return errors.New("service: nil service")
	}
	listener, err := net.Listen("tcp", s.config.Listen)
	if err != nil {
		return fmt.Errorf("service: listen on %s: %w", s.config.Listen, err)
	}
	server := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serveErrors := make(chan error, 1)
	workerErrors := make(chan error, 3)
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	go func() {
		serveErrors <- server.Serve(listener)
	}()
	go func() {
		workerErrors <- s.execution.Run(workerCtx)
	}()
	go func() {
		workerErrors <- s.scheduler.Run(workerCtx)
	}()
	go func() {
		workerErrors <- s.commitTriggers.Run(workerCtx)
	}()

	select {
	case err := <-serveErrors:
		stopWorker()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("service: serve: %w", err)
	case err := <-workerErrors:
		if err == nil && ctx.Err() != nil {
			return nil
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.config.ShutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		if err != nil {
			return fmt.Errorf("service: execution worker: %w", err)
		}
		return errors.New("service: execution worker stopped")
	case <-ctx.Done():
		stopWorker()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.config.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("service: graceful shutdown: %w", err)
		}
		err := <-serveErrors
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("service: serve after shutdown: %w", err)
		}
		return nil
	}
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closeErr = s.store.Close()
	})
	return s.closeErr
}

func normalizeConfig(config Config) (Config, error) {
	config.Listen = strings.TrimSpace(config.Listen)
	if config.Listen == "" {
		config.Listen = "127.0.0.1:8087"
	}
	if !isLoopbackListen(config.Listen) {
		return Config{}, fmt.Errorf("service: listen address %q is not loopback; use a trusted reverse proxy", config.Listen)
	}
	config.StateDir = strings.TrimSpace(config.StateDir)
	if config.StateDir == "" {
		config.StateDir = ".gci-service"
	}
	absStateDir, err := filepath.Abs(config.StateDir)
	if err != nil {
		return Config{}, fmt.Errorf("service: state directory: %w", err)
	}
	config.StateDir = absStateDir
	if config.AdminTokenFile == "" {
		config.AdminTokenFile = filepath.Join(config.StateDir, "admin.token")
	}
	if config.SessionKeyFile == "" {
		config.SessionKeyFile = filepath.Join(config.StateDir, "session.key")
	}
	if config.SecretKeyFile == "" {
		config.SecretKeyFile = filepath.Join(config.StateDir, "secret.key")
	}
	if len(config.ProjectRoots) == 0 {
		return Config{}, errors.New("service: at least one project root is required")
	}
	if config.MaxBodyBytes <= 0 {
		config.MaxBodyBytes = httpapi.DefaultMaxBodyBytes
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = 10 * time.Second
	}
	return config, nil
}

func isLoopbackListen(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
