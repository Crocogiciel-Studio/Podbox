package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Podman drives the podman CLI.
type Podman struct {
	Bin string
	run runner
}

// NewPodman locates podman via PODBOX_PODMAN, then PATH.
func NewPodman() (*Podman, error) {
	bin, err := LocatePodman()
	if err != nil {
		return nil, err
	}
	return &Podman{Bin: bin, run: execRunner{}}, nil
}

func (p *Podman) out(ctx context.Context, args ...string) ([]byte, error) {
	return p.run.output(ctx, p.Bin, args)
}

func (p *Podman) Info(ctx context.Context) (Info, error) {
	b, err := p.out(ctx, "info", "--format", "json")
	if err != nil {
		return Info{}, err
	}
	var raw struct {
		Host struct {
			Arch              string   `json:"arch"`
			OS                string   `json:"os"`
			CgroupManager     string   `json:"cgroupManager"`
			CgroupVersion     string   `json:"cgroupVersion"`
			CgroupControllers []string `json:"cgroupControllers"`
			NetworkBackend    string   `json:"networkBackend"`
			ServiceIsRemote   bool     `json:"serviceIsRemote"`
			Security          struct {
				Rootless bool `json:"rootless"`
				SELinux  bool `json:"selinuxEnabled"`
			} `json:"security"`
		} `json:"host"`
		Store struct {
			GraphDriverName string `json:"graphDriverName"`
		} `json:"store"`
		Version struct {
			Version string `json:"Version"`
		} `json:"version"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return Info{}, fmt.Errorf("parse podman info: %w", err)
	}
	h := raw.Host
	return Info{
		Version: raw.Version.Version, OS: h.OS, Arch: h.Arch,
		Rootless: h.Security.Rootless, SELinux: h.Security.SELinux,
		NetworkBackend: h.NetworkBackend, CgroupVersion: h.CgroupVersion,
		CgroupManager: h.CgroupManager, CgroupControllers: h.CgroupControllers,
		StorageDriver: raw.Store.GraphDriverName, Remote: h.ServiceIsRemote,
	}, nil
}

func (p *Podman) NetworkCreate(ctx context.Context, s NetworkSpec) error {
	args := []string{"network", "create"}
	if s.Internal {
		args = append(args, "--internal")
	}
	if s.DisableDNS {
		args = append(args, "--disable-dns")
	}
	args = append(args, labelFlags(s.Labels)...)
	_, err := p.out(ctx, append(args, s.Name)...)
	return err
}

func (p *Podman) NetworkInspect(ctx context.Context, name string) (Network, error) {
	b, err := p.out(ctx, "network", "inspect", name)
	if err != nil {
		return Network{}, err
	}
	var raw []struct {
		Name     string            `json:"name"`
		Internal bool              `json:"internal"`
		Labels   map[string]string `json:"labels"`
		Subnets  []struct {
			Subnet string `json:"subnet"`
		} `json:"subnets"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || len(raw) == 0 {
		return Network{}, fmt.Errorf("parse network inspect: %v", err)
	}
	n := Network{Name: raw[0].Name, Internal: raw[0].Internal, Labels: raw[0].Labels}
	for _, s := range raw[0].Subnets {
		n.Subnets = append(n.Subnets, s.Subnet)
	}
	return n, nil
}

func (p *Podman) NetworkRemove(ctx context.Context, name string) error {
	_, err := p.out(ctx, "network", "rm", name)
	return err
}

func (p *Podman) NetworkList(ctx context.Context, label string) ([]string, error) {
	return p.names(ctx, "network", "ls", "--filter", "label="+label, "--format", "{{.Name}}")
}

func (p *Podman) ImageExists(ctx context.Context, ref string) (bool, error) {
	return p.exists(ctx, "image", "exists", ref)
}

func (p *Podman) ImagePull(ctx context.Context, ref string) error {
	_, err := p.out(ctx, "pull", "--quiet", ref)
	return err
}

func (p *Podman) ImageBuild(ctx context.Context, s BuildSpec) error {
	args := []string{"build", "-t", s.Tag, "-f", s.File}
	if s.NoProxyEnv {
		args = append(args, "--http-proxy=false")
	}
	if s.Network != "" {
		args = append(args, "--network="+s.Network)
	}
	for _, k := range sortedKeys(s.BuildArgs) {
		args = append(args, "--build-arg", k+"="+s.BuildArgs[k])
	}
	args = append(args, labelFlags(s.Labels)...)
	args = append(args, s.Context)
	return p.run.stream(ctx, p.Bin, args, streams{Stdout: s.Stdout, Stderr: s.Stderr})
}

func (p *Podman) ImageList(ctx context.Context, label string) ([]Image, error) {
	b, err := p.out(ctx, "images", "--filter", "label="+label, "--format", "json")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID     string            `json:"Id"`
		Names  []string          `json:"Names"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse images: %w", err)
	}
	imgs := make([]Image, 0, len(raw))
	for _, r := range raw {
		imgs = append(imgs, Image{ID: r.ID, Names: r.Names, Labels: r.Labels})
	}
	return imgs, nil
}

func (p *Podman) ImageRemove(ctx context.Context, ref string) error {
	_, err := p.out(ctx, "rmi", ref)
	return err
}

func (p *Podman) VolumeCreate(ctx context.Context, name string, labels map[string]string) error {
	args := append([]string{"volume", "create"}, labelFlags(labels)...)
	_, err := p.out(ctx, append(args, name)...)
	return err
}

func (p *Podman) VolumeExists(ctx context.Context, name string) (bool, error) {
	return p.exists(ctx, "volume", "exists", name)
}

func (p *Podman) VolumeRemove(ctx context.Context, name string) error {
	_, err := p.out(ctx, "volume", "rm", name)
	return err
}

func (p *Podman) VolumeList(ctx context.Context, label string) ([]string, error) {
	return p.names(ctx, "volume", "ls", "--filter", "label="+label, "--format", "{{.Name}}")
}

// CreateArgs builds the `podman create` argv for a spec, in a fixed order.
func CreateArgs(s ContainerSpec) []string {
	args := []string{"create", "--name", s.Name}
	if s.Hostname != "" {
		args = append(args, "--hostname", s.Hostname)
	}
	if s.Userns != "" {
		args = append(args, "--userns="+s.Userns)
	}
	if s.User != "" {
		args = append(args, "--user", s.User)
	}
	for _, c := range s.CapDrop {
		args = append(args, "--cap-drop="+c)
	}
	if s.NoNewPrivileges {
		args = append(args, "--security-opt", "no-new-privileges")
	}
	if s.ReadOnlyRoot {
		args = append(args, "--read-only")
	}
	if !s.InheritProxyEnv {
		args = append(args, "--http-proxy=false")
	}
	if l := s.Limits; l != nil {
		if l.CPUs != "" {
			args = append(args, "--cpus", l.CPUs)
		}
		if l.Memory != "" {
			args = append(args, "--memory", l.Memory)
		}
		if l.PIDs > 0 {
			args = append(args, "--pids-limit", strconv.Itoa(l.PIDs))
		}
	}
	for _, n := range s.Networks {
		v := n.Name
		if n.IP != "" {
			v += ":ip=" + n.IP
		}
		args = append(args, "--network", v)
	}
	for _, d := range s.DNS {
		args = append(args, "--dns", d)
	}
	for _, h := range sortedKeys(s.Hosts) {
		args = append(args, "--add-host", h+":"+s.Hosts[h])
	}
	for _, m := range s.Mounts {
		args = append(args, mountFlag(m)...)
	}
	if s.Workdir != "" {
		args = append(args, "--workdir", s.Workdir)
	}
	for _, k := range sortedKeys(s.Env) {
		args = append(args, "--env", k+"="+s.Env[k])
	}
	args = append(args, labelFlags(s.Labels)...)
	args = append(args, s.Image)
	return append(args, s.Command...)
}

func mountFlag(m Mount) []string {
	switch m.Type {
	case Tmpfs:
		v := m.Target
		if m.Options != "" {
			v += ":" + m.Options
		}
		return []string{"--tmpfs", v}
	default:
		var opts []string
		if m.ReadOnly {
			opts = append(opts, "ro")
		}
		if m.Relabel {
			opts = append(opts, "z")
		}
		v := m.Source + ":" + m.Target
		if len(opts) > 0 {
			v += ":" + strings.Join(opts, ",")
		}
		return []string{"--volume", v}
	}
}

func (p *Podman) ContainerCreate(ctx context.Context, s ContainerSpec) error {
	_, err := p.out(ctx, CreateArgs(s)...)
	return err
}

func (p *Podman) ContainerStart(ctx context.Context, name string) error {
	_, err := p.out(ctx, "start", name)
	return err
}

func (p *Podman) ContainerStop(ctx context.Context, name string) error {
	_, err := p.out(ctx, "stop", "--time", "5", name)
	return err
}

func (p *Podman) ContainerRemove(ctx context.Context, name string) error {
	_, err := p.out(ctx, "rm", "--force", "--time", "0", name)
	return err
}

func (p *Podman) ContainerInspect(ctx context.Context, name string) (Container, error) {
	b, err := p.out(ctx, "container", "inspect", name)
	if err != nil {
		return Container{}, err
	}
	var raw []struct {
		ID     string `json:"Id"`
		Name   string `json:"Name"`
		Image  string `json:"ImageName"`
		State  struct{ Status string }
		Config struct {
			Labels map[string]string
		}
		NetworkSettings struct {
			Networks map[string]struct{ IPAddress string }
		}
	}
	if err := json.Unmarshal(b, &raw); err != nil || len(raw) == 0 {
		return Container{}, fmt.Errorf("parse container inspect: %v", err)
	}
	r := raw[0]
	c := Container{ID: r.ID, Name: r.Name, Image: r.Image, State: r.State.Status, Labels: r.Config.Labels, Networks: map[string]string{}}
	for n, v := range r.NetworkSettings.Networks {
		c.Networks[n] = v.IPAddress
	}
	return c, nil
}

func (p *Podman) ContainerList(ctx context.Context, label string) ([]Container, error) {
	b, err := p.out(ctx, "ps", "--all", "--filter", "label="+label, "--format", "json")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID       string            `json:"Id"`
		Names    []string          `json:"Names"`
		Image    string            `json:"Image"`
		State    string            `json:"State"`
		Labels   map[string]string `json:"Labels"`
		Networks []string          `json:"Networks"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse ps: %w", err)
	}
	cs := make([]Container, 0, len(raw))
	for _, r := range raw {
		c := Container{ID: r.ID, Image: r.Image, State: r.State, Labels: r.Labels, Networks: map[string]string{}}
		if len(r.Names) > 0 {
			c.Name = r.Names[0]
		}
		for _, n := range r.Networks {
			c.Networks[n] = ""
		}
		cs = append(cs, c)
	}
	return cs, nil
}

// ExecArgs builds the `podman exec` argv.
func ExecArgs(name string, s ExecSpec) []string {
	args := []string{"exec"}
	if s.Interactive {
		args = append(args, "--interactive")
	}
	if s.TTY {
		args = append(args, "--tty")
	}
	if s.User != "" {
		args = append(args, "--user", s.User)
	}
	if s.Workdir != "" {
		args = append(args, "--workdir", s.Workdir)
	}
	for _, k := range sortedKeys(s.Env) {
		args = append(args, "--env", k+"="+s.Env[k])
	}
	args = append(args, name)
	return append(args, s.Command...)
}

func (p *Podman) Exec(ctx context.Context, name string, s ExecSpec) (int, error) {
	return p.run.interactive(ctx, p.Bin, ExecArgs(name, s), streams{Stdin: s.Stdin, Stdout: s.Stdout, Stderr: s.Stderr})
}

// logsArgs builds the `podman logs` argv.
func logsArgs(name string, s LogsSpec) []string {
	args := []string{"logs"}
	if s.Follow {
		args = append(args, "--follow")
	}
	if s.Since != "" {
		args = append(args, "--since", s.Since)
	}
	return append(args, name)
}

func (p *Podman) Logs(ctx context.Context, name string, s LogsSpec) error {
	err := p.run.stream(ctx, p.Bin, logsArgs(name, s), streams{Stdout: s.Stdout})
	if s.Follow && ctx.Err() != nil {
		return nil // interrupted on purpose
	}
	return err
}

func (p *Podman) exists(ctx context.Context, args ...string) (bool, error) {
	code, err := p.run.interactive(ctx, p.Bin, args, streams{})
	if err != nil {
		return false, err
	}
	switch code {
	case 0:
		return true, nil
	case 1:
		return false, nil
	}
	return false, fmt.Errorf("podman %s: exit %d", strings.Join(args, " "), code)
}

func (p *Podman) names(ctx context.Context, args ...string) ([]string, error) {
	b, err := p.out(ctx, args...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(b)), nil
}

func labelFlags(labels map[string]string) []string {
	var args []string
	for _, k := range sortedKeys(labels) {
		args = append(args, "--label", k+"="+labels[k])
	}
	return args
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
