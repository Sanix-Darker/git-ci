package runners

import "testing"

func TestContainerShell_UsesBashByDefault(t *testing.T) {
	got := containerShell("catthehacker/ubuntu:act-22.04")
	if got != "/bin/bash" {
		t.Fatalf("expected /bin/bash, got %q", got)
	}
}

func TestContainerShell_UsesShForAlpineImages(t *testing.T) {
	cases := []string{
		"alpine:3.20",
		"myrepo/busybox",
		"busybox",
		"image-alpine",
		"image:alpine",
	}

	for _, img := range cases {
		if got := containerShell(img); got != "/bin/sh" {
			t.Fatalf("%q expected /bin/sh, got %q", img, got)
		}
	}
}

func TestSanitizeContainerName_NormalizesSpecialChars(t *testing.T) {
	got := sanitizeContainerName("Feature Build (Node_16)")
	want := "feature-build-node_16"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestSanitizeContainerName_ReturnsFallbackForEmpty(t *testing.T) {
	got := sanitizeContainerName("***")
	if got != "job" {
		t.Fatalf("expected fallback job, got %q", got)
	}
}

func TestParseMemoryString_ParsesUnits(t *testing.T) {
	tests := []struct {
		in  string
		out int64
	}{
		{"2g", 2 * 1024 * 1024 * 1024},
		{"512m", 512 * 1024 * 1024},
		{"1.5g", int64(1.5 * 1024 * 1024 * 1024)},
		{"1024k", 1024 * 1024},
		{"", 0},
		{"gibberish", 0},
	}

	for _, tc := range tests {
		if got := parseMemoryString(tc.in); got != tc.out {
			t.Fatalf("input %q expected %d, got %d", tc.in, tc.out, got)
		}
	}
}

func TestParseCPUString_ParsesCpuLimits(t *testing.T) {
	tests := []struct {
		in  string
		out int64
	}{
		{"2", 2048},
		{"0.5", 512},
		{"", 0},
		{"xyz", 0},
	}

	for _, tc := range tests {
		if got := parseCPUString(tc.in); got != tc.out {
			t.Fatalf("input %q expected %d, got %d", tc.in, tc.out, got)
		}
	}
}
