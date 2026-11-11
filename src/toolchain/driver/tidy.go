package driver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Tidy runs go mod tidy for the main module, letting the go command see
// the imports of .gpp files (through stub files) and G++ modules from the
// module cache (through their copies). args are go mod tidy's flags.
func Tidy(dir string, args []string) error {
	prog, err := Load(&Config{Dir: dir, Tests: true}, "./...")
	if err != nil {
		return err
	}
	l := prog.loader
	if l.modRoot == "" {
		return fmt.Errorf("no go.mod file found")
	}
	ov, cleanup, err := l.overlayFile()
	if err != nil {
		return err
	}
	defer cleanup()

	run := func(extra ...string) error {
		cmd := exec.Command("go", append(append([]string{"mod", "tidy", "-overlay=" + ov}, extra...), args...)...)
		cmd.Dir = l.modRoot
		cmd.Env = l.env()
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		return cmd.Run()
	}
	if l.modFile == "" {
		return run()
	}

	// Tidy the module file with the replacements, then write it back
	// without them.
	if err := run("-modfile=" + l.modFile); err != nil {
		return err
	}
	tidied, err := os.ReadFile(l.modFile)
	if err != nil {
		return err
	}
	mod := string(tidied)
	if i := strings.Index(mod, "\n// Added by gpp:"); i >= 0 {
		mod = mod[:i+1]
	}
	if err := os.WriteFile(filepath.Join(l.modRoot, "go.mod"), []byte(mod), 0o644); err != nil {
		return err
	}
	// The tidied sums lack the replaced modules (replacements by
	// directory have none): keep the user's sums for those.
	sums, _ := os.ReadFile(strings.TrimSuffix(l.modFile, ".mod") + ".sum")
	old, _ := os.ReadFile(filepath.Join(l.modRoot, "go.sum"))
	lines := strings.Split(strings.TrimSpace(string(sums)), "\n")
	have := map[string]bool{}
	for _, line := range lines {
		have[line] = true
	}
	for _, line := range strings.Split(string(old), "\n") {
		path, rest, ok := strings.Cut(line, " ")
		if !ok || have[line] {
			continue
		}
		version, _, _ := strings.Cut(rest, " ")
		version = strings.TrimSuffix(version, "/go.mod")
		if _, replaced := l.replaced[path+"@"+version]; replaced {
			lines = append(lines, line)
		}
	}
	out := strings.TrimSpace(strings.Join(lines, "\n"))
	if out != "" {
		out += "\n"
	}
	return os.WriteFile(filepath.Join(l.modRoot, "go.sum"), []byte(out), 0o644)
}
