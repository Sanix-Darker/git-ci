package handlers

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sanix-darker/git-ci/internal/service"
	cli "github.com/urfave/cli/v2"
)

// CmdServe runs the authenticated, loopback-only git-ci control service.
func CmdServe(c *cli.Context) error {
	projectRoots := c.StringSlice("projects-root")
	if len(projectRoots) == 0 {
		projectRoots = []string{c.String("workdir")}
	}
	control, err := service.New(c.Context, service.Config{
		Listen:         c.String("listen"),
		StateDir:       c.String("state-dir"),
		StaticDir:      c.String("static-dir"),
		ProjectRoots:   projectRoots,
		AdminTokenFile: c.String("admin-token-file"),
		SessionKeyFile: c.String("session-key-file"),
		SessionTTL:     c.Duration("session-ttl"),
		MaxBodyBytes:   c.Int64("max-body-bytes"),
		Version:        os.Getenv("GIT_CI_VERSION"),
	})
	if err != nil {
		return err
	}
	defer func() { _ = control.Close() }()
	if token := control.BootstrapToken(); token != "" {
		_, _ = fmt.Fprintf(c.App.Writer, "Bootstrap admin token (shown once): %s\n", token)
	}
	ctx, stop := signal.NotifyContext(c.Context, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return control.Run(ctx)
}
