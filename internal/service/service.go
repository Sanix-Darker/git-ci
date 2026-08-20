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
	"github.com/sanix-darker/git-ci/internal/httpapi"
	"github.com/sanix-darker/git-ci/internal/projects"
	"github.com/sanix-darker/git-ci/internal/store"
)

type Config struct {
	Listen          string
	StateDir        string
	StaticDir       string
	ProjectRoots    []string
	AdminTokenFile  string
	SessionKeyFile  string
	SessionTTL      time.Duration
	MaxBodyBytes    int64
	Version         string
	ShutdownTimeout time.Duration
}

type Service struct {
	config         Config
	store          *store.Store
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
	handler, err := httpapi.New(httpapi.Config{
		Auth:         manager,
		Store:        database,
		Projects:     registry,
		StaticDir:    config.StaticDir,
		Version:      config.Version,
		MaxBodyBytes: config.MaxBodyBytes,
	})
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("service: HTTP API: %w", err)
	}
	return &Service{
		config:         config,
		store:          database,
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
	go func() {
		serveErrors <- server.Serve(listener)
	}()

	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("service: serve: %w", err)
	case <-ctx.Done():
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
