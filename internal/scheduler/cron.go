package scheduler

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Expression is a parsed five-field cron expression. It supports lists,
// ranges, and steps while intentionally excluding command and seconds fields.
type Expression struct {
	minutes, hours, days, months, weekdays field
	dayWildcard, weekdayWildcard           bool
}

type field map[int]struct{}

func Parse(expression string) (Expression, error) {
	parts := strings.Fields(expression)
	if len(parts) != 5 {
		return Expression{}, errors.New("schedule: cron must contain exactly five fields")
	}
	minutes, minuteWildcard, err := parseField(parts[0], 0, 59, false)
	if err != nil {
		return Expression{}, fmt.Errorf("schedule: minute: %w", err)
	}
	hours, _, err := parseField(parts[1], 0, 23, false)
	if err != nil {
		return Expression{}, fmt.Errorf("schedule: hour: %w", err)
	}
	days, dayWildcard, err := parseField(parts[2], 1, 31, false)
	if err != nil {
		return Expression{}, fmt.Errorf("schedule: day: %w", err)
	}
	months, _, err := parseField(parts[3], 1, 12, false)
	if err != nil {
		return Expression{}, fmt.Errorf("schedule: month: %w", err)
	}
	weekdays, weekdayWildcard, err := parseField(parts[4], 0, 7, true)
	if err != nil {
		return Expression{}, fmt.Errorf("schedule: weekday: %w", err)
	}
	_ = minuteWildcard
	return Expression{
		minutes: minutes, hours: hours, days: days, months: months, weekdays: weekdays,
		dayWildcard: dayWildcard, weekdayWildcard: weekdayWildcard,
	}, nil
}

// Next returns the first matching minute strictly after the supplied time in
// the requested timezone. Search is bounded to prevent malformed calendar
// combinations from consuming a scheduler worker indefinitely.
func (e Expression) Next(after time.Time, location *time.Location) (time.Time, error) {
	if location == nil {
		location = time.UTC
	}
	candidate := after.In(location).Truncate(time.Minute).Add(time.Minute)
	limit := candidate.AddDate(5, 0, 0)
	for !candidate.After(limit) {
		if e.matches(candidate) {
			return candidate, nil
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, errors.New("schedule: cron has no occurrence within five years")
}

func (e Expression) matches(value time.Time) bool {
	if !contains(e.minutes, value.Minute()) || !contains(e.hours, value.Hour()) || !contains(e.months, int(value.Month())) {
		return false
	}
	dayMatches := contains(e.days, value.Day())
	weekdayMatches := contains(e.weekdays, int(value.Weekday()))
	switch {
	case e.dayWildcard && e.weekdayWildcard:
		return true
	case e.dayWildcard:
		return weekdayMatches
	case e.weekdayWildcard:
		return dayMatches
	default:
		return dayMatches || weekdayMatches
	}
}

func parseField(raw string, minimum, maximum int, normalizeSunday bool) (field, bool, error) {
	if raw == "" {
		return nil, false, errors.New("field is empty")
	}
	wildcard := raw == "*"
	values := make(field)
	for _, item := range strings.Split(raw, ",") {
		base, stepRaw, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			parsed, err := strconv.Atoi(stepRaw)
			if err != nil || parsed <= 0 {
				return nil, false, fmt.Errorf("invalid step %q", stepRaw)
			}
			step = parsed
		}
		start, end := minimum, maximum
		switch {
		case base == "*":
		case strings.Contains(base, "-"):
			left, right, found := strings.Cut(base, "-")
			if !found {
				return nil, false, fmt.Errorf("invalid range %q", base)
			}
			var err error
			start, err = parseNumber(left, minimum, maximum)
			if err != nil {
				return nil, false, err
			}
			end, err = parseNumber(right, minimum, maximum)
			if err != nil {
				return nil, false, err
			}
			if start > end {
				return nil, false, fmt.Errorf("range %q is descending", base)
			}
		default:
			parsed, err := parseNumber(base, minimum, maximum)
			if err != nil {
				return nil, false, err
			}
			start, end = parsed, parsed
			if hasStep {
				return nil, false, fmt.Errorf("step requires wildcard or range in %q", item)
			}
		}
		for value := start; value <= end; value += step {
			if normalizeSunday && value == 7 {
				values[0] = struct{}{}
			} else {
				values[value] = struct{}{}
			}
		}
	}
	if len(values) == 0 {
		return nil, false, errors.New("field has no values")
	}
	return values, wildcard, nil
}

func parseNumber(raw string, minimum, maximum int) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("value %q must be between %d and %d", raw, minimum, maximum)
	}
	return value, nil
}

func contains(values field, value int) bool {
	_, exists := values[value]
	return exists
}
