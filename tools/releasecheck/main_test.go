package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestParseTarget(t *testing.T) {
	t.Parallel()

	target, err := parseTarget("linux/arm64=dist/gci-linux-arm64")
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	if target.goos != "linux" || target.goarch != "arm64" || target.path != "dist/gci-linux-arm64" {
		t.Fatalf("unexpected target: %#v", target)
	}

	for _, value := range []string{"linux/amd64", "linux=dist/gci", "/amd64=dist/gci", "linux/=dist/gci", "linux/amd64/extra=dist/gci"} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			if _, err := parseTarget(value); err == nil {
				t.Fatalf("parseTarget(%q) succeeded", value)
			}
		})
	}
}

func TestValidateBuildInfo(t *testing.T) {
	t.Parallel()

	valid := &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "CGO_ENABLED", Value: "0"},
		{Key: "GOOS", Value: "linux"},
		{Key: "GOARCH", Value: "amd64"},
	}}
	target := target{goos: "linux", goarch: "amd64"}
	if err := validateBuildInfo(valid, target); err != nil {
		t.Fatalf("valid build info rejected: %v", err)
	}

	invalid := &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "CGO_ENABLED", Value: "1"},
		{Key: "GOOS", Value: "darwin"},
		{Key: "GOARCH", Value: "arm64"},
	}}
	err := validateBuildInfo(invalid, target)
	if err == nil {
		t.Fatal("invalid build info accepted")
	}
	for _, problem := range []string{"CGO_ENABLED", "GOOS", "GOARCH"} {
		if !strings.Contains(err.Error(), problem) {
			t.Errorf("error %q does not mention %s", err, problem)
		}
	}
}
