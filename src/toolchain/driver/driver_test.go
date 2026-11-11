package driver

import (
	"flag"
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
