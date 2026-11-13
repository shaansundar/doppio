package pipeline

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shaansundar/doppio/internal/cache"
	"github.com/shaansundar/doppio/internal/scp"
)

// PrunePacks deletes least recently used packs until the packs directory
// holds at most maxBytes, and removes temp directories left by crashed runs.
// Pack files are hard links into the stage cache, so this is what releases
// their disk space once the cache has pruned the same entries.
func (e *Engine) PrunePacks(maxBytes int64) (int64, error) {
	entries, err := os.ReadDir(e.packsDir)
	if err != nil {
		return 0, fmt.Errorf("failed to list packs: %w", err)
	}
	type pack struct {
		dir   string
		size  int64
		atime time.Time
	}
	var packs []pack
	var total, freed int64
	for _, ent := range entries {
		dir := filepath.Join(e.packsDir, ent.Name())
		if !ent.IsDir() {
			continue
		}
		if strings.HasPrefix(ent.Name(), ".tmp-") {
			if info, err := ent.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
				size, _ := treeSize(dir)
				if os.RemoveAll(dir) == nil {
					freed += size
				}
			}
			continue
		}
		if !cache.ValidKey(ent.Name()) {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, scp.ManifestFile))
		if err != nil {
			continue
		}
		size, err := treeSize(dir)
		if err != nil {
			return freed, err
		}
		packs = append(packs, pack{dir, size, info.ModTime()})
		total += size
	}
	sort.Slice(packs, func(i, j int) bool { return packs[i].atime.Before(packs[j].atime) })
	for _, p := range packs {
		if total <= maxBytes {
			break
		}
		if err := os.RemoveAll(p.dir); err != nil {
			return freed, fmt.Errorf("failed to prune pack: %w", err)
		}
		total -= p.size
		freed += p.size
	}
	return freed, nil
}

func treeSize(dir string) (int64, error) {
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
		return 0, fmt.Errorf("failed to size pack: %w", err)
	}
	return size, nil
}
