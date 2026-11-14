// Package config resolves podbox directories and reads the global config.
package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// Paths are the directories podbox owns.
type Paths struct {
	Config string
	State  string
	Cache  string
}

// Resolve returns the directories for the current user.
func Resolve() (Paths, error) {
	return resolve(runtime.GOOS, os.Getenv, os.UserHomeDir, os.UserConfigDir, os.UserCacheDir)
}

func resolve(goos string, env func(string) string, home, cfgDir, cacheDir func() (string, error)) (Paths, error) {
	if h := env("PODBOX_HOME"); h != "" {
		return Paths{Config: filepath.Join(h, "config"), State: filepath.Join(h, "state"), Cache: filepath.Join(h, "cache")}, nil
	}
	var p Paths
	if goos == "linux" {
		h, err := home()
		if err != nil {
			return p, err
		}
		p.Config = xdg(env("XDG_CONFIG_HOME"), h, ".config")
		p.State = xdg(env("XDG_STATE_HOME"), h, ".local", "state")
		p.Cache = xdg(env("XDG_CACHE_HOME"), h, ".cache")
	} else {
		c, err := cfgDir()
		if err != nil {
			return p, err
		}
		k, err := cacheDir()
		if err != nil {
			return p, err
		}
		p.Config = filepath.Join(c, "podbox")
		p.State = filepath.Join(c, "podbox", "state")
		p.Cache = filepath.Join(k, "podbox")
		return p, nil
	}
	p.Config = filepath.Join(p.Config, "podbox")
	p.State = filepath.Join(p.State, "podbox")
	p.Cache = filepath.Join(p.Cache, "podbox")
	return p, nil
}

func xdg(v, home string, fallback ...string) string {
	if filepath.IsAbs(v) {
		return v
	}
	return filepath.Join(append([]string{home}, fallback...)...)
}
