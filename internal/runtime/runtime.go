// Package runtime abstracts the container engine behind typed specs.
package runtime

import (
	"context"
	"errors"
	"io"
)

// ErrNotFound reports a missing container, network, volume or image.
var ErrNotFound = errors.New("not found")

// Runtime is every engine operation podbox needs.
type Runtime interface {
	Info(ctx context.Context) (Info, error)

	NetworkCreate(ctx context.Context, spec NetworkSpec) error
	NetworkInspect(ctx context.Context, name string) (Network, error)
	NetworkRemove(ctx context.Context, name string) error
	NetworkList(ctx context.Context, label string) ([]string, error)

	ImageExists(ctx context.Context, ref string) (bool, error)
	ImageBuild(ctx context.Context, spec BuildSpec) error
	ImageList(ctx context.Context, label string) ([]Image, error)
	ImageRemove(ctx context.Context, ref string) error

	VolumeCreate(ctx context.Context, name string, labels map[string]string) error
	VolumeExists(ctx context.Context, name string) (bool, error)
	VolumeRemove(ctx context.Context, name string) error
	VolumeList(ctx context.Context, label string) ([]string, error)

	ContainerCreate(ctx context.Context, spec ContainerSpec) error
	ContainerStart(ctx context.Context, name string) error
	ContainerStop(ctx context.Context, name string) error
	ContainerRemove(ctx context.Context, name string) error
	ContainerInspect(ctx context.Context, name string) (Container, error)
	ContainerList(ctx context.Context, label string) ([]Container, error)
	// Exec runs a command in a running container and returns its exit code.
	Exec(ctx context.Context, name string, spec ExecSpec) (int, error)
}

// Info describes the engine and its host.
type Info struct {
	Version           string
	OS                string
	Arch              string
	Rootless          bool
	SELinux           bool
	NetworkBackend    string
	CgroupVersion     string
	CgroupManager     string
	CgroupControllers []string
	StorageDriver     string
	// Remote is true when podman talks to a VM (podman machine).
	Remote bool
}

// NetworkSpec describes a bridge network.
type NetworkSpec struct {
	Name       string
	Internal   bool
	DisableDNS bool
	Labels     map[string]string
}

// Network is an existing network.
type Network struct {
	Name     string
	Internal bool
	Subnets  []string
	Labels   map[string]string
}

// BuildSpec describes an image build from a local context directory.
type BuildSpec struct {
	Tag        string
	Context    string
	File       string
	BuildArgs  map[string]string
	Labels     map[string]string
	Stdout     io.Writer
	Stderr     io.Writer
	NoProxyEnv bool // do not forward host proxy variables into the build
}

// Image is a local image.
type Image struct {
	ID     string            `json:"id"`
	Names  []string          `json:"names"`
	Labels map[string]string `json:"labels"`
}

// MountType selects the kind of mount.
type MountType string

const (
	Bind   MountType = "bind"
	Volume MountType = "volume"
	Tmpfs  MountType = "tmpfs"
)

// Mount attaches storage to a container.
type Mount struct {
	Type     MountType
	Source   string // host path or volume name; unused for tmpfs
	Target   string
	ReadOnly bool
	Relabel  bool   // SELinux shared relabel (:z)
	Options  string // extra tmpfs options
}

// NetworkAttachment joins a container to a network, optionally at a fixed IP.
type NetworkAttachment struct {
	Name string
	IP   string
}

// Limits caps container resources.
type Limits struct {
	CPUs   string
	Memory string
	PIDs   int
}

// ContainerSpec describes a container; zero values mean engine defaults.
type ContainerSpec struct {
	Name            string
	Image           string
	Command         []string
	Hostname        string
	User            string
	Userns          string
	Workdir         string
	Env             map[string]string
	Labels          map[string]string
	Networks        []NetworkAttachment
	DNS             []string
	Hosts           map[string]string
	Mounts          []Mount
	CapDrop         []string
	NoNewPrivileges bool
	ReadOnlyRoot    bool
	InheritProxyEnv bool
	Limits          *Limits
}

// Container is the observed state of a container.
type Container struct {
	ID       string
	Name     string
	Image    string
	State    string // created, running, exited, ...
	Labels   map[string]string
	Networks map[string]string // network name -> IP
}

// Running reports whether the container is up.
func (c Container) Running() bool { return c.State == "running" }

// ExecSpec describes a command run inside a container.
type ExecSpec struct {
	Command     []string
	Workdir     string
	User        string
	Env         map[string]string
	Interactive bool
	TTY         bool
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
}
