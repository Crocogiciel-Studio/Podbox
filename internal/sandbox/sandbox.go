// Package sandbox creates and manages agent sandboxes: one proxy container
// and one agent container on a private internal network.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Crocogiciel-Studio/podbox/internal/agents"
	"github.com/Crocogiciel-Studio/podbox/internal/config"
	"github.com/Crocogiciel-Studio/podbox/internal/images"
	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
	"github.com/Crocogiciel-Studio/podbox/internal/workspace"
)

// ErrExists reports a name already in use.
var ErrExists = errors.New("sandbox already exists")

// ErrNotRunning reports a stopped sandbox.
var ErrNotRunning = errors.New("sandbox is not running")

// Manager drives sandbox lifecycles.
type Manager struct {
	RT     runtime.Runtime
	Store  Store
	Images *images.Builder
	Config config.Config
	Info   runtime.Info
	GOOS   string
	Getenv func(string) string
	Warn   func(format string, a ...any)
	// Home and Owned bound the workspaces accepted (see workspace.CheckRoot).
	Home  string
	Owned []string
}

// Status is a sandbox as seen from the engine and the state directory.
type Status struct {
	Name      string `json:"name"`
	Agent     string `json:"agent"`
	Workspace string `json:"workspace"`
	State     string `json:"state"` // agent container state, or "missing"
	Proxy     string `json:"proxy"` // proxy container state, or "missing"
	Image     string `json:"image,omitempty"`
	StateDir  bool   `json:"state_dir"`
	// ProjectChanged lists project files edited since the last snapshot.
	ProjectChanged []string `json:"project_changed,omitempty"`
	// GitIntegrity compares the workspace's .git with its fingerprint.
	GitIntegrity *GitIntegrity `json:"git_integrity,omitempty"`
}

// Imported is what Create read from the project files, as policy reload
// renders it (allow example.com, notes.txt).
type Imported struct {
	Rules  []string `json:"rules"`
	Ignore []string `json:"ignore"`
}

// Create builds images if needed and creates the sandbox's objects.
func (m *Manager) Create(ctx context.Context, name, agentName, ws string) (meta Meta, imp Imported, err error) {
	if err := ValidateName(name); err != nil {
		return meta, imp, err
	}
	ag, err := agents.Get(agentName)
	if err != nil {
		return meta, imp, err
	}
	if ws, err = filepath.Abs(ws); err != nil {
		return meta, imp, err
	}
	if fi, err := os.Stat(ws); err != nil || !fi.IsDir() {
		return meta, imp, fmt.Errorf("workspace %s is not a directory", ws)
	}
	if err := workspace.CheckRoot(ws, m.Home, m.Owned); err != nil {
		return meta, imp, err
	}
	if _, err := m.Store.Load(name); err == nil {
		return meta, imp, fmt.Errorf("%w: %s", ErrExists, name)
	}
	if cs, err := m.RT.ContainerList(ctx, LabelSandbox+"="+name); err != nil {
		return meta, imp, err
	} else if len(cs) > 0 {
		return meta, imp, fmt.Errorf("%w: %s (podman objects left over; run podbox rm %s)", ErrExists, name, name)
	}
	if ag.Experimental {
		m.Warn("agent %s is experimental: its image and flags are unverified", ag.Name)
	}
	if _, err := m.Images.Ensure(ctx, images.Proxy, false); err != nil {
		return meta, imp, err
	}
	if _, err := m.Images.Ensure(ctx, ag.Image, false); err != nil {
		return meta, imp, err
	}

	meta = Meta{Name: name, Agent: ag.Name, Workspace: ws, Created: time.Now().UTC()}
	defer func() {
		if err != nil {
			_ = m.Remove(context.WithoutCancel(ctx), name, false)
		}
	}()
	if err = m.Store.Save(meta); err != nil {
		return meta, imp, err
	}
	c, err := m.Store.Snapshot(meta)
	if err != nil {
		return meta, imp, err
	}
	for _, w := range c.Warnings {
		m.Warn("%s", w)
	}
	imp = Imported{Rules: c.RulesAdded, Ignore: c.IgnoreAdded}
	if err = m.Store.RecordGit(meta); err != nil {
		return meta, imp, err
	}
	if err = m.refreshPolicy(meta); err != nil {
		return meta, imp, err
	}
	if _, err = ensureNetwork(ctx, m.RT, runtime.NetworkSpec{Name: EgressNetwork, Labels: map[string]string{LabelShared: "egress"}}); err != nil {
		return meta, imp, err
	}
	if _, err = ensureNetwork(ctx, m.RT, runtime.NetworkSpec{
		Name: internalNetwork(name), Internal: true, DisableDNS: true, Labels: labels(meta, "network"),
	}); err != nil {
		return meta, imp, err
	}
	if ok, verr := m.RT.VolumeExists(ctx, HomeVolume(ag.Name)); verr != nil {
		return meta, imp, verr
	} else if !ok {
		if err = m.RT.VolumeCreate(ctx, HomeVolume(ag.Name), map[string]string{LabelHome: ag.Name}); err != nil {
			return meta, imp, err
		}
	}
	if err = m.createProxy(ctx, meta); err != nil {
		return meta, imp, err
	}
	err = m.createAgent(ctx, meta)
	return meta, imp, err
}

func (m *Manager) refreshPolicy(meta Meta) error {
	e, err := m.Store.RefreshPolicy(meta)
	for _, w := range e.Warnings {
		m.Warn("%s", w)
	}
	return err
}

func (m *Manager) proxyAddress(ctx context.Context, name string) (string, error) {
	n, err := m.RT.NetworkInspect(ctx, internalNetwork(name))
	if err != nil {
		return "", fmt.Errorf("internal network of %s: %w", name, err)
	}
	return proxyIP(n)
}

func (m *Manager) createProxy(ctx context.Context, meta Meta) error {
	ref, err := m.Images.Ensure(ctx, images.Proxy, false)
	if err != nil {
		return err
	}
	ip, err := m.proxyAddress(ctx, meta.Name)
	if err != nil {
		return err
	}
	return m.RT.ContainerCreate(ctx, proxySpec(meta, ref, m.Store.Dir(meta.Name), ip, Upstream(m.Config.UpstreamProxy, m.Getenv), m.Info.SELinux))
}

// createAgent resolves masks now, so files added since the last start are
// covered, using the .sandboxignore snapshot and the mask record.
func (m *Manager) createAgent(ctx context.Context, meta Meta) error {
	ag, err := agents.Get(meta.Agent)
	if err != nil {
		return err
	}
	ref, err := m.Images.Ensure(ctx, ag.Image, false)
	if err != nil {
		return err
	}
	ip, err := m.proxyAddress(ctx, meta.Name)
	if err != nil {
		return err
	}
	res, err := m.Store.ResolveMasks(meta)
	if err != nil {
		return err
	}
	if w := MaskWarning(meta, res); w != "" {
		m.Warn("%s", w)
	}
	for _, w := range res.Warnings {
		m.Warn("%s", w)
	}
	if gitPointer(meta.Workspace) {
		m.Warn(".git is a file (worktree or submodule): the hooks and config of the git directory it points to are not protected from the agent")
	}
	var limits *runtime.Limits
	if runtime.LimitsDelegated(m.Info) {
		l := m.Config.Limits
		limits = &runtime.Limits{CPUs: l.CPUs, Memory: l.Memory, PIDs: l.PIDs}
	} else {
		m.Warn("resource limits unavailable (cgroup controllers not delegated): %s runs without CPU/RAM/PID limits; see podbox doctor", meta.Name)
	}
	if err := os.MkdirAll(m.Store.EmptyDir(meta.Name), 0o755); err != nil {
		return err
	}
	return m.RT.ContainerCreate(ctx, agentSpec(agentInput{
		Meta: meta, Image: ref, ProxyIP: ip, ReadOnly: readOnlyProject(meta.Workspace), GitDir: gitDir(meta.Workspace), Masks: res.Masks, EmptyDir: m.Store.EmptyDir(meta.Name),
		Limits: limits, SELinux: m.Info.SELinux, GOOS: m.GOOS,
	}))
}

// Start (re)creates stopped containers with current images, masks and
// policy (from the project snapshot), then starts proxy and agent.
func (m *Manager) Start(ctx context.Context, name string) error {
	meta, err := m.Store.Load(name)
	if err != nil {
		return err
	}
	if err := workspace.CheckRoot(meta.Workspace, m.Home, m.Owned); err != nil {
		return err
	}
	if !m.Store.HasGitPrint(name) { // sandboxes created before fingerprints existed
		if err := m.Store.RecordGit(meta); err != nil {
			return err
		}
	}
	if err := m.refreshPolicy(meta); err != nil {
		return err
	}
	if _, err := m.RT.NetworkInspect(ctx, internalNetwork(name)); err != nil {
		return fmt.Errorf("sandbox %s is damaged (%v): run podbox rm %s", name, err, name)
	}
	if _, err := ensureNetwork(ctx, m.RT, runtime.NetworkSpec{Name: EgressNetwork, Labels: map[string]string{LabelShared: "egress"}}); err != nil {
		return err
	}
	for _, c := range []struct {
		name   string
		create func(context.Context, Meta) error
	}{{proxyContainer(name), m.createProxy}, {agentContainer(name), m.createAgent}} {
		st, err := m.RT.ContainerInspect(ctx, c.name)
		switch {
		case err == nil && st.Running():
			continue
		case err == nil && st.State == "created":
			if err := m.RT.ContainerStart(ctx, c.name); err != nil {
				return err
			}
			continue
		case err == nil:
			if err := m.RT.ContainerRemove(ctx, c.name); err != nil {
				return err
			}
		case !errors.Is(err, runtime.ErrNotFound):
			return err
		}
		if err := c.create(ctx, meta); err != nil {
			return err
		}
		if err := m.RT.ContainerStart(ctx, c.name); err != nil {
			return err
		}
	}
	return nil
}

// Stop stops the agent, then its proxy.
func (m *Manager) Stop(ctx context.Context, name string) error {
	if _, err := m.status(ctx, name); err != nil {
		return err
	}
	for _, c := range []string{agentContainer(name), proxyContainer(name)} {
		if err := m.RT.ContainerStop(ctx, c); err != nil && !errors.Is(err, runtime.ErrNotFound) {
			return err
		}
	}
	return nil
}

// Remove deletes every object of a sandbox; the agent home volume only
// with volumes set.
func (m *Manager) Remove(ctx context.Context, name string, volumes bool) error {
	st, err := m.status(ctx, name)
	if err != nil {
		return err
	}
	cs, err := m.RT.ContainerList(ctx, LabelSandbox+"="+name)
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range cs {
		if err := m.RT.ContainerRemove(ctx, c.Name); err != nil && !errors.Is(err, runtime.ErrNotFound) {
			errs = append(errs, err)
		}
	}
	nets, err := m.RT.NetworkList(ctx, LabelSandbox+"="+name)
	errs = append(errs, err)
	for _, n := range nets {
		if err := m.RT.NetworkRemove(ctx, n); err != nil && !errors.Is(err, runtime.ErrNotFound) {
			errs = append(errs, err)
		}
	}
	errs = append(errs, m.Store.Remove(name))
	if volumes && st.Agent != "" {
		if err := m.RT.VolumeRemove(ctx, HomeVolume(st.Agent)); err != nil && !errors.Is(err, runtime.ErrNotFound) {
			errs = append(errs, err)
		}
	}
	if left, err := m.RT.ContainerList(ctx, LabelSandbox); err == nil && len(left) == 0 {
		if err := m.RT.NetworkRemove(ctx, EgressNetwork); err != nil && !errors.Is(err, runtime.ErrNotFound) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// List reports every sandbox known to the engine or the state directory.
func (m *Manager) List(ctx context.Context) ([]Status, error) {
	cs, err := m.RT.ContainerList(ctx, LabelSandbox)
	if err != nil {
		return nil, err
	}
	byName := map[string]*Status{}
	get := func(n string) *Status {
		if s, ok := byName[n]; ok {
			return s
		}
		s := &Status{Name: n, State: "missing", Proxy: "missing"}
		byName[n] = s
		return s
	}
	for _, c := range cs {
		s := get(c.Labels[LabelSandbox])
		s.Agent, s.Workspace = c.Labels[LabelAgent], c.Labels[LabelWorkspace]
		switch c.Labels[LabelRole] {
		case "agent":
			s.State, s.Image = c.State, c.Image
		case "proxy":
			s.Proxy = c.State
		}
	}
	entries, _ := os.ReadDir(m.Store.Root)
	for _, e := range entries {
		if meta, err := m.Store.Load(e.Name()); err == nil {
			s := get(meta.Name)
			s.StateDir = true
			s.ProjectChanged = m.Store.Drift(meta)
			if g, err := m.Store.CheckGit(meta); err != nil {
				m.Warn("%s: git integrity check failed: %v", meta.Name, err)
			} else {
				s.GitIntegrity = g
			}
			if s.Agent == "" {
				s.Agent, s.Workspace = meta.Agent, meta.Workspace
			}
		}
	}
	out := make([]Status, 0, len(byName))
	for _, s := range byName {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Manager) status(ctx context.Context, name string) (Status, error) {
	all, err := m.List(ctx)
	if err != nil {
		return Status{}, err
	}
	for _, s := range all {
		if s.Name == name {
			return s, nil
		}
	}
	return Status{}, ErrNotFound
}

// IO is the terminal wiring of an interactive command.
type IO struct {
	In       io.Reader
	Out, Err io.Writer
	TTY      bool
}

// Exec runs cmd in the agent container at the workspace path.
func (m *Manager) Exec(ctx context.Context, name string, cmd []string, tio IO) (int, error) {
	meta, err := m.Store.Load(name)
	if err != nil {
		return -1, err
	}
	c, err := m.RT.ContainerInspect(ctx, agentContainer(name))
	if errors.Is(err, runtime.ErrNotFound) || err == nil && !c.Running() {
		return -1, fmt.Errorf("%w: %s (run podbox start %s)", ErrNotRunning, name, name)
	}
	if err != nil {
		return -1, err
	}
	return m.RT.Exec(ctx, agentContainer(name), runtime.ExecSpec{
		Command: cmd, Workdir: WorkspaceTarget(m.GOOS, meta.Workspace),
		Interactive: tio.In != nil, TTY: tio.TTY, Stdin: tio.In, Stdout: tio.Out, Stderr: tio.Err,
	})
}

// Attach opens an agent session in a running sandbox.
func (m *Manager) Attach(ctx context.Context, name string, tio IO) (int, error) {
	meta, err := m.Store.Load(name)
	if err != nil {
		return -1, err
	}
	ag, err := agents.Get(meta.Agent)
	if err != nil {
		return -1, err
	}
	return m.Exec(ctx, name, ag.Command, tio)
}
