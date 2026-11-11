package driver

import (
	"archive/zip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeProxy writes a file-based module proxy holding one module version
// with the given files.
func writeProxy(t *testing.T, dir, path, version string, files map[string]string) {
	t.Helper()
	vdir := filepath.Join(dir, path, "@v")
	os.MkdirAll(vdir, 0o755)
	zf, err := os.Create(filepath.Join(vdir, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	for name, content := range files {
		w, _ := zw.Create(path + "@" + version + "/" + name)
		w.Write([]byte(content))
	}
	zw.Close()
	zf.Close()
	os.WriteFile(filepath.Join(vdir, version+".mod"), []byte(files["go.mod"]), 0o644)
	os.WriteFile(filepath.Join(vdir, version+".info"), []byte(`{"Version":"`+version+`","Time":"2026-11-01T00:00:00Z"}`), 0o644)
	os.WriteFile(filepath.Join(vdir, "list"), []byte(version+"\n"), 0o644)
}

// A G++ module downloaded into the module cache (where the go command
// allows no overlays) builds through a copy, and gpp mod tidy keeps its
// requirement.
func TestModuleCacheDependency(t *testing.T) {
	tmp := t.TempDir()
	writeProxy(t, filepath.Join(tmp, "proxy"), "example.com/glib", "v1.2.0", map[string]string{
		"go.mod":            "module example.com/glib\n\ngo 1.27\n",
		"colors/colors.gpp": "package colors\n\ntype Color enum { Red; Green }\n\nfunc Pick(red bool) Color => red ? Color.Red : Color.Green\n",
	})
	app := filepath.Join(tmp, "app")
	os.MkdirAll(app, 0o755)
	os.WriteFile(filepath.Join(app, "go.mod"), []byte("module example.com/app\n\ngo 1.27\n\nrequire example.com/glib v1.2.0\n"), 0o644)
	os.WriteFile(filepath.Join(app, "main.gpp"), []byte("package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/glib/colors\"\n)\n\nfunc main() {\n\tc := colors.Pick(false)\n\tfmt.Println(c, c is colors.Color.Green)\n}\n"), 0o644)

	env := []string{
		"GOPROXY=file://" + filepath.Join(tmp, "proxy"), "GOSUMDB=off", "GOFLAGS=-modcacherw -mod=mod",
		"GOMODCACHE=" + filepath.Join(tmp, "modcache"), "GPPCACHE=" + filepath.Join(tmp, "gppcache"),
	}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}

	prog, err := Load(&Config{Dir: app}, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(prog.Errors) > 0 || len(prog.Diagnostics()) > 0 {
		t.Fatalf("errors: %v %v", prog.Errors, prog.Diagnostics())
	}
	if prog.ModFile == "" {
		t.Fatal("no module file replacing the G++ module")
	}
	lowered, diags := Lower(prog)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	ov, err := WriteOverlay(lowered)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", "-overlay="+ov, "-modfile="+prog.ModFile, ".")
	cmd.Dir = app
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "Green true\n" {
		t.Fatalf("go run: %v\n%s", err, out)
	}

	if err := Tidy(app, nil); err != nil {
		t.Fatal(err)
	}
	gomod, _ := os.ReadFile(filepath.Join(app, "go.mod"))
	if !strings.Contains(string(gomod), "require example.com/glib v1.2.0\n") || strings.Contains(string(gomod), "replace") || strings.Contains(string(gomod), "indirect") {
		t.Errorf("go.mod after tidy:\n%s", gomod)
	}
}
