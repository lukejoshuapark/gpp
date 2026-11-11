package driver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
)

// A Cache keeps type-checked packages between loads (in the language
// server), so that only edited packages and their dependents are checked
// again. All loads sharing a cache share its file set.
type Cache struct {
	fset *token.FileSet
	mu   sync.Mutex
	pkgs map[string]*Package // by ID
}

// NewCache returns an empty cache.
func NewCache() *Cache {
	return &Cache{fset: token.NewFileSet(), pkgs: map[string]*Package{}}
}

func (c *Cache) get(id, hash string, full bool) *Package {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p := c.pkgs[cacheKey(id, full)]; p != nil && p.hash == hash {
		return p
	}
	return nil
}

func (c *Cache) put(p *Package) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pkgs[cacheKey(p.ID, p.full)] = p
}

func cacheKey(id string, full bool) string { return fmt.Sprintf("%s|%v", id, full) }

// hashPackage identifies the inputs of type-checking p: its files (by
// content for unsaved files, else by size and modification time), its
// dependencies' hashes, and whether bodies are checked.
func (l *loader) hashPackage(prog *Program, p *Package, full bool) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s %s %v\n", p.ID, p.Name, full)
	for _, path := range slices.Concat(p.GoFiles, p.GppFiles) {
		if b, ok := l.cfg.Overlay[path]; ok {
			sum := sha256.Sum256(b)
			fmt.Fprintf(h, "%s overlay %x\n", path, sum)
		} else if fi, err := os.Stat(path); err == nil {
			fmt.Fprintf(h, "%s %d %d\n", path, fi.Size(), fi.ModTime().UnixNano())
		}
	}
	var deps []string
	for _, id := range p.Imports {
		deps = append(deps, id)
	}
	sort.Strings(deps)
	for _, id := range deps {
		if d := prog.Packages[id]; d != nil {
			fmt.Fprintf(h, "dep %s %s\n", id, d.hash)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func numCPU() int { return runtime.NumCPU() }

func goarch() string {
	if a := os.Getenv("GOARCH"); a != "" {
		return a
	}
	return runtime.GOARCH
}

// isLocalPattern reports whether a package pattern names a directory.
func isLocalPattern(pat string) bool {
	return pat == "." || pat == ".." || filepath.IsAbs(pat) ||
		strings.HasPrefix(pat, "./") || strings.HasPrefix(pat, "../")
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}
