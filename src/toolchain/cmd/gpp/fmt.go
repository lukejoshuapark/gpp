package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gpp/format"
)

// fmtCmd formats G++ files: gpp fmt [-l] [paths]. Directories are
// searched for .gpp files; files are rewritten in place, and their names
// printed. With -l, only the names of files needing formatting are
// printed. "gpp fmt -" formats standard input to standard output.
func fmtCmd(args []string) error {
	list := false
	var paths []string
	for _, a := range args {
		switch a {
		case "-l":
			list = true
		case "-w":
		case "-":
			src, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			out, err := format.Source(src)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(out)
			return err
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("fmt: unknown flag %s", a)
			}
			paths = append(paths, a)
		}
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}
	failed := false
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			return err
		}
		var files []string
		if fi.IsDir() {
			filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if d.IsDir() {
					if path != p && (strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" || d.Name() == "testdata" || d.Name() == "node_modules") {
						return filepath.SkipDir
					}
					return nil
				}
				if strings.HasSuffix(path, ".gpp") {
					files = append(files, path)
				}
				return nil
			})
		} else {
			files = []string{p}
		}
		for _, f := range files {
			src, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			out, err := format.Source(src)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", f, err)
				failed = true
				continue
			}
			if bytes.Equal(src, out) {
				continue
			}
			fmt.Println(f)
			if !list {
				if err := os.WriteFile(f, out, fi.Mode().Perm()|0o200); err != nil {
					return err
				}
			}
		}
	}
	if failed {
		return exitCode(2)
	}
	return nil
}
