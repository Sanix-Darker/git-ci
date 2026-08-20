package execution

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/client"
	citypes "github.com/sanix-darker/git-ci/pkg/types"
)

func TestSafeContainerOptions(t *testing.T) {
	options, err := parseSafeContainerOptions(`--cpus 1.5 --memory=512m --pids-limit 128 --health-cmd "redis-cli ping" --health-interval 2s --health-timeout 1s --health-retries 10`, true)
	if err != nil {
		t.Fatalf("parse options: %v", err)
	}
	if options.CPUs != 1_500_000_000 || options.Memory != 512*1024*1024 || options.PIDs != 128 {
		t.Fatalf("resources = %#v", options)
	}
	if options.Health == nil || strings.Join(options.Health.Test, " ") != "CMD-SHELL redis-cli ping" || options.Health.Retries != 10 {
		t.Fatalf("health = %#v", options.Health)
	}
	for _, unsafe := range []string{"--privileged", "--volume /:/host", "--network host", "--security-opt seccomp=unconfined"} {
		if _, err := parseSafeContainerOptions(unsafe, true); err == nil {
			t.Fatalf("unsafe option %q accepted", unsafe)
		}
	}
}

func TestRuntimeContractRejectsRepositoryMounts(t *testing.T) {
	err := validateRuntimeContract(&citypes.Container{Image: "alpine:3.20", Volumes: []string{"/:/host"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "volume mounts") {
		t.Fatalf("error = %v", err)
	}
}

func TestServiceExpressionAndEnvironmentResolution(t *testing.T) {
	session := &dockerJobSession{serviceExpressions: map[string]string{"postgres:5432": "49152"}, serviceEnvironment: map[string]string{"GCI_SERVICE_POSTGRES_HOST": "127.0.0.1"}}
	value := session.resolveServiceExpressions("connect ${{ job.services.postgres.ports[5432] }}")
	if value != "connect 49152" {
		t.Fatalf("resolved expression = %q", value)
	}
	environment := session.resolveEnvironment([]string{"DATABASE_PORT=${{ job.services.postgres.ports[5432] }}"})
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "DATABASE_PORT=49152") || !strings.Contains(joined, "GCI_SERVICE_POSTGRES_HOST=127.0.0.1") {
		t.Fatalf("environment = %v", environment)
	}
}

func TestDockerJobSessionExecutesAgainstHealthyService(t *testing.T) {
	if testing.Short() {
		t.Skip("Docker integration disabled in short mode")
	}
	probe, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("Docker client unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := probe.Ping(ctx); err != nil {
		_ = probe.Close()
		t.Skipf("Docker daemon unavailable: %v", err)
	}
	_ = probe.Close()
	session, err := newDockerJobSession(ctx, dockerJobSessionConfig{
		RunID: "integration-run", JobID: "integration-job", Workspace: t.TempDir(), Container: &citypes.Container{Image: "busybox:1.36.1", Memory: "128m", CPUs: "0.5"},
		Services: map[string]*citypes.Service{"echo": {
			Image: "busybox:1.36.1", Command: []string{"sh", "-c", "mkdir -p /www; echo ready >/www/index.html; httpd -f -p 8080 -h /www"}, Ports: []string{"8080"},
			HealthCheck: &citypes.HealthCheck{Test: []string{"CMD-SHELL", "wget -qO- http://127.0.0.1:8080 >/dev/null"}, Interval: time.Second, Timeout: time.Second, Retries: 20},
		}},
	})
	if err != nil {
		t.Fatalf("start Docker session: %v", err)
	}
	defer session.Close(context.Background())
	var stdout, stderr bytes.Buffer
	err = session.Exec(ctx, dockerExecRequest{WorkingDirectory: "/workspace", Command: []string{"sh", "-eu", "-c", "wget -qO- http://echo:8080"}, Environment: []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("exec: %v; stderr=%s", err, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "ready" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}
