package httpapi

import "testing"

func TestFormatTriggerTypeProducesReadableProvenance(t *testing.T) {
	for input, expected := range map[string]string{
		"webhook":    "WEBHOOK",
		"commit":     "COMMIT",
		"job_replay": "JOB REPLAY",
		"":           "UNKNOWN",
	} {
		if actual := formatTriggerType(input); actual != expected {
			t.Fatalf("formatTriggerType(%q) = %q, want %q", input, actual, expected)
		}
	}
}
