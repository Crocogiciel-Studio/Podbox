// Package output renders command results as text or JSON.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Mode selects the rendering of results.
type Mode string

const (
	Text Mode = "text"
	JSON Mode = "json"
)

// ParseMode validates a --output value.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case Text, JSON:
		return Mode(s), nil
	}
	return "", Errorf(ExitUsage, "invalid --output %q: want text or json", s)
}

// Printer writes results and errors in one mode.
type Printer struct {
	Mode Mode
	Out  io.Writer
	Err  io.Writer
	seen map[string]bool // warnings already written
}

// Print writes v as JSON, or calls text in text mode.
func (p *Printer) Print(v any, text func(w io.Writer)) error {
	if p.Mode == JSON {
		enc := json.NewEncoder(p.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	text(p.Out)
	return nil
}

// Warn writes a warning to stderr in both modes, once per invocation.
func (p *Printer) Warn(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if p.seen[msg] {
		return
	}
	if p.seen == nil {
		p.seen = map[string]bool{}
	}
	p.seen[msg] = true
	fmt.Fprintf(p.Err, "warning: %s\n", msg)
}

// Info writes progress to stderr so stdout stays machine-readable.
func (p *Printer) Info(format string, a ...any) {
	fmt.Fprintf(p.Err, format+"\n", a...)
}

type jsonError struct {
	Error struct {
		Code    int    `json:"code"`
		Kind    string `json:"kind"`
		Message string `json:"message"`
	} `json:"error"`
}

// PrintError renders err and returns its exit code. ExitStatus is silent.
func (p *Printer) PrintError(err error) int {
	code := CodeOf(err)
	var s ExitStatus
	if errors.As(err, &s) {
		return code
	}
	if p.Mode == JSON {
		var je jsonError
		je.Error.Code = code
		je.Error.Kind = Kind(code)
		je.Error.Message = err.Error()
		_ = json.NewEncoder(p.Out).Encode(je)
		return code
	}
	fmt.Fprintf(p.Err, "error: %s\n", err)
	return code
}
