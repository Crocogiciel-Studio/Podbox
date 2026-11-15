package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is <config>/config.yaml.
type Config struct {
	Images struct {
		// Base is the image every agent image builds on (Debian-based).
		Base string `yaml:"base"`
	} `yaml:"images"`
	// CAFile is a PEM bundle trusted inside agent images (corporate TLS).
	CAFile string `yaml:"ca_file"`
	// UpstreamProxy is the parent proxy; empty uses the host HTTPS_PROXY,
	// "none" disables chaining.
	UpstreamProxy string `yaml:"upstream_proxy"`
	Limits        Limits `yaml:"limits"`
}

// Limits are the agent container resource caps.
type Limits struct {
	CPUs   string `yaml:"cpus"`
	Memory string `yaml:"memory"`
	PIDs   int    `yaml:"pids"`
}

// DefaultBase is the default base image.
const DefaultBase = "docker.io/library/node:22-bookworm-slim"

// Load reads config.yaml from dir; a missing file yields defaults.
func Load(dir string) (Config, error) {
	c := Config{Limits: Limits{CPUs: "4", Memory: "8g", PIDs: 2048}}
	c.Images.Base = DefaultBase
	path := filepath.Join(dir, "config.yaml")
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.CAFile != "" && !filepath.IsAbs(c.CAFile) {
		c.CAFile = filepath.Join(dir, c.CAFile)
	}
	return c, nil
}
