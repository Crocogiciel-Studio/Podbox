package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestPrintModes(t *testing.T) {
	v := map[string]string{"a": "b"}
	var out bytes.Buffer
	p := &Printer{Mode: JSON, Out: &out, Err: io.Discard}
	if err := p.Print(v, func(w io.Writer) { fmt.Fprint(w, "text") }); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got["a"] != "b" {
		t.Fatalf("json mode: %q %v", out.String(), err)
	}
	out.Reset()
	p.Mode = Text
	_ = p.Print(v, func(w io.Writer) { fmt.Fprint(w, "text") })
	if out.String() != "text" {
		t.Fatalf("text mode: %q", out.String())
	}
}

func TestParseMode(t *testing.T) {
	if _, err := ParseMode("yaml"); CodeOf(err) != ExitUsage {
		t.Fatalf("yaml: %v", err)
	}
	if m, err := ParseMode("json"); err != nil || m != JSON {
		t.Fatal(m, err)
	}
}

func TestCodeOf(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, ExitOK},
		{errors.New("x"), ExitGeneric},
		{Errorf(ExitNotFound, "nope"), ExitNotFound},
		{fmt.Errorf("wrapped: %w", Errorf(ExitPrereq, "p")), ExitPrereq},
		{Wrap(ExitRuntime, errors.New("boom"), "podman"), ExitRuntime},
		{ExitStatus(42), 42},
	}
	for _, c := range cases {
		if got := CodeOf(c.err); got != c.want {
			t.Errorf("CodeOf(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

func TestPrintErrorJSON(t *testing.T) {
	var out, errw bytes.Buffer
	p := &Printer{Mode: JSON, Out: &out, Err: &errw}
	code := p.PrintError(Errorf(ExitNotFound, "sandbox %q not found", "x"))
	if code != ExitNotFound {
		t.Fatalf("code %d", code)
	}
	var je jsonError
	if err := json.Unmarshal(out.Bytes(), &je); err != nil {
		t.Fatal(err)
	}
	if je.Error.Code != ExitNotFound || je.Error.Kind != "not_found" || !strings.Contains(je.Error.Message, "not found") {
		t.Fatalf("%+v", je)
	}
	if errw.Len() != 0 {
		t.Fatalf("stderr not empty: %q", errw.String())
	}
}

func TestPrintErrorSilentExitStatus(t *testing.T) {
	var out, errw bytes.Buffer
	p := &Printer{Mode: Text, Out: &out, Err: &errw}
	if code := p.PrintError(ExitStatus(7)); code != 7 || out.Len()+errw.Len() != 0 {
		t.Fatalf("code %d out %q err %q", code, out.String(), errw.String())
	}
}
