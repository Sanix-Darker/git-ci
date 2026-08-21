package compatibility

import "testing"

func TestRegistryValidationFilteringAndAggregation(t *testing.T) {
	if err := Validate(entries); err != nil {
		t.Fatal(err)
	}
	report, err := Query(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Count < 45 || report.Counts.Total != report.Count || report.Counts.Supported+report.Counts.Partial+report.Counts.Planned+report.Counts.Unsupported != report.Count {
		t.Fatalf("unexpected complete report: %#v", report.Counts)
	}
	for index := 1; index < len(report.Items); index++ {
		left, right := report.Items[index-1], report.Items[index]
		if left.Provider == right.Provider && left.Category > right.Category {
			t.Fatalf("registry is not stably sorted at %q and %q", left.ID, right.ID)
		}
	}
	filtered, err := Query(Filter{Provider: " GitHub ", State: "PARTIAL", Search: "actions"})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Count == 0 || filtered.Counts.Partial != filtered.Count {
		t.Fatalf("filtered report = %#v", filtered)
	}
	for _, item := range filtered.Items {
		if item.Provider != "github" || item.State != StatePartial {
			t.Fatalf("filter leaked entry %#v", item)
		}
	}
	if _, err := Query(Filter{Provider: "circleci"}); err == nil {
		t.Fatal("invalid provider filter was accepted")
	}
}

func TestRegistryRejectsAmbiguousClaims(t *testing.T) {
	duplicate := entries[0]
	if err := Validate([]Entry{duplicate, duplicate}); err == nil {
		t.Fatal("duplicate IDs were accepted")
	}
	ambiguous := entries[0]
	ambiguous.ID = "ambiguous"
	ambiguous.State = StatePartial
	ambiguous.Limitation = ""
	if err := Validate([]Entry{ambiguous}); err == nil {
		t.Fatal("partial entry without a boundary was accepted")
	}
}
