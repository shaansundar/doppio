// Package cache is a content-addressed store for input blobs and stage outputs.
//
// Stage keys are Merkle-style: a stage's key hashes its own identity (stage,
// version, model, params) together with the keys of its inputs, so changing a
// model or parameter invalidates exactly the affected downstream stages.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const metaFile = "meta.json"

// ErrInvalidKey is returned for keys that are not 64 lowercase hex characters.
var ErrInvalidKey = errors.New("invalid cache key")

// ErrTooLarge is returned when a blob exceeds the configured size limit.
var ErrTooLarge = errors.New("blob exceeds size limit")

// KeyParts identifies one stage computation.
type KeyParts struct {
	Stage         string            `json:"stage"`
	StageVersion  string            `json:"stage_version"`
	ModelID       string            `json:"model_id,omitempty"`
	ModelRevision string            `json:"model_revision,omitempty"`
	Params        any               `json:"params,omitempty"`
	Inputs        map[string]string `json:"inputs,omitempty"`
}

// Key returns the hex SHA-256 of the canonical JSON encoding of p.
// encoding/json sorts map keys, which makes the encoding deterministic.
func Key(p KeyParts) (string, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("failed to encode cache key: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ValidKey reports whether k looks like a key produced by Key or a blob hash.
func ValidKey(k string) bool {
	if len(k) != 64 {
		return false
	}
	for _, c := range k {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Store is rooted at a directory with stages/, inputs/ and tmp/ children.
type Store struct {
	root string
}

// Entry is a committed stage output directory.
type Entry struct {
	Key  string
	Dir  string
	Meta []byte
}

// Open creates the store layout under root.
func Open(root string) (*Store, error) {
	for _, dir := range []string{"stages", "inputs", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			return nil, fmt.Errorf("failed to create cache directory: %w", err)
		}
	}
	return &Store{root: root}, nil
}

// Root returns the store's root directory.
func (s *Store) Root() string { return s.root }

func (s *Store) stageDir(key string) string {
	return filepath.Join(s.root, "stages", key[:2], key)
}

// Get returns the committed entry for key, if present, and refreshes its
// access time for LRU pruning.
func (s *Store) Get(key string) (Entry, bool, error) {
	if !ValidKey(key) {
		return Entry{}, false, ErrInvalidKey
	}
	dir := s.stageDir(key)
	meta, err := os.ReadFile(filepath.Join(dir, metaFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("failed to read cache entry: %w", err)
	}
	now := time.Now()
	_ = os.Chtimes(filepath.Join(dir, metaFile), now, now)
	return Entry{Key: key, Dir: dir, Meta: meta}, true, nil
}

// Txn is an in-progress stage output written to a private temp directory.
type Txn struct {
	store *Store
	key   string
	dir   string
	done  bool
}

// Begin starts writing the output for key.
func (s *Store) Begin(key string) (*Txn, error) {
	if !ValidKey(key) {
		return nil, ErrInvalidKey
	}
	dir, err := os.MkdirTemp(filepath.Join(s.root, "tmp"), key[:12]+"-")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp stage directory: %w", err)
	}
	return &Txn{store: s, key: key, dir: dir}, nil
}

// Dir is where the stage writes its artifacts.
func (t *Txn) Dir() string { return t.dir }

// Commit atomically publishes the output. If another writer committed the
// same key first, that entry wins and this one is discarded.
func (t *Txn) Commit(meta []byte) (Entry, error) {
	if t.done {
		return Entry{}, errors.New("transaction already finished")
	}
	t.done = true
	if err := os.WriteFile(filepath.Join(t.dir, metaFile), meta, 0o600); err != nil {
		_ = os.RemoveAll(t.dir)
		return Entry{}, fmt.Errorf("failed to write cache metadata: %w", err)
	}
	final := t.store.stageDir(t.key)
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		_ = os.RemoveAll(t.dir)
		return Entry{}, fmt.Errorf("failed to create cache shard: %w", err)
	}
	if err := os.Rename(t.dir, final); err != nil {
		_ = os.RemoveAll(t.dir)
		if entry, ok, getErr := t.store.Get(t.key); getErr == nil && ok {
			return entry, nil
		}
		return Entry{}, fmt.Errorf("failed to commit cache entry: %w", err)
	}
	return Entry{Key: t.key, Dir: final, Meta: meta}, nil
}

// Abort discards the output. Safe to call after Commit.
func (t *Txn) Abort() {
	if t.done {
		return
	}
	t.done = true
	_ = os.RemoveAll(t.dir)
}

// PutBlob stores r under its SHA-256, refusing more than maxBytes.
// ext (e.g. ".jpg") is kept so decoders can use it as a hint.
func (s *Store) PutBlob(r io.Reader, maxBytes int64, ext string) (sha, path string, err error) {
	if len(ext) > 10 || filepath.Base(ext) != ext {
		return "", "", fmt.Errorf("invalid blob extension %q", ext)
	}
	tmp, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "blob-")
	if err != nil {
		return "", "", fmt.Errorf("failed to create temp blob: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, maxBytes+1))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", "", fmt.Errorf("failed to write blob: %w", err)
	}
	if n > maxBytes {
		return "", "", ErrTooLarge
	}
	sha = hex.EncodeToString(h.Sum(nil))
	path = filepath.Join(s.root, "inputs", sha+ext)
	if err = os.Rename(tmp.Name(), path); err != nil {
		return "", "", fmt.Errorf("failed to store blob: %w", err)
	}
	return sha, path, nil
}

// Prune deletes least recently used stage entries and input blobs until
// together they use at most maxBytes. It returns the number of bytes freed.
func (s *Store) Prune(maxBytes int64) (int64, error) {
	type item struct {
		dir   string
		size  int64
		atime time.Time
	}
	var items []item
	var total int64
	blobs, err := os.ReadDir(filepath.Join(s.root, "inputs"))
	if err != nil {
		return 0, fmt.Errorf("failed to list input blobs: %w", err)
	}
	for _, b := range blobs {
		info, err := b.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		items = append(items, item{filepath.Join(s.root, "inputs", b.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	shards, err := os.ReadDir(filepath.Join(s.root, "stages"))
	if err != nil {
		return 0, fmt.Errorf("failed to list cache: %w", err)
	}
	for _, shard := range shards {
		entries, err := os.ReadDir(filepath.Join(s.root, "stages", shard.Name()))
		if err != nil {
			return 0, fmt.Errorf("failed to list cache shard: %w", err)
		}
		for _, e := range entries {
			dir := filepath.Join(s.root, "stages", shard.Name(), e.Name())
			info, err := os.Stat(filepath.Join(dir, metaFile))
			if err != nil {
				continue
			}
			size, err := dirSize(dir)
			if err != nil {
				return 0, err
			}
			items = append(items, item{dir, size, info.ModTime()})
			total += size
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].atime.Before(items[j].atime) })
	var freed int64
	for _, it := range items {
		if total <= maxBytes {
			break
		}
		if err := os.RemoveAll(it.dir); err != nil {
			return freed, fmt.Errorf("failed to prune cache entry: %w", err)
		}
		total -= it.size
		freed += it.size
	}
	return freed, nil
}

func dirSize(dir string) (int64, error) {
	var size int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("failed to size cache entry: %w", err)
	}
	return size, nil
}
