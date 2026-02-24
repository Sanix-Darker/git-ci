package runners

import (
	"testing"
	"time"
)

func TestTruncateText_Short(t *testing.T) {
	f := NewOutputFormatter(false)
	result := f.TruncateText("hello", 10)
	if result != "hello" {
		t.Errorf("expected 'hello', got %q", result)
	}
}

func TestTruncateText_Exact(t *testing.T) {
	f := NewOutputFormatter(false)
	result := f.TruncateText("hello", 5)
	if result != "hello" {
		t.Errorf("expected 'hello', got %q", result)
	}
}

func TestTruncateText_Long(t *testing.T) {
	f := NewOutputFormatter(false)
	result := f.TruncateText("hello world", 8)
	if result != "hello..." {
		t.Errorf("expected 'hello...', got %q", result)
	}
}

func TestTruncateText_Unicode(t *testing.T) {
	f := NewOutputFormatter(false)
	// 5 Japanese characters = 5 runes but many bytes
	result := f.TruncateText("こんにちは世界です", 7)
	if result != "こんにち..." {
		t.Errorf("expected 'こんにち...', got %q", result)
	}
}

func TestTruncateText_VeryShort(t *testing.T) {
	f := NewOutputFormatter(false)
	result := f.TruncateText("hello", 2)
	if result != "he" {
		t.Errorf("expected 'he', got %q", result)
	}
}

func TestFormatDuration_Milliseconds(t *testing.T) {
	f := NewOutputFormatter(false)
	result := f.FormatDuration(500 * time.Millisecond)
	if result != "500ms" {
		t.Errorf("expected '500ms', got %q", result)
	}
}

func TestFormatDuration_Seconds(t *testing.T) {
	f := NewOutputFormatter(false)
	result := f.FormatDuration(30 * time.Second)
	if result != "30.0s" {
		t.Errorf("expected '30.0s', got %q", result)
	}
}

func TestFormatDuration_Minutes(t *testing.T) {
	f := NewOutputFormatter(false)
	result := f.FormatDuration(5*time.Minute + 30*time.Second)
	if result != "5m 30s" {
		t.Errorf("expected '5m 30s', got %q", result)
	}
}

func TestFormatDuration_Hours(t *testing.T) {
	f := NewOutputFormatter(false)
	result := f.FormatDuration(2*time.Hour + 15*time.Minute)
	if result != "2h 15m" {
		t.Errorf("expected '2h 15m', got %q", result)
	}
}

func TestWrapText(t *testing.T) {
	f := NewOutputFormatter(false)
	lines := f.WrapText("hello world this is a test", 12)
	if len(lines) < 2 {
		t.Errorf("expected text to wrap into multiple lines, got %d", len(lines))
	}
}

func TestWrapText_Empty(t *testing.T) {
	f := NewOutputFormatter(false)
	lines := f.WrapText("", 10)
	if len(lines) != 1 || lines[0] != "" {
		t.Errorf("expected single empty string, got %v", lines)
	}
}

func TestNewOutputFormatter(t *testing.T) {
	f := NewOutputFormatter(true)
	if !f.Verbose {
		t.Error("expected Verbose to be true")
	}
	if f.Width != 80 {
		t.Errorf("expected Width 80, got %d", f.Width)
	}
	if !f.UseColor {
		t.Error("expected UseColor to be true")
	}
}

func TestSetColorEnabled(t *testing.T) {
	f := NewOutputFormatter(false)
	f.SetColorEnabled(false)
	if f.IsColorEnabled() {
		t.Error("expected colors to be disabled")
	}
}

func TestLine(t *testing.T) {
	f := NewOutputFormatter(false)
	line := f.Line('=')
	if len(line) == 0 {
		t.Error("expected non-empty line")
	}
}
