// Package config resolves doppio's directories, settings and auth token.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// DefaultAddr is the loopback address the daemon listens on.
const DefaultAddr = "127.0.0.1:7341"

// Paths are the directories and files doppio uses.
type Paths struct {
	ConfigDir string // token, logs, state
	CacheDir  string // stage cache, input blobs, packs
}

// TokenFile holds the bearer token clients must present.
func (p Paths) TokenFile() string { return filepath.Join(p.ConfigDir, "token") }

// LogFile receives daemon and worker logs when started in the background.
func (p Paths) LogFile() string { return filepath.Join(p.ConfigDir, "logs", "doppio.log") }

// StageCacheDir is the content-addressed stage cache root.
func (p Paths) StageCacheDir() string { return filepath.Join(p.CacheDir, "cache") }

// PacksDir holds assembled Spatial Context Packs.
func (p Paths) PacksDir() string { return filepath.Join(p.CacheDir, "packs") }

// DefaultPaths honours DOPPIO_HOME (both dirs beneath it), otherwise the
// platform user config/cache dirs (macOS: ~/Library/Application Support and ~/Library/Caches).
func DefaultPaths() (Paths, error) {
	if home := os.Getenv("DOPPIO_HOME"); home != "" {
		return Paths{ConfigDir: filepath.Join(home, "config"), CacheDir: filepath.Join(home, "cache")}, nil
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, fmt.Errorf("failed to locate config directory: %w", err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return Paths{}, fmt.Errorf("failed to locate cache directory: %w", err)
	}
	return Paths{ConfigDir: filepath.Join(cfg, "doppio"), CacheDir: filepath.Join(cache, "doppio")}, nil
}

// Ensure creates the directories with owner-only permissions.
func (p Paths) Ensure() error {
	for _, dir := range []string{p.ConfigDir, filepath.Dir(p.LogFile()), p.CacheDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("failed to create %s: %w", dir, err)
		}
	}
	return nil
}

// LoadOrCreateToken returns the token at path, creating a random one if absent.
// It refuses tokens readable by other users.
func LoadOrCreateToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return "", fmt.Errorf("failed to stat token: %w", statErr)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("token file %s is accessible by other users; run chmod 600 on it", path)
		}
		token := strings.TrimSpace(string(data))
		if len(token) < 32 {
			return "", fmt.Errorf("token file %s is malformed", path)
		}
		return token, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("failed to read token: %w", err)
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("failed to generate token: %w", err)
	}
	token := hex.EncodeToString(b[:])
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("failed to create token directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return LoadOrCreateToken(path) // lost a race with another process
	}
	if err != nil {
		return "", fmt.Errorf("failed to create token: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(token + "\n"); err != nil {
		return "", fmt.Errorf("failed to write token: %w", err)
	}
	return token, nil
}

// ValidateLoopbackAddr rejects listen addresses that are not loopback.
func ValidateLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid address %q: %w", addr, err)
	}
	if port == "" {
		return fmt.Errorf("address %q needs a port", addr)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("refusing to listen on non-loopback address %q", addr)
	}
	return nil
}

// AllowedHosts returns Host header values accepted for addr, which blocks
// DNS-rebinding attacks from web pages.
func AllowedHosts(addr string) map[string]bool {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return map[string]bool{}
	}
	return map[string]bool{
		"127.0.0.1:" + port: true,
		"localhost:" + port: true,
		"[::1]:" + port:     true,
	}
}

// ResolveWorkerDir finds the Python worker project: explicit value, then
// DOPPIO_WORKER_DIR, then next to the executable, then the working directory
// and its parents (development checkouts).
func ResolveWorkerDir(explicit string) (string, error) {
	candidates := []string{}
	if explicit != "" {
		candidates = append(candidates, explicit)
	}
	if env := os.Getenv("DOPPIO_WORKER_DIR"); env != "" {
		candidates = append(candidates, env)
	}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "..", "worker"),
			filepath.Join(dir, "..", "share", "doppio", "worker"))
	}
	if wd, err := os.Getwd(); err == nil {
		for dir := wd; ; dir = filepath.Dir(dir) {
			candidates = append(candidates, filepath.Join(dir, "worker"))
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "pyproject.toml")); err == nil {
			abs, err := filepath.Abs(c)
			if err != nil {
				return "", fmt.Errorf("failed to resolve worker dir: %w", err)
			}
			return abs, nil
		}
	}
	return "", errors.New("worker project not found; pass --worker-dir or set DOPPIO_WORKER_DIR")
}
