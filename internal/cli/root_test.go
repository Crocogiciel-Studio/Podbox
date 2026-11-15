package cli

import (
	"testing"

	"github.com/Crocogiciel-Studio/podbox/internal/output"
)

func TestUsageErrors(t *testing.T) {
	cases := [][]string{
		{"--output", "yaml", "version"},
		{"version", "extra"},
		{"nosuchcommand"},
		{"version", "--nosuchflag"},
	}
	for _, args := range cases {
		if got := run(args); got != output.ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, got, output.ExitUsage)
		}
	}
	if got := run([]string{"version", "-o", "json"}); got != output.ExitOK {
		t.Errorf("version: exit %d", got)
	}
}
