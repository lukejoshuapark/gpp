// Package driver loads, type-checks and builds G++ programs.
//
// G++ packages live in ordinary Go modules: a package directory may hold
// .go and .gpp files side by side. The driver asks the go command about
// packages (go list), presenting each .gpp file to it as a stub .go file
// through an overlay, then parses and type-checks every package from
// source with the G++ front end.
package driver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/build/constraint"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"gpp/ast"
	"gpp/lexer"
	"gpp/parser"
	"gpp/types"
)

// Config configures loading.
type Config struct {
	// Dir is the directory the patterns are relative to (the current
	// directory if empty).
	Dir string
	// Overlay holds the contents of files that differ from the disk
	// (editor buffers), by absolute path.
	Overlay map[string][]byte
	// Tests includes the test files of the matched packages.
	Tests bool
	// Env is the environment for the go command (os.Environ if nil).
	Env []string
	// Cache, if set, holds packages type-checked by earlier loads, reused
	// when neither they nor their dependencies changed.
	Cache *Cache
}

// A Package is a type-checked package.
type Package struct {
	ID       string // unique: the import path, with " [p.test]" for test variants
	PkgPath  string
	Name     string
	Dir      string
	Standard bool
	DepOnly  bool   // only loaded as a dependency of the requested packages
	ForTest  string // for test variants: the package being tested
	Module   *Module

	GoFiles  []string // .go files (abs), excluding generated .gpp.go files
	GppFiles []string // .gpp files (abs)

	Imports map[string]string // import path in source -> package ID

	Syntax []*ast.File // parsed files, .go then .gpp, in the order above
	Types  *types.Package
	Info   *types.Info

	Diagnostics []Diagnostic // parse and type errors

	hash string // content hash of sources and dependencies, for caching
	full bool   // checked with function bodies
}

// HasGpp reports whether the package has G++ files.
func (p *Package) HasGpp() bool { return len(p.GppFiles) > 0 }

// A Module describes a Go module.
type Module struct {
	Path      string
	Dir       string
	GoVersion string
	Main      bool
}

// A Diagnostic is an error in a package.
type Diagnostic struct {
	Pos  token.Position
	End  token.Position
	Msg  string
	Soft bool // e.g. unused variables: does not prevent further analysis
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%s: %s", d.Pos, d.Msg)
}

// A Program is the result of a load.
type Program struct {
	Fset     *token.FileSet
	Packages map[string]*Package // by ID
	Roots    []*Package          // the packages matching the patterns
	Order    []*Package          // dependency order
	ModRoot  string              // main module root, if any
	// Errors are problems with the patterns or the module (e.g. a
	// package that cannot be found), as opposed to package diagnostics.
	Errors []string
}

// Diagnostics returns the diagnostics of the root packages, sorted.
func (prog *Program) Diagnostics() []Diagnostic {
	var all []Diagnostic
	seen := map[string]bool{}
	for _, p := range prog.Roots {
		for _, d := range p.Diagnostics {
			k := d.String()
			if !seen[k] {
				seen[k] = true
				all = append(all, d)
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i].Pos, all[j].Pos
		if a.Filename != b.Filename {
			return a.Filename < b.Filename
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})
	return all
}

// Load loads and type-checks the packages matching patterns (as for the
// go command, e.g. ".", "./...", an import path) and their dependencies.
func Load(cfg *Config, patterns ...string) (*Program, error) {
	if cfg.Dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		cfg.Dir = wd
	}
	dir, err := filepath.Abs(cfg.Dir)
	if err != nil {
		return nil, err
	}
	cfg.Dir = dir
	if len(patterns) == 0 {
		patterns = []string{"."}
	}

	l := &loader{cfg: cfg, stubs: map[string]string{}, fset: token.NewFileSet()}
	if cfg.Cache != nil {
		l.fset = cfg.Cache.fset
	}
	l.modRoot = l.findModRoot()
	if l.modRoot != "" {
		l.addGppDirs(l.modRoot)
	}
	// Patterns naming directories outside the module (or with no module).
	for _, pat := range patterns {
		if d := strings.TrimSuffix(pat, "/..."); isLocalPattern(d) {
			abs := d
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(dir, d)
			}
			if l.modRoot == "" || !within(abs, l.modRoot) {
				if strings.HasSuffix(pat, "/...") {
					l.addGppDirs(abs)
				} else {
					l.addGppDir(abs)
				}
			}
		}
	}

	var list []*listPackage
	for range 4 {
		list, err = l.goList(patterns)
		if err != nil {
			return nil, err
		}
		// Dependencies elsewhere (other modules) may have .gpp files too.
		more := false
		for _, lp := range list {
			if lp.Dir != "" && !lp.Standard && !l.scanned[lp.Dir] {
				if l.addGppDir(lp.Dir) {
					more = true
				}
			}
		}
		if !more {
			break
		}
	}

	prog := &Program{Fset: l.fset, Packages: map[string]*Package{}, ModRoot: l.modRoot}
	for _, lp := range list {
		p := l.newPackage(lp)
		prog.Packages[p.ID] = p
		prog.Order = append(prog.Order, p)
		if !lp.DepOnly {
			prog.Roots = append(prog.Roots, p)
		}
		if lp.Error != nil && len(p.GoFiles)+len(p.GppFiles) == 0 {
			prog.Errors = append(prog.Errors, strings.TrimSpace(lp.Error.Err))
		}
	}
	l.check(prog)
	return prog, nil
}

type loader struct {
	cfg     *Config
	fset    *token.FileSet
	modRoot string
	stubs   map[string]string // stub .go path -> .gpp path
	scanned map[string]bool   // directories scanned for .gpp files
}

func (l *loader) env() []string {
	if l.cfg.Env != nil {
		return l.cfg.Env
	}
	return os.Environ()
}

func (l *loader) findModRoot() string {
	cmd := exec.Command("go", "env", "GOMOD")
	cmd.Dir = l.cfg.Dir
	cmd.Env = l.env()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return ""
	}
	return filepath.Dir(gomod)
}

// addGppDirs scans the directory tree at root (stopping at nested modules)
// for .gpp files.
func (l *loader) addGppDirs(root string) {
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != root {
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
		}
		l.addGppDir(path)
		return nil
	})
}

// addGppDir adds stubs for the .gpp files in dir. It reports whether there
// were any.
func (l *loader) addGppDir(dir string) bool {
	if l.scanned == nil {
		l.scanned = map[string]bool{}
	}
	if l.scanned[dir] {
		return false
	}
	l.scanned[dir] = true
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	found := false
	for _, e := range entries {
		if name := e.Name(); IsGppFile(name) && !e.IsDir() {
			l.stubs[filepath.Join(dir, StubName(name))] = filepath.Join(dir, name)
			found = true
		}
	}
	// Unsaved files in the editor that are not on disk yet.
	for path := range l.cfg.Overlay {
		if filepath.Dir(path) == dir && IsGppFile(filepath.Base(path)) {
			l.stubs[filepath.Join(dir, StubName(filepath.Base(path)))] = path
			found = true
		}
	}
	return found
}

// IsGppFile reports whether name is a G++ source file the go command would
// consider (not starting with _ or .).
func IsGppFile(name string) bool {
	return strings.HasSuffix(name, ".gpp") && !strings.HasPrefix(name, "_") && !strings.HasPrefix(name, ".")
}

// StubName returns the name of the Go file standing in for the G++ file
// name: x.gpp becomes x.gpp.go and x_test.gpp becomes x_test.gpp_test.go,
// keeping the go command's file name conventions (_test, _GOOS, _GOARCH).
func StubName(name string) string {
	if strings.HasSuffix(name, "_test.gpp") {
		return name + "_test.go"
	}
	return name + ".go"
}

// readFile reads a file, preferring the overlay.
func (l *loader) readFile(path string) ([]byte, error) {
	if b, ok := l.cfg.Overlay[path]; ok {
		return b, nil
	}
	return os.ReadFile(path)
}

// stub returns the stub Go source for a .gpp file: its build constraints,
// package clause and imports.
func (l *loader) stub(gppPath string) []byte {
	src, err := l.readFile(gppPath)
	if err != nil {
		return nil
	}
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, gppPath, src, parser.ImportsOnly|parser.ParseComments)
	var b bytes.Buffer
	if f == nil || f.Name == nil || f.Name.Name == "" || f.Name.Name == "_" {
		// Let the go command complain about the package clause; keep
		// the build constraints.
		writeConstraints(&b, src)
		b.WriteString("package _\n")
		return b.Bytes()
	}
	writeConstraints(&b, src)
	fmt.Fprintf(&b, "package %s\n", f.Name.Name)
	for _, imp := range f.Imports {
		if imp.Path.Value == `"C"` {
			// cgo needs its preamble; G++ files cannot use cgo.
			continue
		}
		fmt.Fprintf(&b, "import _ %s\n", imp.Path.Value)
	}
	return b.Bytes()
}

// writeConstraints copies the //go:build line of a file's header.
func writeConstraints(b *bytes.Buffer, src []byte) {
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") && !constraint.IsGoBuild(line) {
			continue
		}
		if constraint.IsGoBuild(line) {
			b.WriteString(line + "\n\n")
		}
		if !strings.HasPrefix(line, "//") {
			break
		}
	}
}

type listPackage struct {
	ImportPath     string
	Name           string
	Dir            string
	GoFiles        []string
	CgoFiles       []string
	TestGoFiles    []string
	XTestGoFiles   []string
	IgnoredGoFiles []string
	Imports        []string
	ImportMap      map[string]string
	Standard       bool
	DepOnly        bool
	ForTest        string
	Module         *struct {
		Path      string
		Dir       string
		GoVersion string
		Main      bool
	}
	Error *struct{ Err string }
}

// overlayFile writes the go command overlay for the stubs (and any
// unsaved .go files) and returns its path.
func (l *loader) overlayFile() (string, func(), error) {
	tmp, err := os.MkdirTemp("", "gpp-list-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(tmp) }
	replace := map[string]string{}
	i := 0
	write := func(path string, content []byte) error {
		i++
		f := filepath.Join(tmp, fmt.Sprintf("%d_%s", i, filepath.Base(path)))
		if err := os.WriteFile(f, content, 0o644); err != nil {
			return err
		}
		replace[path] = f
		return nil
	}
	for stub, gpp := range l.stubs {
		if err := write(stub, l.stub(gpp)); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	for path, content := range l.cfg.Overlay {
		if strings.HasSuffix(path, ".go") {
			if err := write(path, content); err != nil {
				cleanup()
				return "", nil, err
			}
		}
	}
	data, _ := json.Marshal(map[string]any{"Replace": replace})
	ov := filepath.Join(tmp, "overlay.json")
	if err := os.WriteFile(ov, data, 0o644); err != nil {
		cleanup()
		return "", nil, err
	}
	return ov, cleanup, nil
}

func (l *loader) goList(patterns []string) ([]*listPackage, error) {
	ov, cleanup, err := l.overlayFile()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	args := []string{"list", "-e", "-json=ImportPath,Name,Dir,GoFiles,CgoFiles,TestGoFiles,XTestGoFiles,IgnoredGoFiles,Imports,ImportMap,Standard,DepOnly,ForTest,Module,Error", "-deps", "-overlay=" + ov}
	if l.cfg.Tests {
		args = append(args, "-test")
	}
	args = append(args, "--")
	args = append(args, patterns...)
	cmd := exec.Command("go", args...)
	cmd.Dir = l.cfg.Dir
	cmd.Env = l.env()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("go list: %v\n%s", err, stderr.String())
	}
	var list []*listPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var lp listPackage
		if err := dec.Decode(&lp); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("go list: %v", err)
		}
		if l.cfg.Tests && strings.HasSuffix(lp.ImportPath, ".test") && lp.Name == "main" && lp.ForTest == "" {
			continue // synthesized test main
		}
		list = append(list, &lp)
	}
	return list, nil
}

func (l *loader) newPackage(lp *listPackage) *Package {
	p := &Package{
		ID:       lp.ImportPath,
		PkgPath:  lp.ImportPath,
		Name:     lp.Name,
		Dir:      lp.Dir,
		Standard: lp.Standard,
		DepOnly:  lp.DepOnly,
		ForTest:  lp.ForTest,
		Imports:  map[string]string{},
	}
	if i := strings.Index(p.PkgPath, " ["); i >= 0 {
		p.PkgPath = p.PkgPath[:i]
	}
	if lp.Module != nil {
		p.Module = &Module{Path: lp.Module.Path, Dir: lp.Module.Dir, GoVersion: lp.Module.GoVersion, Main: lp.Module.Main}
	}
	// For test variants (p [p.test]) and external test packages
	// (p_test [p.test]), GoFiles includes the test files.
	files := slices.Concat(lp.GoFiles, lp.CgoFiles)
	seen := map[string]bool{}
	for _, f := range files {
		path := f
		if !filepath.IsAbs(path) {
			path = filepath.Join(lp.Dir, f)
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		if gpp, ok := l.stubs[path]; ok {
			p.GppFiles = append(p.GppFiles, gpp)
		} else if strings.HasSuffix(path, ".gpp.go") || strings.HasSuffix(path, ".gpp_test.go") {
			// generated by gpp gen; the .gpp file is the source
			continue
		} else {
			p.GoFiles = append(p.GoFiles, path)
		}
	}
	for _, imp := range lp.Imports {
		id := imp
		if m, ok := lp.ImportMap[imp]; ok {
			id = m
		}
		p.Imports[imp] = id
	}
	for src, id := range lp.ImportMap {
		p.Imports[src] = id
	}
	return p
}

// ----------------------------------------------------------------------------
// Type checking

func (l *loader) check(prog *Program) {
	// Which packages need function bodies: the roots and every package
	// with G++ files (to lower them), plus test variants of roots.
	full := map[string]bool{}
	for _, p := range prog.Roots {
		full[p.ID] = true
	}
	for _, p := range prog.Order {
		if p.HasGpp() && !p.Standard {
			full[p.ID] = true
		}
	}

	// Check in dependency order, in parallel where possible.
	done := map[string]chan struct{}{}
	for _, p := range prog.Order {
		done[p.ID] = make(chan struct{})
	}
	sem := make(chan struct{}, max(4, numCPU()))
	var wg sync.WaitGroup
	for _, p := range prog.Order {
		wg.Add(1)
		go func(p *Package) {
			defer wg.Done()
			defer close(done[p.ID])
			for _, id := range p.Imports {
				if ch, ok := done[id]; ok {
					<-ch
				}
			}
			sem <- struct{}{}
			defer func() { <-sem }()
			l.checkPackage(prog, p, full[p.ID])
		}(p)
	}
	wg.Wait()
}

func (l *loader) checkPackage(prog *Program, p *Package, full bool) {
	if p.PkgPath == "unsafe" {
		p.Types = types.Unsafe
		return
	}
	cache := l.cfg.Cache
	p.hash = l.hashPackage(prog, p, full)
	if cache != nil {
		if c := cache.get(p.ID, p.hash, full); c != nil {
			p.Syntax, p.Types, p.Info, p.Diagnostics, p.full = c.Syntax, c.Types, c.Info, c.Diagnostics, c.full
			return
		}
	}

	var diags []Diagnostic
	addErr := func(err error) {
		var list lexer.ErrorList
		var terr types.Error
		switch {
		case errors.As(err, &list):
			for _, e := range list {
				diags = append(diags, Diagnostic{Pos: e.Pos, End: e.Pos, Msg: e.Msg})
			}
		case errors.As(err, &terr):
			start, end := terr.Span()
			diags = append(diags, Diagnostic{Pos: l.fset.Position(start), End: l.fset.Position(end), Msg: terr.Msg, Soft: terr.Soft})
		default:
			diags = append(diags, Diagnostic{Msg: err.Error()})
		}
	}

	mode := parser.ParseComments | parser.SkipObjectResolution
	var syntax []*ast.File
	usesCgo := false
	for _, path := range slices.Concat(p.GoFiles, p.GppFiles) {
		src, err := l.readFile(path)
		if err != nil {
			addErr(err)
			continue
		}
		f, err := parser.ParseFile(l.fset, path, src, mode)
		if err != nil {
			addErr(err)
		}
		if f != nil {
			for _, imp := range f.Imports {
				if imp.Path.Value == `"C"` {
					usesCgo = true
				}
			}
			syntax = append(syntax, f)
		}
	}
	p.Syntax = syntax

	info := &types.Info{
		Types:        map[ast.Expr]types.TypeAndValue{},
		Defs:         map[*ast.Ident]types.Object{},
		Uses:         map[*ast.Ident]types.Object{},
		Implicits:    map[ast.Node]types.Object{},
		Selections:   map[*ast.SelectorExpr]*types.Selection{},
		Scopes:       map[ast.Node]*types.Scope{},
		Instances:    map[*ast.Ident]types.Instance{},
		FileVersions: map[*ast.File]string{},
	}
	goVersion := ""
	if p.Module != nil && p.Module.GoVersion != "" {
		goVersion = "go" + p.Module.GoVersion
	}
	conf := &types.Config{
		GoVersion:        goVersion,
		IgnoreFuncBodies: !full,
		FakeImportC:      usesCgo,
		Error:            addErr,
		Importer:         importerFunc(func(path string) (*types.Package, error) { return l.importPackage(prog, p, path) }),
		Sizes:            types.SizesFor("gc", goarch()),
	}
	tpkg, _ := conf.Check(p.PkgPath, l.fset, syntax, info)
	p.Types = tpkg
	p.Info = info
	p.full = full
	// Only report diagnostics for packages we fully check; dependencies
	// are assumed to build (the go command reports them otherwise).
	// Packages using cgo cannot be checked fully without running cgo;
	// leave their errors to the go command.
	if full && !usesCgo {
		p.Diagnostics = diags
	}
	if cache != nil {
		cache.put(p)
	}
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

func (l *loader) importPackage(prog *Program, from *Package, path string) (*types.Package, error) {
	if path == "unsafe" {
		return types.Unsafe, nil
	}
	id, ok := from.Imports[path]
	if !ok {
		id = path
	}
	dep, ok := prog.Packages[id]
	if !ok || dep.Types == nil {
		return nil, fmt.Errorf("could not import %s", path)
	}
	return dep.Types, nil
}
