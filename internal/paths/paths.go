// Package paths resolves listen's per-user XDG storage directories.
package paths

import (
	"errors"
	"os"
	"path/filepath"
)

// Paths contains listen-specific cache, data, and configuration directories.
type Paths struct {
	CacheDir  string
	DataDir   string
	ConfigDir string
}

// ForHome resolves paths against home and env. Relative XDG values are ignored
// as required by the XDG Base Directory specification.
func ForHome(home string, env func(string) string) (Paths, error) {
	if home == "" {
		return Paths{}, errors.New("home directory is unavailable")
	}
	if !filepath.IsAbs(home) {
		return Paths{}, errors.New("home directory must be absolute")
	}
	if env == nil {
		env = func(string) string { return "" }
	}

	cacheHome := xdgHome(env("XDG_CACHE_HOME"), filepath.Join(home, ".cache"))
	dataHome := xdgHome(env("XDG_DATA_HOME"), filepath.Join(home, ".local", "share"))
	configHome := xdgHome(env("XDG_CONFIG_HOME"), filepath.Join(home, ".config"))
	return Paths{
		CacheDir:  filepath.Join(cacheHome, "listen"),
		DataDir:   filepath.Join(dataHome, "listen"),
		ConfigDir: filepath.Join(configHome, "listen"),
	}, nil
}

// FromEnv resolves paths using the current user's home and process environment.
func FromEnv() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	return ForHome(home, os.Getenv)
}

func xdgHome(value, fallback string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(fallback)
}
