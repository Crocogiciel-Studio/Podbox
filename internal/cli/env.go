package cli

import (
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"

	"github.com/Crocogiciel-Studio/podbox/internal/config"
	"github.com/Crocogiciel-Studio/podbox/internal/images"
	"github.com/Crocogiciel-Studio/podbox/internal/output"
	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
	"github.com/Crocogiciel-Studio/podbox/internal/sandbox"
)

func (a *app) store() (sandbox.Store, config.Paths, error) {
	p, err := config.Resolve()
	if err != nil {
		return sandbox.Store{}, p, output.Wrap(output.ExitGeneric, err, "resolve directories")
	}
	return sandbox.Store{Root: filepath.Join(p.State, "sandboxes"), ClonesRoot: filepath.Join(p.State, "clones"), ConfigDir: p.Config}, p, nil
}

type engine struct {
	rt      *runtime.Podman
	info    runtime.Info
	cfg     config.Config
	paths   config.Paths
	store   sandbox.Store
	builder *images.Builder
}

// engine connects to podman and loads the configuration.
func (a *app) engine(ctx context.Context) (*engine, error) {
	st, paths, err := a.store()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return nil, output.Wrap(output.ExitUsage, err, "config")
	}
	rt, err := runtime.NewPodman()
	if err != nil {
		return nil, output.Wrap(output.ExitPrereq, err, "podman not found (run `podbox doctor`)")
	}
	info, err := rt.Info(ctx)
	if err != nil {
		return nil, output.Wrap(output.ExitPrereq, err, "podman unusable (run `podbox doctor`)")
	}
	b := &images.Builder{RT: rt, Base: cfg.Images.Base, CAFile: cfg.CAFile, Arch: info.Arch, Progress: a.out.Err, GOOS: goruntime.GOOS, Getenv: os.Getenv}
	return &engine{rt: rt, info: info, cfg: cfg, paths: paths, store: st, builder: b}, nil
}
