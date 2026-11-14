package cli

import (
	"errors"
	"path/filepath"

	"github.com/Crocogiciel-Studio/podbox/internal/config"
	"github.com/Crocogiciel-Studio/podbox/internal/output"
	"github.com/Crocogiciel-Studio/podbox/internal/sandbox"
)

func (a *app) store() (sandbox.Store, config.Paths, error) {
	p, err := config.Resolve()
	if err != nil {
		return sandbox.Store{}, p, output.Wrap(output.ExitGeneric, err, "resolve directories")
	}
	return sandbox.Store{Root: filepath.Join(p.State, "sandboxes"), ConfigDir: p.Config}, p, nil
}

func (a *app) loadMeta(st sandbox.Store, name string) (sandbox.Meta, error) {
	m, err := st.Load(name)
	if errors.Is(err, sandbox.ErrNotFound) {
		return m, output.Errorf(output.ExitNotFound, "sandbox %q not found", name)
	}
	return m, err
}
