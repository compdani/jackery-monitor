package kasa

import (
	"errors"
	"fmt"
)

// Kind classifies a Kasa failure the same way the Python hierarchy does
// (KasaTransientError / KasaConfigError under KasaError).
type Kind int

const (
	// KindGeneric is an unclassified upstream failure (KasaError).
	KindGeneric Kind = iota
	// KindTransient is retryable: timeout, reset, device busy.
	KindTransient
	// KindConfig needs the user to fix something: missing/rejected credentials.
	// Never retried.
	KindConfig
)

// Error is the error type returned by this package.
type Error struct {
	Kind Kind
	Msg  string
	Err  error
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Err }

func newErr(kind Kind, cause error, format string, a ...any) *Error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, a...), Err: cause}
}

// IsTransient reports whether err is a retryable Kasa error.
func IsTransient(err error) bool { return kindOf(err) == KindTransient }

// IsConfig reports whether err is a user-fixable configuration error.
func IsConfig(err error) bool { return kindOf(err) == KindConfig }

func kindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return KindGeneric
}
