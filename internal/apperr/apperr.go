// Package apperr defines classified, agent-actionable errors shared by all
// layers. Every error returned to an MCP client should be an *Error so the
// agent gets a category and a concrete next step.
package apperr

import (
	"errors"
	"fmt"
	"strings"
)

// Kind is a coarse error category. Each maps to a different next step.
type Kind string

// Error kinds.
const (
	Auth        Kind = "auth"         // fix credentials
	Permission  Kind = "permission"   // enable API / grant IAM / billing
	Quota       Kind = "quota"        // wait and retry, or lower volume
	NotFound    Kind = "not_found"    // model renamed/retired, wrong region, missing file
	Invalid     Kind = "invalid"      // fix the arguments
	Safety      Kind = "safety"       // rephrase the prompt
	Unavailable Kind = "unavailable"  // transient server problem
	Timeout     Kind = "timeout"      // retry with longer wait / check job
	Canceled    Kind = "canceled"     // client cancelled
	Budget      Kind = "budget"       // spending cap reached
	Confirm     Kind = "confirmation" // needs explicit cost approval
	Unknown     Kind = "unknown"
)

// Error is a classified error with a remediation hint.
type Error struct {
	Kind      Kind
	Message   string
	Hint      string
	Retryable bool
	Status    int
	Cause     error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Message)
	if e.Hint != "" {
		b.WriteString("\nHint: ")
		b.WriteString(e.Hint)
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Cause }

// New builds a classified error.
func New(kind Kind, msg, hint string) *Error {
	return &Error{Kind: kind, Message: msg, Hint: hint}
}

// Invalidf builds an Invalid error with a formatted message.
func Invalidf(format string, args ...any) *Error {
	return &Error{Kind: Invalid, Message: fmt.Sprintf(format, args...)}
}

// KindOf returns the classified kind of err, or Unknown.
func KindOf(err error) Kind {
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Kind
	}
	return Unknown
}

// As returns err as *Error when it is one.
func As(err error) (*Error, bool) {
	var ce *Error
	ok := errors.As(err, &ce)
	return ce, ok
}
