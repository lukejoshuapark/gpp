package driver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/lukejoshuapark/gpp/src/toolchain/ast"
	"github.com/lukejoshuapark/gpp/src/toolchain/lower"
)

// A Lowered file is the Go translation of a .gpp file.
type Lowered struct {
	Gpp  string // the .gpp file
	Stub string // the .go path the go command sees for it
	Src  []byte // the Go source
}

// Lower lowers the G++ files of every package in prog that has them. It
// fails, returning the diagnostics, if a package with G++ files (or a root
// package) has errors.
func Lower(prog *Program) ([]*Lowered, []Diagnostic) {
	var diags []Diagnostic
	for _, p := range prog.Order {
		if p.HasGpp() || !p.DepOnly {
			diags = append(diags, p.Diagnostics...)
		}
	}
	if len(diags) > 0 {
		return nil, sortDiagnostics(diags)
	}

	var out []*Lowered
	done := map[string]bool{}
	for _, p := range prog.Order {
		if !p.HasGpp() {
			continue
		}
		files := map[string]*ast.File{}
		for _, f := range p.Syntax {
			files[prog.Fset.Position(f.Pos()).Filename] = f
		}
		for _, path := range p.GppFiles {
			if done[path] {
				continue // a test variant of a package already lowered
			}
			f := files[path]
			if f == nil {
				continue
			}
			done[path] = true
			src, errs := lower.File(prog.Fset, p.Types, p.Info, f)
			for _, e := range errs {
				diags = append(diags, Diagnostic{Pos: e.Pos, End: e.Pos, Msg: e.Msg})
			}
			out = append(out, &Lowered{Gpp: path, Stub: filepath.Join(filepath.Dir(path), StubName(filepath.Base(path))), Src: src})
		}
	}
	if len(diags) > 0 {
		return nil, sortDiagnostics(diags)
	}
	return out, nil
}

// WriteOverlay writes the lowered files to the cache and returns the path
// of a go command overlay file presenting them in place of the .gpp files.
func WriteOverlay(files []*Lowered) (string, error) {
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	replace := map[string]string{}
	for _, f := range files {
		sum := sha256.Sum256(f.Src)
		name := hex.EncodeToString(sum[:12]) + "-" + filepath.Base(f.Stub)
		path := filepath.Join(dir, "gen", name)
		if _, err := os.Stat(path); err != nil {
			if err := writeFileAtomic(path, f.Src); err != nil {
				return "", err
			}
		}
		replace[f.Stub] = path
	}
	data, err := json.MarshalIndent(map[string]any{"Replace": replace}, "", "\t")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	ov := filepath.Join(dir, "overlay", hex.EncodeToString(sum[:12])+".json")
	if err := writeFileAtomic(ov, data); err != nil {
		return "", err
	}
	return ov, nil
}

// cacheDir returns the directory for generated files.
func cacheDir() (string, error) {
	if d := os.Getenv("GPPCACHE"); d != "" {
		return d, nil
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("no cache directory: %v (set GPPCACHE)", err)
	}
	return filepath.Join(d, "gpp"), nil
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	return os.Rename(f.Name(), path)
}

func sortDiagnostics(diags []Diagnostic) []Diagnostic {
	seen := map[string]bool{}
	var out []Diagnostic
	for _, d := range diags {
		if k := d.String(); !seen[k] {
			seen[k] = true
			out = append(out, d)
		}
	}
	slices.SortStableFunc(out, func(a, b Diagnostic) int {
		if a.Pos.Filename != b.Pos.Filename {
			if a.Pos.Filename < b.Pos.Filename {
				return -1
			}
			return 1
		}
		if a.Pos.Line != b.Pos.Line {
			return a.Pos.Line - b.Pos.Line
		}
		return a.Pos.Column - b.Pos.Column
	})
	return out
}
