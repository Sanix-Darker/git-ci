package scheduler

import (
	"testing"
	"time"
)

func TestExpressionNextSupportsListsRangesStepsAndTimezone(t *testing.T) {
	expression, err := Parse("*/15 8-10 * * 1,3,5")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	location, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	after := time.Date(2026, time.August, 20, 7, 59, 0, 0, location)
	next, err := expression.Next(after, location)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	want := time.Date(2026, time.August, 21, 8, 0, 0, 0, location)
	if !next.Equal(want) {
		t.Fatalf("Next = %s, want %s", next, want)
	}
}

func TestExpressionUsesStandardDayOrWeekdaySemantics(t *testing.T) {
	expression, err := Parse("0 0 15 * 1")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	after := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	next, err := expression.Next(after, time.UTC)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	want := time.Date(2026, time.January, 5, 0, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("Next = %s, want %s", next, want)
	}
}

func TestExpressionRejectsUnsafeOrUnsupportedForms(t *testing.T) {
	for _, value := range []string{"", "* * * *", "* * * * * command", "60 * * * *", "*/0 * * * *", "5/2 * * * *", "10-2 * * * *"} {
		if _, err := Parse(value); err == nil {
			t.Errorf("Parse(%q) error = nil", value)
		}
	}
}

func TestSundaySevenIsNormalized(t *testing.T) {
	expression, err := Parse("0 12 * * 7")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	after := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC)
	next, err := expression.Next(after, time.UTC)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if next.Weekday() != time.Sunday || next.Hour() != 12 {
		t.Fatalf("Next = %s, want Sunday noon", next)
	}
}
