package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/Crocogiciel-Studio/podbox/internal/agentctx"
	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
)

// agentHome is the agent's home inside the container.
const agentHome = "/home/agent"

// exitAbsent is the exit code of the read script for a missing file.
const exitAbsent = 3

const (
	readScript  = `[ -e "$1" ] || exit 3; cat "$1"`
	writeScript = `mkdir -p "$(dirname "$1")" && cat > "$1.podbox-tmp" && mv "$1.podbox-tmp" "$1"`
)

// installContext writes the agent's context into its home, merged with
// what is already there. It never fails the start: problems are warnings.
func (m *Manager) installContext(ctx context.Context, meta Meta) {
	for _, f := range agentctx.Files(meta.Agent) {
		if err := m.installFile(ctx, meta, f); err != nil {
			m.Warn("agent context %s of %s not installed: %v", f.Path, meta.Name, err)
		}
	}
}

func (m *Manager) installFile(ctx context.Context, meta Meta, f agentctx.File) error {
	target := path.Join(agentHome, f.Path)
	content := f.Content
	if f.Mode == agentctx.Block {
		cur, err := m.readHome(ctx, meta, target)
		if err != nil {
			return err
		}
		if content, err = agentctx.MergeBlock(cur, f.Content); err != nil {
			return fmt.Errorf("%s left unchanged: %w", target, err)
		}
	}
	var stderr bytes.Buffer
	code, err := m.RT.Exec(ctx, agentContainer(meta.Name), runtime.ExecSpec{
		Command: []string{"sh", "-c", writeScript, "sh", target}, User: AgentUser, Interactive: true,
		Stdin: strings.NewReader(content), Stdout: &bytes.Buffer{}, Stderr: &stderr,
	})
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("write %s: exit %d: %s", target, code, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// readHome returns the file's content, empty when it does not exist.
func (m *Manager) readHome(ctx context.Context, meta Meta, target string) (string, error) {
	var out, stderr bytes.Buffer
	code, err := m.RT.Exec(ctx, agentContainer(meta.Name), runtime.ExecSpec{
		Command: []string{"sh", "-c", readScript, "sh", target}, User: AgentUser, Stdout: &out, Stderr: &stderr,
	})
	switch {
	case err != nil:
		return "", err
	case code == exitAbsent:
		return "", nil
	case code != 0:
		return "", fmt.Errorf("read %s: exit %d: %s", target, code, strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}
