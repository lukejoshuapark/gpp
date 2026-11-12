package driver

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "update golden files")

// copyTestModule copies the test program in dir into a new module.
func copyTestModule(t *testing.T, dir string) string {
	t.Helper()
	tmp := t.TempDir()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(tmp, rel), 0o755)
		}
		if d.Name() == "stdout" || d.Name() == "errors" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(tmp, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "go.mod")); err != nil {
		os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module example.com/test\n\ngo 1.27\n"), 0o644)
	}
	return tmp
}

func golden(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		os.WriteFile(path, []byte(got), 0o644)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file (run with -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRun builds and runs each program in testdata/run and compares its
// output with the stdout file.
func TestRun(t *testing.T) {
	dirs, _ := filepath.Glob("../testdata/run/*")
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			t.Parallel()
			mod := copyTestModule(t, dir)
			prog, err := Load(&Config{Dir: mod}, ".")
			if err != nil {
				t.Fatal(err)
			}
			lowered, diags := Lower(prog)
			if len(diags) > 0 {
				t.Fatalf("errors: %v", diags)
			}
			ov, err := WriteOverlay(lowered)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("go", "run", "-overlay="+ov, ".")
			cmd.Dir = mod
			out, err := cmd.CombinedOutput()
			if err != nil && !strings.Contains(dir, "panic") {
				t.Fatalf("%v\n%s", err, out)
			}
			got := strings.ReplaceAll(string(out), mod+string(filepath.Separator), "")
			golden(t, filepath.Join(dir, "stdout"), got)
		})
	}
}

// TestErrors checks each program in testdata/errors and compares the
// errors with the errors file.
func TestErrors(t *testing.T) {
	dirs, _ := filepath.Glob("../testdata/errors/*")
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			t.Parallel()
			mod := copyTestModule(t, dir)
			prog, err := Load(&Config{Dir: mod}, "./...")
			if err != nil {
				t.Fatal(err)
			}
			var b strings.Builder
			for _, e := range prog.Errors {
				b.WriteString(strings.ReplaceAll(e, mod+string(filepath.Separator), "") + "\n")
			}
			for _, d := range prog.Diagnostics() {
				b.WriteString(strings.ReplaceAll(d.String(), mod+string(filepath.Separator), "") + "\n")
			}
			golden(t, filepath.Join(dir, "errors"), b.String())
		})
	}
}

// TestListSources checks that .gpp files are listed in place of their
// stubs, test files included, and that packages with only .gpp files and
// packages that do not compile are found.
func TestListSources(t *testing.T) {
	mod := t.TempDir()
	files := map[string]string{
		"go.mod":     "module example.com/test\n\ngo 1.27\n",
		"a.go":       "package main\n\nfunc main() {}\n",
		"b.gpp":      "package main\n\nvar x = undefined\n",
		"b_test.gpp": "package main\n",
		"x_test.gpp": "package main_test\n",
		"sub/s.gpp":  "package sub\n",
		"tag/t.gpp":  "//go:build generate\n\npackage tag\n",
		"tag/u.gpp":  "//go:build ignore\n\npackage tag\n",
	}
	for name, src := range files {
		path := filepath.Join(mod, name)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := append(os.Environ(), "GOFLAGS=-tags=generate")
	pkgs, err := ListSources(&Config{Dir: mod, Env: env}, "./...")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range pkgs {
		rel := func(fs []string) string {
			var names []string
			for _, f := range fs {
				r, _ := filepath.Rel(mod, f)
				names = append(names, r)
			}
			return strings.Join(names, " ")
		}
		got = append(got, fmt.Sprintf("%s main=%v files=[%s] x=[%s] err=%q", p.ImportPath, p.Main, rel(p.Files), rel(p.XFiles), p.Error))
	}
	want := []string{
		`example.com/test main=true files=[a.go b.gpp b_test.gpp] x=[x_test.gpp] err=""`,
		`example.com/test/sub main=true files=[sub/s.gpp] x=[] err=""`,
		`example.com/test/tag main=true files=[tag/t.gpp] x=[] err=""`,
	}
	if g, w := strings.Join(got, "\n"), strings.Join(want, "\n"); g != w {
		t.Errorf("got:\n%s\nwant:\n%s", g, w)
	}
}
