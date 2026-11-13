package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultPathsHonoursDoppioHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DOPPIO_HOME", home)
	p, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if p.ConfigDir != filepath.Join(home, "config") || p.CacheDir != filepath.Join(home, "cache") {
		t.Fatalf("paths = %+v", p)
	}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p.ConfigDir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("config dir perms = %v, %v", info.Mode().Perm(), err)
	}
	for _, path := range []string{p.TokenFile(), p.LogFile(), p.StageCacheDir(), p.PacksDir()} {
		if !strings.HasPrefix(path, home) {
			t.Errorf("%s not under DOPPIO_HOME", path)
		}
	}
}

func TestLoadOrCreateToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "token")
	tok, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 64 {
		t.Fatalf("token length = %d", len(tok))
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token perms = %v", info.Mode().Perm())
	}
	again, err := LoadOrCreateToken(path)
	if err != nil || again != tok {
		t.Fatalf("reload = %q, %v", again, err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateToken(path); err == nil || !strings.Contains(err.Error(), "other users") {
		t.Fatalf("world-readable token err = %v", err)
	}
	short := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(short, []byte("abc"), 0o600)
	if _, err := LoadOrCreateToken(short); err == nil {
		t.Fatal("want error for malformed token")
	}
}

func TestValidateLoopbackAddr(t *testing.T) {
	tests := map[string]bool{
		"127.0.0.1:7341": true,
		"localhost:7341": true,
		"[::1]:7341":     true,
		"0.0.0.0:7341":   false,
		"192.168.1.5:80": false,
		":7341":          false,
		"127.0.0.1":      false,
		"127.0.0.1:":     false,
	}
	for addr, ok := range tests {
		if err := ValidateLoopbackAddr(addr); (err == nil) != ok {
			t.Errorf("ValidateLoopbackAddr(%q) err = %v, want ok=%v", addr, err, ok)
		}
	}
}

func TestAllowedHosts(t *testing.T) {
	h := AllowedHosts("127.0.0.1:7341")
	for _, want := range []string{"127.0.0.1:7341", "localhost:7341", "[::1]:7341"} {
		if !h[want] {
			t.Errorf("missing %s", want)
		}
	}
	if h["evil.example:7341"] {
		t.Error("foreign host allowed")
	}
	if len(AllowedHosts("garbage")) != 0 {
		t.Error("garbage addr should allow nothing")
	}
}

func TestResolveWorkerDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveWorkerDir(dir)
	if err != nil || got != dir {
		t.Fatalf("explicit = %q, %v", got, err)
	}
	t.Setenv("DOPPIO_WORKER_DIR", dir)
	if got, err := ResolveWorkerDir(""); err != nil || got != dir {
		t.Fatalf("env = %q, %v", got, err)
	}
}
