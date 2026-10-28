package config

import (
	"errors"
	"fmt"
)

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

// add records a new message at path.
func (l *errorList) add(path, format string, args ...any) {
	l.errs = append(l.errs, &FieldError{Path: path, Err: fmt.Errorf(format, args...)})
}

// addErr records an existing error at path, keeping its wrapped causes.
func (l *errorList) addErr(path string, err error) {
	l.errs = append(l.errs, &FieldError{Path: path, Err: err})
}

// err joins everything recorded, or returns nil.
func (l *errorList) err() error {
	return errors.Join(l.errs...)
}
