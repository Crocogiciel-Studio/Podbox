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

// maxRead bounds what is read from a file the agent controls.
const maxRead = 1 << 20

// viewFile is the policy view inside the agent container.
const viewFile = agentMount + "/" + AgentPolicyFile

const (
	// A FIFO or device is refused, not read. The read stops one byte past maxRead, so over the cap is detectable.
	readScript = `[ -e "$1" ] || exit 3; [ -f "$1" ] || { echo "not a regular file" >&2; exit 1; }; head -c 1048577 "$1"`
	// The write goes through a per-call temporary file next to the real
	// target (a symbolic link is followed, not replaced) and keeps its mode.
	writeScript = `mkdir -p "$(dirname "$1")" || exit 1
f=$(readlink -f "$1") || exit 1
tmp="$f.podbox-tmp.$$"
trap 'rm -f "$tmp"' EXIT
cat > "$tmp" || exit 1
if [ -e "$f" ]; then chmod "$(stat -c %a "$f")" "$tmp" || exit 1; fi
mv -T "$tmp" "$f"`
)

// installContext writes the agent's context into its home, merged with
// what is already there. It never fails the start: problems are warnings.
func (m *Manager) installContext(ctx context.Context, meta Meta) {
	for _, f := range agentctx.Files(meta.Agent) {
		if err := m.installFile(ctx, meta, f); err != nil {
			m.Warn("agent context %s of %s not installed: %v", f.Path, meta.Name, err)
		}
	}
	if len(agentctx.Files(meta.Agent)) == 0 {
		return
	}
	// An agent container created before the policy view lacks the mount.
	code, err := m.RT.Exec(ctx, agentContainer(meta.Name), runtime.ExecSpec{
		Command: []string{"test", "-r", viewFile}, User: AgentUser, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if err != nil || code != 0 {
		m.Warn("restart %s to get the policy view at %s", meta.Name, viewFile)
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
	if out.Len() > maxRead {
		return "", fmt.Errorf("%s is larger than %d bytes", target, maxRead)
	}
	return out.String(), nil
}
