// Command gpp is the G++ toolchain: it builds, runs, tests, checks and
// formats G++ code, serves the language server, and passes any other
// command through to the go command.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gpp/driver"
	"gpp/lsp"
)

const version = "0.1.0"

const usage = `G++ is a superset of Go.

Usage:

	gpp <command> [arguments]

Commands:

	build    compile packages and dependencies (go build)
	run      compile and run a program (go run)
	test     test packages (go test)
	install  compile and install packages (go install)
	vet      report likely mistakes (go vet)
	check    type-check packages and report errors
	fmt      format G++ (and Go) source files
	gen      write the Go translation of .gpp files next to them
	lsp      run the language server on stdin/stdout
	version  print the G++ version

Any other command is passed to the go command (gpp mod tidy, gpp get, ...).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "build", "run", "test", "install", "vet":
		err = goWithOverlay(cmd, args)
	case "check":
		err = check(args)
	case "fmt":
		err = fmtCmd(args)
	case "gen":
		err = gen(args)
	case "mod":
		if len(args) > 0 && args[0] == "tidy" {
			err = driver.Tidy("", args[1:])
		} else {
			passthrough(cmd, args)
		}
	case "lsp":
		err = lsp.Serve(os.Stdin, os.Stdout)
	case "version":
		fmt.Printf("gpp version %s\n", version)
		passthrough("version", nil)
	case "help", "-h", "-help", "--help":
		if len(args) > 0 {
			passthrough("help", args)
		}
		fmt.Print(usage)
	default:
		passthrough(cmd, args)
	}
	if err != nil {
		var exit exitCode
		if errors.As(err, &exit) {
			os.Exit(int(exit))
		}
		fmt.Fprintln(os.Stderr, "gpp:", err)
		os.Exit(1)
	}
}

type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// passthrough runs the go command with the same arguments and exits with
// its status.
func passthrough(cmd string, args []string) {
	c := exec.Command("go", append([]string{cmd}, args...)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "gpp:", err)
		os.Exit(1)
	}
	if cmd != "version" {
		os.Exit(0)
	}
}

// Build flags of the go command that take a value (as a separate argument).
var valueFlags = map[string]bool{
	"o": true, "p": true, "C": true, "asmflags": true, "buildmode": true, "buildvcs": false,
	"compiler": true, "gccgoflags": true, "gcflags": true, "installsuffix": true, "ldflags": true,
	"mod": true, "modfile": true, "overlay": true, "pkgdir": true, "tags": true, "toolexec": true,
	"pgo": true, "covermode": true, "coverpkg": true, "exec": true, "run": true, "skip": true,
	"bench": true, "benchtime": true, "count": true, "cpu": true, "parallel": true, "timeout": true,
	"blockprofile": true, "coverprofile": true, "cpuprofile": true, "memprofile": true,
	"mutexprofile": true, "outputdir": true, "trace": true, "list": true, "fuzz": true,
	"fuzztime": true, "fuzzminimizetime": true, "shuffle": true, "vettool": true, "benchmem": false,
	"blockprofilerate": true, "memprofilerate": true, "mutexprofilefraction": true, "c": false,
}

// splitArgs splits go command arguments into flags and package patterns
// (or files). For run, arguments after the first .go/.gpp file or package
// are the program's.
func splitArgs(cmd string, args []string) (flags, pkgs, rest []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pkgs = append(pkgs, args[i+1:]...)
			return
		}
		if strings.HasPrefix(a, "-") && len(pkgs) == 0 {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(name, "=") && valueFlags[name] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		if cmd == "run" {
			if strings.HasSuffix(a, ".gpp") || strings.HasSuffix(a, ".go") {
				pkgs = append(pkgs, a)
				continue
			}
			if len(pkgs) == 0 {
				pkgs = append(pkgs, a)
				rest = args[i+1:]
				return
			}
			rest = args[i:]
			return
		}
		if cmd == "test" && len(pkgs) > 0 && strings.HasPrefix(a, "-") {
			rest = args[i:]
			return
		}
		pkgs = append(pkgs, a)
	}
	return
}

// goWithOverlay type-checks and lowers the G++ code in the packages, then
// runs the go command over the result.
func goWithOverlay(cmd string, args []string) error {
	flags, pkgs, rest := splitArgs(cmd, args)
	for _, f := range flags {
		if f == "-overlay" || strings.HasPrefix(f, "-overlay=") {
			return fmt.Errorf("gpp %s does not support -overlay", cmd)
		}
	}

	// Files on the command line (gpp run main.gpp) load as their directory.
	loadPatterns := pkgs
	goPkgs := pkgs
	if len(pkgs) > 0 && (strings.HasSuffix(pkgs[0], ".gpp") || strings.HasSuffix(pkgs[0], ".go")) {
		dirs := map[string]bool{}
		loadPatterns = nil
		goPkgs = nil
		for _, f := range pkgs {
			d := filepath.Dir(f)
			if !dirs[d] {
				dirs[d] = true
				if !filepath.IsAbs(d) && !strings.HasPrefix(d, ".") {
					d = "./" + d
				}
				loadPatterns = append(loadPatterns, d)
			}
			if strings.HasSuffix(f, ".gpp") {
				f = filepath.Join(filepath.Dir(f), driver.StubName(filepath.Base(f)))
			}
			goPkgs = append(goPkgs, f)
		}
	}
	if len(loadPatterns) == 0 {
		loadPatterns = []string{"."}
	}

	prog, err := driver.Load(&driver.Config{Tests: cmd == "test" || cmd == "vet", Env: buildEnv(flags)}, loadPatterns...)
	if err != nil {
		return err
	}
	if len(prog.Errors) > 0 {
		for _, e := range prog.Errors {
			fmt.Fprintln(os.Stderr, e)
		}
		return exitCode(1)
	}
	lowered, diags := driver.Lower(prog)
	if len(diags) > 0 {
		printDiagnostics(os.Stderr, diags)
		return exitCode(1)
	}
	goArgs := []string{cmd}
	if prog.ModFile != "" {
		// G++ modules from the module cache are replaced by copies
		for _, f := range flags {
			if f == "-modfile" || strings.HasPrefix(f, "-modfile=") {
				return fmt.Errorf("gpp %s does not support -modfile with G++ dependencies", cmd)
			}
		}
		goArgs = append(goArgs, "-modfile="+prog.ModFile)
	}
	if len(lowered) > 0 {
		ov, err := driver.WriteOverlay(lowered)
		if err != nil {
			return err
		}
		goArgs = append(goArgs, "-overlay="+ov)
	}
	goArgs = append(goArgs, flags...)
	goArgs = append(goArgs, goPkgs...)
	goArgs = append(goArgs, rest...)
	c := exec.Command("go", goArgs...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return exitCode(ee.ExitCode())
		}
		return err
	}
	return nil
}

// buildEnv applies build flags that change what files are loaded (-tags)
// to the environment of go list.
func buildEnv(flags []string) []string {
	env := os.Environ()
	for i, f := range flags {
		name, val, hasVal := strings.Cut(strings.TrimLeft(f, "-"), "=")
		if name == "tags" {
			if !hasVal && i+1 < len(flags) {
				val = flags[i+1]
			}
			env = append(env, "GOFLAGS="+strings.TrimSpace(os.Getenv("GOFLAGS")+" -tags="+val))
		}
	}
	return env
}

func printDiagnostics(w io.Writer, diags []driver.Diagnostic) {
	wd, _ := os.Getwd()
	for _, d := range diags {
		pos := d.Pos
		if rel, err := filepath.Rel(wd, pos.Filename); err == nil && !strings.HasPrefix(rel, "..") {
			pos.Filename = rel
		}
		if pos.IsValid() {
			fmt.Fprintf(w, "%s: %s\n", pos, d.Msg)
		} else {
			fmt.Fprintln(w, d.Msg)
		}
	}
}

// check type-checks packages and prints their errors.
func check(args []string) error {
	_, pkgs, _ := splitArgs("check", args)
	if len(pkgs) == 0 {
		pkgs = []string{"."}
	}
	prog, err := driver.Load(&driver.Config{Tests: true}, pkgs...)
	if err != nil {
		return err
	}
	for _, e := range prog.Errors {
		fmt.Fprintln(os.Stderr, e)
	}
	diags := prog.Diagnostics()
	printDiagnostics(os.Stderr, diags)
	if len(diags) > 0 || len(prog.Errors) > 0 {
		return exitCode(1)
	}
	return nil
}

// gen writes the lowered Go next to each .gpp file.
func gen(args []string) error {
	_, pkgs, _ := splitArgs("gen", args)
	if len(pkgs) == 0 {
		pkgs = []string{"."}
	}
	prog, err := driver.Load(&driver.Config{Tests: true}, pkgs...)
	if err != nil {
		return err
	}
	for _, e := range prog.Errors {
		fmt.Fprintln(os.Stderr, e)
	}
	lowered, diags := driver.Lower(prog)
	if len(diags) > 0 {
		printDiagnostics(os.Stderr, diags)
		return exitCode(1)
	}
	roots := map[string]bool{}
	for _, p := range prog.Roots {
		for _, f := range p.GppFiles {
			roots[f] = true
		}
	}
	for _, l := range lowered {
		if !roots[l.Gpp] {
			continue
		}
		if err := os.WriteFile(l.Stub, l.Src, 0o644); err != nil {
			return err
		}
		fmt.Println(l.Stub)
	}
	return nil
}
