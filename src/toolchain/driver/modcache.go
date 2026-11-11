package driver

// G++ modules from the module cache.
//
// The go command does not allow overlays for files in the module cache, so
// a dependency there with .gpp files cannot be presented to it as Go. The
// loader copies such a module version (once: versions are immutable) into
// gpp's cache, and runs the go command with a temporary module file that
// replaces the module with the copy. The user's go.mod is not touched.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

// modCacheDir returns GOMODCACHE.
func (l *loader) modCacheDir() string {
	if l.gomodcache == nil {
		dir := ""
		cmd := exec.Command("go", "env", "GOMODCACHE")
		cmd.Dir = l.cfg.Dir
		cmd.Env = l.env()
		if out, err := cmd.Output(); err == nil {
			dir = strings.TrimSpace(string(out))
		}
		l.gomodcache = &dir
	}
	return *l.gomodcache
}

func (l *loader) inModCache(dir string) bool {
	mc := l.modCacheDir()
	return mc != "" && within(dir, mc)
}

// hasGppFiles reports whether dir has .gpp files.
func hasGppFiles(dir string) bool {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && IsGppFile(e.Name()) {
			return true
		}
	}
	return false
}

// replaceModule arranges for the module version mod (in the module cache)
// to be replaced by a copy in gpp's cache. It reports whether this is a
// new replacement.
func (l *loader) replaceModule(mod listModule) (bool, error) {
	key := mod.Path + "@" + mod.Version
	if l.replaced[key] != "" {
		return false, nil
	}
	if l.modRoot == "" {
		return false, fmt.Errorf("%s has G++ files: building it needs a main module (go.mod)", key)
	}
	cache, err := cacheDir()
	if err != nil {
		return false, err
	}
	dst := filepath.Join(cache, "mod", escapeModulePath(mod.Path)+"@"+mod.Version)
	if _, err := os.Stat(dst); err != nil {
		if err := copyTree(mod.Dir, dst); err != nil {
			return false, fmt.Errorf("copying %s: %v", key, err)
		}
	}
	if l.replaced == nil {
		l.replaced = map[string]string{}
	}
	l.replaced[key] = dst
	l.modules = nil // the build list changed
	l.addGppDirs(dst)
	return true, l.writeModFile()
}

// writeModFile writes the main module's go.mod and go.sum, with the
// replacements, to gpp's cache, and uses them for the go command.
func (l *loader) writeModFile() error {
	gomod, err := os.ReadFile(filepath.Join(l.modRoot, "go.mod"))
	if err != nil {
		return err
	}
	gosum, _ := os.ReadFile(filepath.Join(l.modRoot, "go.sum"))
	var b strings.Builder
	b.Write(gomod)
	b.WriteString("\n// Added by gpp: G++ modules, copied from the module cache.\n")
	keys := make([]string, 0, len(l.replaced))
	for k := range l.replaced {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		path, version, _ := strings.Cut(k, "@")
		fmt.Fprintf(&b, "replace %s %s => %s\n", path, version, l.replaced[k])
	}
	cache, err := cacheDir()
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(l.modRoot + "\x00" + b.String()))
	base := filepath.Join(cache, "modfile", hex.EncodeToString(sum[:12]))
	if err := writeFileAtomic(base+".sum", gosum); err != nil {
		return err
	}
	if err := writeFileAtomic(base+".mod", []byte(b.String())); err != nil {
		return err
	}
	l.modFile = base + ".mod"
	return nil
}

// copyTree copies the directory src to dst (which must not exist), via a
// temporary directory so that dst appears complete or not at all.
func copyTree(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".tmp-")
	if err != nil {
		return err
	}
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(tmp, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err == nil {
		err = os.Rename(tmp, dst)
		if err != nil && fileExists(dst) {
			err = nil // another gpp got there first
		}
	}
	if err != nil {
		os.RemoveAll(tmp)
	}
	return err
}

// escapeModulePath escapes upper-case letters as the module cache does
// (!x for X), so paths differing in case don't collide.
func escapeModulePath(path string) string {
	var b strings.Builder
	for _, r := range path {
		if unicode.IsUpper(r) {
			b.WriteByte('!')
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}
