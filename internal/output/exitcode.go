package output

import (
	"errors"
	"fmt"
)

// Exit codes are part of the public contract; see README.
const (
	ExitOK       = 0
	ExitGeneric  = 1
	ExitUsage    = 2
	ExitPrereq   = 3
	ExitNotFound = 4
	ExitRuntime  = 5
	// ExitGitIntegrity: podbox git-check found suspicious .git changes.
	ExitGitIntegrity = 6
	// ExitMasks: a workspace path is unreadable or a recorded mask is
	// missing, so the agent is not started.
	ExitMasks = 7
)

var kinds = map[int]string{
	ExitGeneric:      "generic",
	ExitUsage:        "usage",
	ExitPrereq:       "prerequisite",
	ExitNotFound:     "not_found",
	ExitRuntime:      "runtime",
	ExitGitIntegrity: "git_integrity",
	ExitMasks:        "masks",
}

// Error is an error carrying its exit code.
type Error struct {
	Code int
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	if e.Err != nil && e.Msg != "" {
		return e.Msg + ": " + e.Err.Error()
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Msg
}

func (e *Error) Unwrap() error { return e.Err }

// Errorf builds an Error with the given exit code.
func Errorf(code int, format string, a ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// Wrap attaches an exit code to err; nil stays nil.
func Wrap(code int, err error, msg string) error {
	if err == nil {
		return nil
	}
	return &Error{Code: code, Msg: msg, Err: err}
}

// ExitStatus is returned by exec-like commands to propagate an inner exit
// code without printing anything.
type ExitStatus int

func (s ExitStatus) Error() string { return fmt.Sprintf("exit status %d", int(s)) }

// CodeOf maps an error to its exit code; untyped errors are generic.
func CodeOf(err error) int {
	if err == nil {
		return ExitOK
	}
	var s ExitStatus
	if errors.As(err, &s) {
		return int(s)
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ExitGeneric
}

// Kind names an exit code in JSON errors.
func Kind(code int) string {
	if k, ok := kinds[code]; ok {
		return k
	}
	return "exit"
}
