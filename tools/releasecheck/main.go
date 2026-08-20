package main

import (
	"debug/buildinfo"
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
)

type target struct {
	goos   string
	goarch string
	path   string
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: releasecheck GOOS/GOARCH=PATH [...]")
		os.Exit(2)
	}

	failed := false
	for _, value := range os.Args[1:] {
		target, err := parseTarget(value)
		if err == nil {
			err = verifyTarget(target)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "releasecheck: %v\n", err)
			failed = true
			continue
		}
		fmt.Printf("releasecheck: %s is portable %s/%s\n", target.path, target.goos, target.goarch)
	}
	if failed {
		os.Exit(1)
	}
}

func parseTarget(value string) (target, error) {
	platform, path, ok := strings.Cut(value, "=")
	if !ok || path == "" {
		return target{}, fmt.Errorf("invalid target %q; expected GOOS/GOARCH=PATH", value)
	}
	goos, goarch, ok := strings.Cut(platform, "/")
	if !ok || goos == "" || goarch == "" || strings.Contains(goarch, "/") {
		return target{}, fmt.Errorf("invalid platform %q; expected GOOS/GOARCH", platform)
	}
	return target{goos: goos, goarch: goarch, path: path}, nil
}

func verifyTarget(target target) error {
	info, err := buildinfo.ReadFile(target.path)
	if err != nil {
		return fmt.Errorf("read build information from %s: %w", target.path, err)
	}
	if err := validateBuildInfo(info, target); err != nil {
		return fmt.Errorf("%s: %w", target.path, err)
	}
	if target.goos == "linux" {
		if err := verifyStaticELF(target.path); err != nil {
			return err
		}
	}
	return nil
}

func validateBuildInfo(info *debug.BuildInfo, target target) error {
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}

	var problems []error
	if settings["CGO_ENABLED"] != "0" {
		problems = append(problems, fmt.Errorf("CGO_ENABLED=%q, want 0", settings["CGO_ENABLED"]))
	}
	if settings["GOOS"] != target.goos {
		problems = append(problems, fmt.Errorf("GOOS=%q, want %q", settings["GOOS"], target.goos))
	}
	if settings["GOARCH"] != target.goarch {
		problems = append(problems, fmt.Errorf("GOARCH=%q, want %q", settings["GOARCH"], target.goarch))
	}
	return errors.Join(problems...)
}

func verifyStaticELF(path string) error {
	file, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("open Linux ELF %s: %w", path, err)
	}
	defer file.Close()

	if file.Section(".interp") != nil {
		return fmt.Errorf("%s: dynamically linked ELF contains an interpreter", path)
	}
	libraries, err := file.ImportedLibraries()
	if err != nil {
		return fmt.Errorf("inspect imported libraries in %s: %w", path, err)
	}
	if len(libraries) != 0 {
		return fmt.Errorf("%s: dynamically linked ELF imports %s", path, strings.Join(libraries, ", "))
	}
	return nil
}
