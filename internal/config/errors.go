package config

import "errors"

// FieldError is one failure at a JSON path such as pools["a"].labels.
type FieldError struct {
	// Path is the JSON path. Empty when the failure has no single field.
	Path string
	Err  error
}

// Error returns "path: message", or the message alone without a path.
func (e *FieldError) Error() string {
	if e.Path == "" {
		return e.Err.Error()
	}
	return e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error so errors.Is and errors.As work.
func (e *FieldError) Unwrap() error {
	return e.Err
}

// errorList collects FieldErrors.
type errorList struct {
	errs []error
}

// err joins everything recorded, or returns nil.
func (l *errorList) err() error {
	return errors.Join(l.errs...)
}
