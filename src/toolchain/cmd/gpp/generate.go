// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file implements gpp generate, adapted from the go command's
// generate: it runs the //go:generate directives of .gpp files as well as
// .go files (go generate only reads .go files from disk).

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/lukejoshuapark/gpp/src/toolchain/driver"
	"github.com/lukejoshuapark/gpp/src/toolchain/parser"
)

// generateOpts are the flags of gpp generate.
type generateOpts struct {
	run, skip    *regexp.Regexp
	n, v, x      bool
	goroot       string
	goos, goarch string
	wd           string
	failed       bool
}

// generate runs the //go:generate directives in the packages' .go and .gpp
// files: gpp generate [-run regexp] [-skip regexp] [-n] [-v] [-x]
// [build flags] [packages].
func generate(args []string) error {
	flags, pkgs, _ := splitArgs("generate", args)
	o := &generateOpts{}
	var tags string
	for i := 0; i < len(flags); i++ {
		name, val, hasVal := strings.Cut(strings.TrimLeft(flags[i], "-"), "=")
		if !hasVal && valueFlags[name] && i+1 < len(flags) {
			i++
			val = flags[i]
		}
		var err error
		switch name {
		case "run":
			if val != "" {
				o.run, err = regexp.Compile(val)
			}
		case "skip":
			if val != "" {
				o.skip, err = regexp.Compile(val)
			}
		case "n":
			o.n = true
		case "v":
			o.v = true
		case "x":
			o.x = true
		case "tags":
			tags = val
		}
		if err != nil {
			return fmt.Errorf("generate: %v", err)
		}
	}
	// Generators see the "generate" build tag.
	if tags = strings.TrimSpace(tags); tags != "" {
		tags = strings.Join(strings.FieldsFunc(tags, func(r rune) bool { return r == ',' || r == ' ' }), ",") + ","
	}
	env := append(os.Environ(), "GOFLAGS="+strings.TrimSpace(os.Getenv("GOFLAGS")+" -tags="+tags+"generate"))

	out, err := exec.Command("go", "env", "GOROOT", "GOOS", "GOARCH").Output()
	if err != nil {
		return fmt.Errorf("go env: %v", err)
	}
	if vals := strings.Split(strings.TrimSpace(string(out)), "\n"); len(vals) == 3 {
		o.goroot, o.goos, o.goarch = vals[0], vals[1], vals[2]
	}
	o.wd, _ = os.Getwd()

	list, err := driver.ListSources(&driver.Config{Env: env}, pkgs...)
	if err != nil {
		return err
	}
	printed := false
	for _, p := range list {
		if !p.Main {
			if !printed {
				fmt.Fprintf(os.Stderr, "gpp: not generating in packages in dependency modules\n")
				printed = true
			}
			continue
		}
		if p.Error != "" {
			fmt.Fprintln(os.Stderr, p.Error)
			o.failed = true
			continue
		}
		// An error stops generation in the rest of the package's files.
		for _, files := range [][]string{p.Files, p.XFiles} {
			for _, f := range files {
				if !o.generateFile(f) {
					break
				}
			}
		}
	}
	if o.failed {
		return exitCode(1)
	}
	return nil
}

// shortPath returns path relative to the working directory if that is
// shorter.
func (o *generateOpts) shortPath(path string) string {
	if rel, err := filepath.Rel(o.wd, path); err == nil && len(rel) < len(path) {
		return rel
	}
	return path
}

// generateFile runs the directives in one file, reporting whether they all
// succeeded.
func (o *generateOpts) generateFile(absFile string) bool {
	src, err := os.ReadFile(absFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate: %s\n", err)
		o.failed = true
		return false
	}
	// Parse the package clause; ignore files without a valid one.
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.PackageClauseOnly|parser.SkipObjectResolution)
	if err != nil || f.Name == nil {
		return true
	}
	g := &generator{
		o:        o,
		r:        bytes.NewReader(src),
		path:     absFile,
		pkg:      f.Name.Name,
		commands: map[string][]string{},
	}
	return g.run()
}

// A generator holds the state of a single file being scanned for
// generator commands.
type generator struct {
	o        *generateOpts
	r        io.Reader
	path     string // full rooted path name.
	dir      string // full rooted directory of file.
	file     string // base name of file.
	pkg      string
	commands map[string][]string
	lineNum  int // current line number.
	env      []string
}

var errStop = errors.New("error in generation")

// run runs the generators in the file.
func (g *generator) run() (ok bool) {
	// Processing below calls g.errorf on failure, which panics with
	// errStop and abandons the package.
	defer func() {
		if e := recover(); e != nil {
			if e != errStop {
				panic(e)
			}
			ok = false
			g.o.failed = true
		}
	}()
	g.dir, g.file = filepath.Split(g.path)
	g.dir = filepath.Clean(g.dir)
	if g.o.v {
		fmt.Fprintf(os.Stderr, "%s\n", g.o.shortPath(g.path))
	}

	// Scan for lines that start "//go:generate". bufio.Scanner cannot
	// handle the long lines generate directives often have.
	input := bufio.NewReader(g.r)
	var err error
	for {
		g.lineNum++ // 1-indexed.
		var buf []byte
		buf, err = input.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			// Line too long: consume and ignore.
			if isGoGenerate(buf) {
				g.errorf("directive too long")
			}
			for err == bufio.ErrBufferFull {
				_, err = input.ReadSlice('\n')
			}
			if err != nil {
				break
			}
			continue
		}
		if err != nil {
			// Check for a directive at EOF without a final \n.
			if err == io.EOF && isGoGenerate(buf) {
				err = io.ErrUnexpectedEOF
			}
			break
		}
		if !isGoGenerate(buf) {
			continue
		}
		if g.o.run != nil && !g.o.run.Match(bytes.TrimSpace(buf)) {
			continue
		}
		if g.o.skip != nil && g.o.skip.Match(bytes.TrimSpace(buf)) {
			continue
		}

		g.setEnv()
		words := g.split(string(buf))
		if len(words) == 0 {
			g.errorf("no arguments to directive")
		}
		if words[0] == "-command" {
			g.setShorthand(words)
			continue
		}
		if g.o.n || g.o.x {
			fmt.Fprintf(os.Stderr, "%s\n", strings.Join(words, " "))
		}
		if g.o.n {
			continue
		}
		g.exec(words)
	}
	if err != nil && err != io.EOF {
		g.errorf("error reading %s: %s", g.o.shortPath(g.path), err)
	}
	return true
}

func isGoGenerate(buf []byte) bool {
	return bytes.HasPrefix(buf, []byte("//go:generate ")) || bytes.HasPrefix(buf, []byte("//go:generate\t"))
}

// setEnv sets the extra environment variables of a single directive.
func (g *generator) setEnv() {
	env := []string{
		"GOROOT=" + g.o.goroot,
		"GOARCH=" + g.o.goarch,
		"GOOS=" + g.o.goos,
		"GOFILE=" + g.file,
		"GOLINE=" + strconv.Itoa(g.lineNum),
		"GOPACKAGE=" + g.pkg,
		"DOLLAR=" + "$",
	}
	// Prefer the go command's own tools, as go generate does.
	if g.o.goroot != "" {
		path := filepath.Join(g.o.goroot, "bin")
		if p := os.Getenv("PATH"); p != "" {
			path += string(filepath.ListSeparator) + p
		}
		env = append(env, "PATH="+path)
	}
	env = append(env, "PWD="+g.dir)
	g.env = env
}

// split breaks the line into words, evaluating quoted strings and
// environment variables. The line starts with //go:generate.
func (g *generator) split(line string) []string {
	var words []string
	line = line[len("//go:generate ") : len(line)-1] // Drop preamble and final newline.
	// There may still be a carriage return.
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	// One (possibly quoted) word per iteration.
Words:
	for {
		line = strings.TrimLeft(line, " \t")
		if len(line) == 0 {
			break
		}
		if line[0] == '"' {
			for i := 1; i < len(line); i++ {
				c := line[i] // Only looking for ASCII so this is OK.
				switch c {
				case '\\':
					if i+1 == len(line) {
						g.errorf("bad backslash")
					}
					i++ // Absorb next byte (a multibyte one fails in Unquote).
				case '"':
					word, err := strconv.Unquote(line[0 : i+1])
					if err != nil {
						g.errorf("bad quoted string")
					}
					words = append(words, word)
					line = line[i+1:]
					// The next character must be a space or the end of line.
					if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
						g.errorf("expect space after quoted argument")
					}
					continue Words
				}
			}
			g.errorf("mismatched quoted string")
		}
		i := strings.IndexAny(line, " \t")
		if i < 0 {
			i = len(line)
		}
		words = append(words, line[0:i])
		line = line[i:]
	}
	// Substitute a -command shorthand.
	if len(words) > 0 && g.commands[words[0]] != nil {
		words = append(slices.Clone(g.commands[words[0]]), words[1:]...)
	}
	for i, word := range words {
		words[i] = os.Expand(word, g.expandVar)
	}
	return words
}

// errorf prints an error with the file and line number and stops
// generation in the package.
func (g *generator) errorf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s:%d: %s\n", g.o.shortPath(g.path), g.lineNum, fmt.Sprintf(format, args...))
	panic(errStop)
}

// expandVar expands $word, for os.Expand.
func (g *generator) expandVar(word string) string {
	w := word + "="
	for _, e := range g.env {
		if strings.HasPrefix(e, w) {
			return e[len(w):]
		}
	}
	return os.Getenv(word)
}

// setShorthand installs a shorthand defined by a -command directive.
func (g *generator) setShorthand(words []string) {
	if len(words) == 1 {
		g.errorf("no command specified for -command")
	}
	command := words[1]
	if g.commands[command] != nil {
		g.errorf("command %q multiply defined", command)
	}
	g.commands[command] = slices.Clip(words[2:])
}

// exec runs the command words in the file's directory.
func (g *generator) exec(words []string) {
	path := words[0]
	if path != "" && !strings.Contains(path, string(os.PathSeparator)) && g.o.goroot != "" {
		// '//go:generate go run ...' almost certainly means the go command
		// that gpp uses: prefer commands from GOROOT/bin.
		if p, err := exec.LookPath(filepath.Join(g.o.goroot, "bin", path)); err == nil {
			path = p
		}
	}
	cmd := exec.Command(path, words[1:]...)
	cmd.Args[0] = words[0]
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Dir = g.dir
	cmd.Env = append(os.Environ(), g.env...)
	if err := cmd.Run(); err != nil {
		g.errorf("running %q: %s", words[0], err)
	}
}
