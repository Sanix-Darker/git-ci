package execution

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	dockercontainer "github.com/docker/docker/api/types/container"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	citypes "github.com/sanix-darker/git-ci/pkg/types"
)

const (
	defaultContainerMemory = int64(2 * 1024 * 1024 * 1024)
	defaultContainerCPUs   = int64(2_000_000_000)
	defaultContainerPIDs   = int64(512)
	containerReadyTimeout  = 90 * time.Second
)

var servicePortExpression = regexp.MustCompile(`\$\{\{\s*job\.services\.([A-Za-z0-9_.-]+)\.ports\[([0-9]+)\]\s*\}\}`)

type dockerJobSessionConfig struct {
	RunID     string
	JobID     string
	Workspace string
	Container *citypes.Container
	Services  map[string]*citypes.Service
	Secrets   map[string]string
}

type dockerExecRequest struct {
	WorkingDirectory string
	Environment      []string
	Command          []string
}

type dockerJobSession struct {
	client             *client.Client
	workspace          string
	jobContainerID     string
	serviceContainerID []string
	networkID          string
	networkName        string
	serviceEnvironment map[string]string
	serviceExpressions map[string]string
	closeOnce          sync.Once
	closeErr           error
}

type safeContainerOptions struct {
	CPUs       int64
	Memory     int64
	PIDs       int64
	User       string
	Entrypoint []string
	Health     *citypes.HealthCheck
	Init       bool
}

func newDockerJobSession(ctx context.Context, configuration dockerJobSessionConfig) (*dockerJobSession, error) {
	if err := validateRuntimeContract(configuration.Container, configuration.Services); err != nil {
		return nil, err
	}
	workspace, err := filepath.Abs(configuration.Workspace)
	if err != nil {
		return nil, fmt.Errorf("container runtime: resolve workspace: %w", err)
	}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("container runtime: create Docker client: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(pingCtx); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("container runtime: Docker is unavailable: %w", err)
	}
	session := &dockerJobSession{client: cli, workspace: workspace, serviceEnvironment: make(map[string]string), serviceExpressions: make(map[string]string)}
	cleanupOnError := func(cause error) (*dockerJobSession, error) {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		return nil, errors.Join(cause, session.Close(cleanupCtx))
	}
	if len(configuration.Services) > 0 {
		session.networkName = runtimeResourceName(configuration.RunID, configuration.JobID)
		created, err := cli.NetworkCreate(ctx, session.networkName, dockernetwork.CreateOptions{Driver: "bridge", Labels: map[string]string{"gci.managed": "true", "gci.run": configuration.RunID, "gci.job": configuration.JobID}})
		if err != nil {
			return cleanupOnError(fmt.Errorf("container runtime: create job network: %w", err))
		}
		session.networkID = created.ID
		if err := session.startServices(ctx, configuration); err != nil {
			return cleanupOnError(err)
		}
	}
	if configuration.Container != nil {
		if err := session.startJobContainer(ctx, configuration); err != nil {
			return cleanupOnError(err)
		}
	}
	return session, nil
}

func validateRuntimeContract(job *citypes.Container, services map[string]*citypes.Service) error {
	if job != nil {
		if strings.TrimSpace(job.Image) == "" {
			return errors.New("container runtime: job container image is required")
		}
		if job.Privileged || len(job.CapAdd) > 0 || len(job.SecurityOpt) > 0 {
			return errors.New("container runtime: privileged mode, added capabilities, and custom security options are not allowed in service mode")
		}
		if len(job.Volumes) > 0 {
			return errors.New("container runtime: repository-defined job volume mounts are not allowed in service mode")
		}
		if job.Network != "" || job.NetworkMode != "" {
			return errors.New("container runtime: repository-defined Docker networks are not allowed in service mode")
		}
		if _, err := parseSafeContainerOptions(job.Options, true); err != nil {
			return fmt.Errorf("container runtime: job options: %w", err)
		}
		if _, _, _, err := resourceLimits(job.CPUs, job.Memory, safeContainerOptions{}); err != nil {
			return fmt.Errorf("container runtime: job resources: %w", err)
		}
	}
	keys := make([]string, 0, len(services))
	for key := range services {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		service := services[key]
		if service == nil || strings.TrimSpace(service.Image) == "" {
			return fmt.Errorf("container runtime: service %q image is required", key)
		}
		if len(service.Volumes) > 0 || len(service.Networks) > 0 {
			return fmt.Errorf("container runtime: service %q cannot mount repository-defined volumes or join custom networks", key)
		}
		if _, err := parseSafeContainerOptions(service.Options, true); err != nil {
			return fmt.Errorf("container runtime: service %q options: %w", key, err)
		}
		if job == nil && len(service.Ports) == 0 {
			return fmt.Errorf("container runtime: host-executed service %q must declare at least one port", key)
		}
	}
	return nil
}

func (session *dockerJobSession) startJobContainer(ctx context.Context, configuration dockerJobSessionConfig) error {
	contract := configuration.Container
	options, _ := parseSafeContainerOptions(contract.Options, true)
	memory, cpus, pids, err := resourceLimits(contract.CPUs, contract.Memory, options)
	if err != nil {
		return err
	}
	if err := session.pullImage(ctx, contract.Image, contract.Credentials, configuration.Secrets); err != nil {
		return err
	}
	init := true
	config := &dockercontainer.Config{
		Image: contract.Image, WorkingDir: "/workspace", Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:         []string{"trap 'exit 0' TERM INT; while :; do sleep 3600; done"},
		Env:         environmentList(expandRuntimeSecrets(contract.Env, configuration.Secrets)),
		Healthcheck: dockerHealthCheck(mergeHealthCheck(contract.HealthCheck, options.Health)), User: firstNonEmpty(contract.User, options.User),
	}
	host := &dockercontainer.HostConfig{
		AutoRemove: false, Init: &init, Mounts: []mount.Mount{{Type: mount.TypeBind, Source: session.workspace, Target: "/workspace"}},
		Resources: dockercontainer.Resources{Memory: memory, MemorySwap: memory, NanoCPUs: cpus, PidsLimit: &pids},
	}
	var networking *dockernetwork.NetworkingConfig
	if session.networkName != "" {
		networking = &dockernetwork.NetworkingConfig{EndpointsConfig: map[string]*dockernetwork.EndpointSettings{session.networkName: {Aliases: []string{"job"}}}}
	}
	created, err := session.client.ContainerCreate(ctx, config, host, networking, nil, session.networkName+"-job")
	if err != nil {
		return fmt.Errorf("container runtime: create job container %q: %w", contract.Image, err)
	}
	session.jobContainerID = created.ID
	if err := session.client.ContainerStart(ctx, created.ID, dockercontainer.StartOptions{}); err != nil {
		return fmt.Errorf("container runtime: start job container %q: %w", contract.Image, err)
	}
	return session.waitReady(ctx, created.ID, "job container")
}

func (session *dockerJobSession) startServices(ctx context.Context, configuration dockerJobSessionConfig) error {
	keys := make([]string, 0, len(configuration.Services))
	for key := range configuration.Services {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		service := configuration.Services[key]
		options, _ := parseSafeContainerOptions(service.Options, true)
		if err := session.pullImage(ctx, service.Image, nil, configuration.Secrets); err != nil {
			return fmt.Errorf("service %q: %w", key, err)
		}
		exposed, bindings, err := nat.ParsePortSpecs(service.Ports)
		if err != nil {
			return fmt.Errorf("container runtime: service %q ports: %w", key, err)
		}
		if configuration.Container != nil {
			bindings = nil
		} else {
			for port, values := range bindings {
				if len(values) == 0 {
					values = []nat.PortBinding{{HostIP: "127.0.0.1"}}
				}
				for index := range values {
					values[index].HostIP = "127.0.0.1"
				}
				bindings[port] = values
			}
		}
		if options.CPUs == 0 {
			options.CPUs = 1_000_000_000
		}
		if options.Memory == 0 {
			options.Memory = 1 * 1024 * 1024 * 1024
		}
		if options.PIDs == 0 {
			options.PIDs = 256
		}
		memory, cpus, pids, err := resourceLimits("", "", options)
		if err != nil {
			return fmt.Errorf("container runtime: service %q resources: %w", key, err)
		}
		init := true
		entrypoint := append([]string(nil), service.Entrypoint...)
		if len(entrypoint) == 0 {
			entrypoint = append([]string(nil), options.Entrypoint...)
		}
		config := &dockercontainer.Config{
			Image: service.Image, Env: environmentList(expandRuntimeSecrets(service.Env, configuration.Secrets)), Cmd: append([]string(nil), service.Command...), Entrypoint: entrypoint,
			ExposedPorts: exposed, Healthcheck: dockerHealthCheck(mergeHealthCheck(service.HealthCheck, options.Health)), User: options.User,
		}
		host := &dockercontainer.HostConfig{AutoRemove: false, Init: &init, PortBindings: bindings, Resources: dockercontainer.Resources{Memory: memory, MemorySwap: memory, NanoCPUs: cpus, PidsLimit: &pids}}
		aliases := serviceAliases(key, service)
		networking := &dockernetwork.NetworkingConfig{EndpointsConfig: map[string]*dockernetwork.EndpointSettings{session.networkName: {Aliases: aliases}}}
		created, err := session.client.ContainerCreate(ctx, config, host, networking, nil, session.networkName+"-"+sanitizeRuntimeName(key))
		if err != nil {
			return fmt.Errorf("container runtime: create service %q: %w", key, err)
		}
		session.serviceContainerID = append(session.serviceContainerID, created.ID)
		if err := session.client.ContainerStart(ctx, created.ID, dockercontainer.StartOptions{}); err != nil {
			return fmt.Errorf("container runtime: start service %q: %w", key, err)
		}
		if err := session.waitReady(ctx, created.ID, "service "+key); err != nil {
			return err
		}
		if err := session.captureServicePorts(ctx, key, aliases, created.ID, exposed, configuration.Container != nil); err != nil {
			return err
		}
	}
	return nil
}

func (session *dockerJobSession) pullImage(ctx context.Context, name string, credentials, secrets map[string]string) error {
	options := dockerimage.PullOptions{}
	if len(credentials) > 0 {
		payload := map[string]string{
			"username": expandSecrets(credentials["username"], secrets), "password": expandSecrets(credentials["password"], secrets),
			"serveraddress": expandSecrets(firstNonEmpty(credentials["serveraddress"], credentials["server"]), secrets),
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("container runtime: encode registry credentials: %w", err)
		}
		options.RegistryAuth = base64.URLEncoding.EncodeToString(encoded)
	}
	reader, err := session.client.ImagePull(ctx, name, options)
	if err != nil {
		return fmt.Errorf("container runtime: pull image %q: %w", name, err)
	}
	defer reader.Close()
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return fmt.Errorf("container runtime: read image pull for %q: %w", name, err)
	}
	return nil
}

func (session *dockerJobSession) waitReady(ctx context.Context, containerID, label string) error {
	readyCtx, cancel := context.WithTimeout(ctx, containerReadyTimeout)
	defer cancel()
	started := time.Now()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		inspect, err := session.client.ContainerInspect(readyCtx, containerID)
		if err != nil {
			return fmt.Errorf("container runtime: inspect %s: %w", label, err)
		}
		if inspect.State == nil || !inspect.State.Running {
			status := "not running"
			if inspect.State != nil {
				status = fmt.Sprintf("%s (exit %d)", inspect.State.Status, inspect.State.ExitCode)
			}
			return fmt.Errorf("container runtime: %s is %s", label, status)
		}
		if inspect.State.Health == nil {
			if time.Since(started) >= 500*time.Millisecond {
				return nil
			}
		} else {
			switch inspect.State.Health.Status {
			case "healthy":
				return nil
			case "unhealthy":
				detail := "health check failed"
				if logs := inspect.State.Health.Log; len(logs) > 0 && strings.TrimSpace(logs[len(logs)-1].Output) != "" {
					detail = strings.TrimSpace(logs[len(logs)-1].Output)
				}
				return fmt.Errorf("container runtime: %s is unhealthy: %s", label, detail)
			}
		}
		select {
		case <-readyCtx.Done():
			return fmt.Errorf("container runtime: %s readiness timed out: %w", label, readyCtx.Err())
		case <-ticker.C:
		}
	}
}

func (session *dockerJobSession) captureServicePorts(ctx context.Context, key string, aliases []string, containerID string, exposed nat.PortSet, containerJob bool) error {
	inspect, err := session.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return fmt.Errorf("container runtime: inspect service %q ports: %w", key, err)
	}
	names := append([]string{key}, aliases...)
	for port := range exposed {
		containerPort := port.Port()
		hostPort := containerPort
		if !containerJob {
			bindings := inspect.NetworkSettings.Ports[port]
			if len(bindings) == 0 || bindings[0].HostPort == "" {
				return fmt.Errorf("container runtime: service %q port %s was not published", key, port)
			}
			hostPort = bindings[0].HostPort
		}
		for _, name := range names {
			normalized := normalizeEnvironmentKey(name)
			session.serviceEnvironment["GCI_SERVICE_"+normalized+"_"+containerPort+"_PORT"] = hostPort
			session.serviceExpressions[name+":"+containerPort] = hostPort
		}
	}
	host := "127.0.0.1"
	if containerJob {
		host = sanitizeRuntimeName(key)
	}
	for _, name := range names {
		session.serviceEnvironment["GCI_SERVICE_"+normalizeEnvironmentKey(name)+"_HOST"] = host
	}
	return nil
}

func (session *dockerJobSession) HasJobContainer() bool {
	return session != nil && session.jobContainerID != ""
}

func (session *dockerJobSession) ContainerWorkingDirectory(hostDirectory string) (string, error) {
	relative, err := filepath.Rel(session.workspace, hostDirectory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("container runtime: working directory leaves workspace")
	}
	if relative == "." {
		return "/workspace", nil
	}
	return "/workspace/" + filepath.ToSlash(relative), nil
}

func (session *dockerJobSession) Exec(ctx context.Context, request dockerExecRequest, stdout, stderr io.Writer) error {
	if !session.HasJobContainer() {
		return errors.New("container runtime: job container is not active")
	}
	created, err := session.client.ContainerExecCreate(ctx, session.jobContainerID, dockercontainer.ExecOptions{AttachStdout: true, AttachStderr: true, Cmd: request.Command, WorkingDir: request.WorkingDirectory, Env: request.Environment})
	if err != nil {
		return fmt.Errorf("container runtime: create step exec: %w", err)
	}
	attached, err := session.client.ContainerExecAttach(ctx, created.ID, dockercontainer.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("container runtime: attach step exec: %w", err)
	}
	defer attached.Close()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			attached.Close()
			killCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = session.client.ContainerKill(killCtx, session.jobContainerID, "KILL")
		case <-done:
		}
	}()
	_, copyErr := stdcopy.StdCopy(stdout, stderr, attached.Reader)
	close(done)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if copyErr != nil {
		return fmt.Errorf("container runtime: stream step output: %w", copyErr)
	}
	inspect, err := session.client.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return fmt.Errorf("container runtime: inspect step exec: %w", err)
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("container step exited with status %d", inspect.ExitCode)
	}
	return nil
}

func (session *dockerJobSession) resolveServiceExpressions(value string) string {
	return servicePortExpression.ReplaceAllStringFunc(value, func(expression string) string {
		matches := servicePortExpression.FindStringSubmatch(expression)
		if len(matches) == 3 {
			if resolved, ok := session.serviceExpressions[matches[1]+":"+matches[2]]; ok {
				return resolved
			}
		}
		return expression
	})
}

func (session *dockerJobSession) resolveEnvironment(values []string) []string {
	result := make(map[string]string, len(values)+len(session.serviceEnvironment))
	for _, value := range values {
		key, item, found := strings.Cut(value, "=")
		if found {
			result[key] = session.resolveServiceExpressions(item)
		}
	}
	for key, value := range session.serviceEnvironment {
		result[key] = value
	}
	return environmentList(result)
}

func (session *dockerJobSession) Close(ctx context.Context) error {
	if session == nil {
		return nil
	}
	session.closeOnce.Do(func() {
		var cleanup []error
		if session.jobContainerID != "" {
			cleanup = append(cleanup, session.client.ContainerRemove(ctx, session.jobContainerID, dockercontainer.RemoveOptions{Force: true, RemoveVolumes: true}))
		}
		for index := len(session.serviceContainerID) - 1; index >= 0; index-- {
			cleanup = append(cleanup, session.client.ContainerRemove(ctx, session.serviceContainerID[index], dockercontainer.RemoveOptions{Force: true, RemoveVolumes: true}))
		}
		if session.networkID != "" {
			cleanup = append(cleanup, session.client.NetworkRemove(ctx, session.networkID))
		}
		cleanup = append(cleanup, session.client.Close())
		session.closeErr = errors.Join(cleanup...)
	})
	return session.closeErr
}

func parseSafeContainerOptions(raw string, allowHealth bool) (safeContainerOptions, error) {
	var result safeContainerOptions
	words, err := splitOptionWords(raw)
	if err != nil {
		return result, err
	}
	for index := 0; index < len(words); index++ {
		name, value, hasValue := strings.Cut(words[index], "=")
		if name == "--init" || name == "--no-healthcheck" {
			if hasValue {
				return result, fmt.Errorf("%s does not take a value", name)
			}
			if name == "--init" {
				result.Init = true
			} else {
				result.Health = &citypes.HealthCheck{Disable: true}
			}
			continue
		}
		if !hasValue {
			index++
			if index >= len(words) {
				return result, fmt.Errorf("%s requires a value", name)
			}
			value = words[index]
		}
		switch name {
		case "--cpus":
			result.CPUs, err = parseCPULimit(value)
		case "--memory":
			result.Memory, err = parseMemoryLimit(value)
		case "--pids-limit":
			result.PIDs, err = strconv.ParseInt(value, 10, 64)
			if err == nil && (result.PIDs < 16 || result.PIDs > 4096) {
				err = errors.New("must be between 16 and 4096")
			}
		case "--user":
			result.User = strings.TrimSpace(value)
			if result.User == "" {
				err = errors.New("user cannot be empty")
			}
		case "--entrypoint":
			result.Entrypoint = []string{value}
		case "--health-cmd", "--health-interval", "--health-timeout", "--health-retries", "--health-start-period":
			if !allowHealth {
				err = errors.New("health options are not allowed here")
				break
			}
			if result.Health == nil {
				result.Health = &citypes.HealthCheck{}
			}
			switch name {
			case "--health-cmd":
				result.Health.Test = []string{"CMD-SHELL", value}
			case "--health-interval":
				result.Health.Interval, err = time.ParseDuration(value)
			case "--health-timeout":
				result.Health.Timeout, err = time.ParseDuration(value)
			case "--health-retries":
				result.Health.Retries, err = strconv.Atoi(value)
				if err == nil && (result.Health.Retries < 1 || result.Health.Retries > 100) {
					err = errors.New("must be between 1 and 100")
				}
			case "--health-start-period":
				result.Health.StartPeriod, err = time.ParseDuration(value)
			}
		default:
			return result, fmt.Errorf("option %q is not allowed in service mode", name)
		}
		if err != nil {
			return result, fmt.Errorf("%s: %w", name, err)
		}
	}
	return result, nil
}

func splitOptionWords(value string) ([]string, error) {
	var result []string
	var current strings.Builder
	var quote rune
	escaped, started := false, false
	flush := func() {
		if started {
			result = append(result, current.String())
			current.Reset()
			started = false
		}
	}
	for _, character := range value {
		if escaped {
			current.WriteRune(character)
			escaped, started = false, true
			continue
		}
		if character == '\\' && quote != '\'' {
			escaped, started = true, true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				current.WriteRune(character)
			}
			started = true
			continue
		}
		switch {
		case character == '\'' || character == '"':
			quote, started = character, true
		case unicode.IsSpace(character):
			flush()
		default:
			current.WriteRune(character)
			started = true
		}
	}
	if escaped || quote != 0 {
		return nil, errors.New("unterminated quote or escape")
	}
	flush()
	return result, nil
}

func resourceLimits(cpuValue, memoryValue string, options safeContainerOptions) (memory, cpus, pids int64, err error) {
	memory, cpus, pids = defaultContainerMemory, defaultContainerCPUs, defaultContainerPIDs
	if options.Memory > 0 {
		memory = options.Memory
	}
	if options.CPUs > 0 {
		cpus = options.CPUs
	}
	if options.PIDs > 0 {
		pids = options.PIDs
	}
	if strings.TrimSpace(memoryValue) != "" {
		memory, err = parseMemoryLimit(memoryValue)
		if err != nil {
			return 0, 0, 0, err
		}
	}
	if strings.TrimSpace(cpuValue) != "" {
		cpus, err = parseCPULimit(cpuValue)
		if err != nil {
			return 0, 0, 0, err
		}
	}
	return memory, cpus, pids, nil
}

func parseMemoryLimit(value string) (int64, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	multiplier := float64(1)
	for _, unit := range []struct {
		suffix string
		factor float64
	}{{"gb", 1 << 30}, {"mb", 1 << 20}, {"kb", 1 << 10}, {"g", 1 << 30}, {"m", 1 << 20}, {"k", 1 << 10}} {
		if strings.HasSuffix(normalized, unit.suffix) {
			normalized = strings.TrimSuffix(normalized, unit.suffix)
			multiplier = unit.factor
			break
		}
	}
	number, err := strconv.ParseFloat(normalized, 64)
	bytes := number * multiplier
	if err != nil || math.IsNaN(bytes) || math.IsInf(bytes, 0) || bytes < 16*(1<<20) || bytes > 64*(1<<30) {
		return 0, errors.New("memory must be between 16m and 64g")
	}
	return int64(bytes), nil
}

func parseCPULimit(value string) (int64, error) {
	number, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0.05 || number > 64 {
		return 0, errors.New("cpus must be between 0.05 and 64")
	}
	return int64(number * 1_000_000_000), nil
}

func dockerHealthCheck(value *citypes.HealthCheck) *dockercontainer.HealthConfig {
	if value == nil {
		return nil
	}
	if value.Disable {
		return &dockercontainer.HealthConfig{Test: []string{"NONE"}}
	}
	return &dockercontainer.HealthConfig{Test: append([]string(nil), value.Test...), Interval: value.Interval, Timeout: value.Timeout, Retries: value.Retries, StartPeriod: value.StartPeriod}
}

func mergeHealthCheck(base, override *citypes.HealthCheck) *citypes.HealthCheck {
	if override != nil {
		return override
	}
	return base
}

func serviceAliases(key string, service *citypes.Service) []string {
	seen := make(map[string]struct{})
	var result []string
	add := func(value string) {
		value = sanitizeRuntimeName(value)
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	add(key)
	add(service.Name)
	for _, alias := range strings.Fields(strings.ReplaceAll(service.Alias, ",", " ")) {
		add(alias)
	}
	return result
}

func environmentList(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func expandRuntimeSecrets(values, secrets map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = expandSecrets(value, secrets)
	}
	return result
}

func runtimeResourceName(runID, jobID string) string {
	base := "gci-" + sanitizeRuntimeName(runID) + "-" + sanitizeRuntimeName(jobID) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if len(base) > 63 {
		base = base[:63]
	}
	return strings.Trim(base, "-")
}

func sanitizeRuntimeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	previousDash := false
	for _, character := range value {
		valid := character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' || character == '-'
		if valid {
			result.WriteRune(character)
			previousDash = false
		} else if !previousDash {
			result.WriteByte('-')
			previousDash = true
		}
	}
	return strings.Trim(result.String(), "-")
}

func normalizeEnvironmentKey(value string) string {
	value = strings.ToUpper(value)
	var result strings.Builder
	for _, character := range value {
		if character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			result.WriteRune(character)
		} else {
			result.WriteByte('_')
		}
	}
	return strings.Trim(result.String(), "_")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
