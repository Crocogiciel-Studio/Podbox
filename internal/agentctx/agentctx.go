// Package agentctx renders the context an agent reads about its podbox
// sandbox, and merges it into the agent's own instruction files. Pure: no I/O.
package agentctx

import (
	_ "embed"
	"errors"
	"strings"
)

//go:embed templates/claude-block.md
var claudeBlock string

//go:embed templates/claude-skill.md
var claudeSkill string

//go:embed templates/kiro-steering.md
var kiroSteering string

// Mode tells how a file is installed.
type Mode int

const (
	// Owned files are replaced as a whole.
	Owned Mode = iota
	// Block content is merged between markers into a file the user also edits.
	Block
)

// File is one file of the context.
type File struct {
	Path    string // relative to the agent's home, slash separated
	Content string
	Mode    Mode
}

// Files returns the context of an agent; none for an agent without context.
func Files(agent string) []File {
	switch agent {
	case "claude":
		return []File{
			{Path: ".claude/CLAUDE.md", Content: claudeBlock, Mode: Block},
			{Path: ".claude/skills/podbox-sandbox/SKILL.md", Content: claudeSkill, Mode: Owned},
		}
	case "kiro":
		return []File{{Path: ".kiro/steering/podbox.md", Content: kiroSteering, Mode: Owned}}
	}
	return nil
}

const (
	beginMarker = "<!-- podbox:begin -->"
	endMarker   = "<!-- podbox:end -->"
)

// ErrUnterminated reports a begin marker with no end marker after it.
var ErrUnterminated = errors.New("podbox begin marker without end marker")

// MergeBlock puts block between the podbox markers of existing: in place
// when the markers are there, appended otherwise. The user's text is kept.
func MergeBlock(existing, block string) (string, error) {
	region := beginMarker + "\n" + strings.TrimRight(block, "\n") + "\n" + endMarker + "\n"
	b := strings.Index(existing, beginMarker)
	if b < 0 {
		if strings.TrimSpace(existing) == "" {
			return region, nil
		}
		return strings.TrimRight(existing, "\n") + "\n\n" + region, nil
	}
	e := strings.Index(existing[b:], endMarker)
	if e < 0 {
		return "", ErrUnterminated
	}
	tail := existing[b+e+len(endMarker):]
	return existing[:b] + region + strings.TrimPrefix(tail, "\n"), nil
}
