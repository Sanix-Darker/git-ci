package store

// ErrNotFound reports that a requested store resource does not exist.
// Callers can use errors.As to inspect the resource and lookup key.
type ErrNotFound struct {
	Resource string
	Key      string
}

func (e *ErrNotFound) Error() string {
	if e == nil || e.Resource == "" {
		return "store: resource not found"
	}
	if e.Key == "" {
		return "store: " + e.Resource + " not found"
	}
	return "store: " + e.Resource + " not found: " + e.Key
}

// Is makes all not-found errors comparable by category with errors.Is.
func (e *ErrNotFound) Is(target error) bool {
	_, ok := target.(*ErrNotFound)
	return ok
}

// ErrConflict reports that a unique or otherwise exclusive resource value is
// already in use. Callers can use errors.As to inspect the conflicting field.
type ErrConflict struct {
	Resource string
	Field    string
	Value    string
}

func (e *ErrConflict) Error() string {
	if e == nil || e.Resource == "" {
		return "store: resource conflict"
	}
	if e.Field == "" {
		return "store: " + e.Resource + " conflict"
	}
	if e.Value == "" {
		return "store: " + e.Resource + " conflict on " + e.Field
	}
	return "store: " + e.Resource + " conflict on " + e.Field + ": " + e.Value
}

// Is makes all conflict errors comparable by category with errors.Is.
func (e *ErrConflict) Is(target error) bool {
	_, ok := target.(*ErrConflict)
	return ok
}

// ErrReleaseTransition describes a release lifecycle invariant that the
// caller can fix without retrying the same mutation unchanged.
type ErrReleaseTransition struct {
	Code    string
	Message string
}

func (e *ErrReleaseTransition) Error() string {
	if e == nil || e.Message == "" {
		return "store: invalid release transition"
	}
	return "store: " + e.Message
}

func (e *ErrReleaseTransition) Is(target error) bool {
	_, ok := target.(*ErrReleaseTransition)
	return ok
}

type invalidInputError struct {
	field  string
	reason string
}

func (e *invalidInputError) Error() string {
	return "store: invalid " + e.field + ": " + e.reason
}

func invalidInput(field, reason string) error {
	return &invalidInputError{field: field, reason: reason}
}
