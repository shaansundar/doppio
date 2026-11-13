package cache

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func mustKey(t *testing.T, p KeyParts) string {
	t.Helper()
	k, err := Key(p)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestKeyIsDeterministicAndSensitive(t *testing.T) {
	base := KeyParts{
		Stage: "geometry", StageVersion: "1", ModelID: "m", ModelRevision: "r",
		Params: map[string]any{"b": 2, "a": 1}, Inputs: map[string]string{"image": "abc"},
	}
	k1 := mustKey(t, base)
	reordered := base
	reordered.Params = map[string]any{"a": 1, "b": 2}
	if k2 := mustKey(t, reordered); k1 != k2 {
		t.Fatalf("map order changed key: %s vs %s", k1, k2)
	}
	if !ValidKey(k1) {
		t.Fatalf("Key produced invalid key %q", k1)
	}

	variants := map[string]func(p *KeyParts){
		"stage":    func(p *KeyParts) { p.Stage = "maps" },
		"version":  func(p *KeyParts) { p.StageVersion = "2" },
		"model":    func(p *KeyParts) { p.ModelID = "other" },
		"revision": func(p *KeyParts) { p.ModelRevision = "r2" },
		"params":   func(p *KeyParts) { p.Params = map[string]any{"a": 1, "b": 3} },
		"input":    func(p *KeyParts) { p.Inputs = map[string]string{"image": "abd"} },
	}
	for name, mutate := range variants {
		t.Run(name, func(t *testing.T) {
			p := base
			mutate(&p)
			if mustKey(t, p) == k1 {
				t.Fatalf("changing %s did not change the key", name)
			}
		})
	}
}

func TestKeyRejectsUnencodableParams(t *testing.T) {
	if _, err := Key(KeyParts{Params: make(chan int)}); err == nil {
		t.Fatal("want error for unencodable params")
	}
}

func TestValidKey(t *testing.T) {
	tests := map[string]bool{
		strings.Repeat("a", 64):         true,
		strings.Repeat("A", 64):         false,
		strings.Repeat("a", 63):         false,
		"../" + strings.Repeat("a", 61): false,
	}
	for in, want := range tests {
		if got := ValidKey(in); got != want {
			t.Errorf("ValidKey(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestBeginCommitGet(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := mustKey(t, KeyParts{Stage: "x"})
	if _, ok, err := s.Get(key); ok || err != nil {
		t.Fatalf("Get before commit = %v, %v", ok, err)
	}
	txn, err := s.Begin(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(txn.Dir(), "out.bin"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := txn.Commit([]byte(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	txn.Abort() // no-op after commit
	got, ok, err := s.Get(key)
	if err != nil || !ok {
		t.Fatalf("Get after commit = %v, %v", ok, err)
	}
	if got.Dir != entry.Dir || string(got.Meta) != `{"ok":true}` {
		t.Fatalf("Get = %+v, want dir %s", got, entry.Dir)
	}
	if data, _ := os.ReadFile(filepath.Join(got.Dir, "out.bin")); string(data) != "hello" {
		t.Fatalf("artifact not preserved: %q", data)
	}
	if _, err := txn.Commit(nil); err == nil {
		t.Fatal("second Commit want error")
	}
}

func TestConcurrentCommitsFirstWriterWins(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := mustKey(t, KeyParts{Stage: "race"})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			txn, err := s.Begin(key)
			if err != nil {
				errs <- err
				return
			}
			if _, err := txn.Commit([]byte(`{}`)); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent commit error: %v", err)
	}
	tmp, _ := os.ReadDir(filepath.Join(s.Root(), "tmp"))
	if len(tmp) != 0 {
		t.Errorf("temp dirs leaked: %d", len(tmp))
	}
}

func TestAbortRemovesTempDir(t *testing.T) {
	s, _ := Open(t.TempDir())
	txn, err := s.Begin(mustKey(t, KeyParts{Stage: "abort"}))
	if err != nil {
		t.Fatal(err)
	}
	txn.Abort()
	if _, err := os.Stat(txn.Dir()); !os.IsNotExist(err) {
		t.Fatalf("temp dir still exists: %v", err)
	}
}

func TestInvalidKeysRejected(t *testing.T) {
	s, _ := Open(t.TempDir())
	if _, _, err := s.Get("../../etc"); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("Get err = %v", err)
	}
	if _, err := s.Begin("nope"); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("Begin err = %v", err)
	}
}

func TestPutBlob(t *testing.T) {
	s, _ := Open(t.TempDir())
	sha, path, err := s.PutBlob(bytes.NewReader([]byte("abc")), 10, ".jpg")
	if err != nil {
		t.Fatal(err)
	}
	// sha256("abc")
	if sha != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("sha = %s", sha)
	}
	if filepath.Base(path) != sha+".jpg" {
		t.Fatalf("path = %s", path)
	}
	if _, _, err := s.PutBlob(bytes.NewReader([]byte("too long")), 3, ".jpg"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize err = %v", err)
	}
	if _, _, err := s.PutBlob(bytes.NewReader(nil), 3, "../x"); err == nil {
		t.Fatal("want error for traversal extension")
	}
	tmp, _ := os.ReadDir(filepath.Join(s.Root(), "tmp"))
	if len(tmp) != 0 {
		t.Errorf("temp blobs leaked: %d", len(tmp))
	}
}

func TestPruneEvictsLeastRecentlyUsed(t *testing.T) {
	s, _ := Open(t.TempDir())
	keys := make([]string, 3)
	for i := range keys {
		keys[i] = mustKey(t, KeyParts{Stage: "p", StageVersion: string(rune('a' + i))})
		txn, _ := s.Begin(keys[i])
		_ = os.WriteFile(filepath.Join(txn.Dir(), "data"), bytes.Repeat([]byte("x"), 1000), 0o600)
		e, err := txn.Commit([]byte("{}"))
		if err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(time.Duration(i-10) * time.Minute)
		_ = os.Chtimes(filepath.Join(e.Dir, metaFile), old, old)
	}
	freed, err := s.Prune(2100) // room for two entries (~1002 bytes each)
	if err != nil {
		t.Fatal(err)
	}
	if freed == 0 {
		t.Fatal("Prune freed nothing")
	}
	if _, ok, _ := s.Get(keys[0]); ok {
		t.Error("oldest entry survived")
	}
	for _, k := range keys[1:] {
		if _, ok, _ := s.Get(k); !ok {
			t.Errorf("recent entry %s pruned", k[:8])
		}
	}
}

func TestPruneIncludesInputBlobs(t *testing.T) {
	s, _ := Open(t.TempDir())
	_, oldPath, err := s.PutBlob(bytes.NewReader(bytes.Repeat([]byte("o"), 1000)), 2000, ".jpg")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(oldPath, old, old)
	_, newPath, _ := s.PutBlob(bytes.NewReader(bytes.Repeat([]byte("n"), 1000)), 2000, ".jpg")
	if _, err := s.Prune(1500); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("old blob survived prune")
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Error("recent blob pruned")
	}
}
