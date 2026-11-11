package lsp

import (
	"bytes"
	"go/token"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/lukejoshuapark/gpp/src/toolchain/ast"
	"github.com/lukejoshuapark/gpp/src/toolchain/format"
	"github.com/lukejoshuapark/gpp/src/toolchain/parser"
	"github.com/lukejoshuapark/gpp/src/toolchain/types"
)

// Organize imports: remove unused imports and add missing ones (packages
// used as pkg.Name but not imported), like goimports.

func (s *server) organizeImports(snap *snapshot, fi *fileInfo, text []byte) []TextEdit {
	info := fi.pkg.Info
	fset := fi.prog.Fset

	// Unused imports.
	used := map[types.Object]bool{}
	for _, obj := range info.Uses {
		if pn, ok := obj.(*types.PkgName); ok {
			used[pn] = true
		}
	}
	var unused []*ast.ImportSpec
	have := map[string]bool{}
	for _, spec := range fi.file.Imports {
		p, _ := strconv.Unquote(spec.Path.Value)
		have[p] = true
		if p == "C" || spec.Name != nil && (spec.Name.Name == "_" || spec.Name.Name == ".") {
			continue
		}
		var obj types.Object
		if spec.Name != nil {
			obj = info.Defs[spec.Name]
		} else {
			obj = info.Implicits[spec]
		}
		if obj != nil && !used[obj] {
			unused = append(unused, spec)
		}
	}

	// Missing imports: x.Sel where x is not declared.
	sels := map[string]map[string]bool{} // package name -> selected names
	ast.Inspect(fi.file, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok && info.Uses[x] == nil && info.Defs[x] == nil && x.Name != "_" {
				if sels[x.Name] == nil {
					sels[x.Name] = map[string]bool{}
				}
				sels[x.Name][sel.Sel.Name] = true
			}
		}
		return true
	})
	var missing []string
	names := make([]string, 0, len(sels))
	for name := range sels {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if p := s.findImport(snap, fi, name, sels[name]); p != "" && !have[p] {
			missing = append(missing, p)
			have[p] = true
		}
	}
	if len(unused) == 0 && len(missing) == 0 {
		return []TextEdit{}
	}

	// Remove the unused specs (whole lines), then add the missing ones.
	type cut struct{ start, end int }
	var cuts []cut
	lineStart := func(off int) int { return bytes.LastIndexByte(text[:off], '\n') + 1 }
	lineEnd := func(off int) int {
		if i := bytes.IndexByte(text[off:], '\n'); i >= 0 {
			return off + i + 1
		}
		return len(text)
	}
	for _, d := range fi.file.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			continue
		}
		var gone []*ast.ImportSpec
		for _, spec := range gd.Specs {
			if slices.Contains(unused, spec.(*ast.ImportSpec)) {
				gone = append(gone, spec.(*ast.ImportSpec))
			}
		}
		if len(gone) == 0 {
			continue
		}
		if len(gone) == len(gd.Specs) {
			cuts = append(cuts, cut{lineStart(fset.Position(gd.Pos()).Offset), lineEnd(fset.Position(gd.End()).Offset - 1)})
			continue
		}
		for _, spec := range gone {
			start := fset.Position(spec.Pos()).Offset
			if spec.Doc != nil {
				start = fset.Position(spec.Doc.Pos()).Offset
			}
			end := fset.Position(spec.End()).Offset
			if spec.Comment != nil {
				end = fset.Position(spec.Comment.End()).Offset
			}
			cuts = append(cuts, cut{lineStart(start), lineEnd(end)})
		}
	}
	src := slices.Clone(text)
	slices.SortFunc(cuts, func(a, b cut) int { return b.start - a.start })
	for _, c := range cuts {
		src = append(src[:c.start:c.start], src[c.end:]...)
	}
	src = addImports(src, missing)
	if out, err := format.Source(src); err == nil {
		src = out
	}
	return minimalEdit(text, src)
}

// findImport returns the import path of the package with the given name
// that exports all the given names, preferring the standard library and
// shorter paths.
func (s *server) findImport(snap *snapshot, fi *fileInfo, name string, sels map[string]bool) string {
	var candidates []string
	for _, p := range s.importable(snap, fi) {
		if path.Base(p) == name {
			candidates = append(candidates, p)
		}
	}
	slices.SortStableFunc(candidates, func(a, b string) int {
		as, bs := !strings.Contains(strings.Split(a, "/")[0], "."), !strings.Contains(strings.Split(b, "/")[0], ".")
		if as != bs {
			if as {
				return -1
			}
			return 1
		}
		return len(a) - len(b)
	})
	for _, p := range candidates {
		pkg := s.loadTypes(fi, p)
		if pkg == nil {
			continue
		}
		ok := true
		for sel := range sels {
			if obj := pkg.Scope().Lookup(sel); obj == nil || !obj.Exported() {
				ok = false
				break
			}
		}
		if ok {
			return p
		}
	}
	return ""
}

// addImports adds imports of paths to the Go/G++ source src: into its
// first import block (standard library paths into its first group), or
// after its imports, or after the package clause.
func addImports(src []byte, paths []string) []byte {
	if len(paths) == 0 {
		return src
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ImportsOnly|parser.ParseComments)
	if err != nil && f == nil {
		return src
	}
	isStd := func(p string) bool { return !strings.Contains(strings.Split(p, "/")[0], ".") }
	insert := func(off int, s string) []byte {
		return slices.Concat(src[:off:off], []byte(s), src[off:])
	}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.IMPORT || !gd.Lparen.IsValid() || len(gd.Specs) == 0 {
			continue
		}
		// The end of the first group (specs on consecutive lines), or of
		// the whole block.
		firstEnd := gd.Specs[0].End()
		for i := 1; i < len(gd.Specs); i++ {
			if fset.Position(gd.Specs[i].Pos()).Line > fset.Position(gd.Specs[i-1].End()).Line+1 {
				break
			}
			firstEnd = gd.Specs[i].End()
		}
		firstStd := isStd(strings.Trim(gd.Specs[0].(*ast.ImportSpec).Path.Value, `"`))
		var std, other strings.Builder
		for _, p := range paths {
			if isStd(p) && firstStd {
				std.WriteString("\n\t" + strconv.Quote(p))
			} else {
				other.WriteString("\n\t" + strconv.Quote(p))
			}
		}
		lastEnd := gd.Specs[len(gd.Specs)-1].End()
		if other.Len() > 0 {
			src = insert(fset.Position(lastEnd).Offset, other.String())
		}
		if std.Len() > 0 {
			src = insert(fset.Position(firstEnd).Offset, std.String())
		}
		return src
	}
	var b strings.Builder
	if len(paths) == 1 {
		b.WriteString("\n\nimport " + strconv.Quote(paths[0]))
	} else {
		b.WriteString("\n\nimport (")
		for _, p := range paths {
			b.WriteString("\n\t" + strconv.Quote(p))
		}
		b.WriteString("\n)")
	}
	off := fset.Position(f.Name.End()).Offset
	for _, d := range f.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			off = fset.Position(gd.End()).Offset
		}
	}
	return insert(off, b.String())
}

// minimalEdit returns a single edit turning old into new, covering only
// the lines that differ.
func minimalEdit(old, new []byte) []TextEdit {
	if bytes.Equal(old, new) {
		return []TextEdit{}
	}
	// common prefix and suffix, in whole lines
	p := 0
	for p < len(old) && p < len(new) && old[p] == new[p] {
		p++
	}
	p = bytes.LastIndexByte(old[:p], '\n') + 1
	s := 0
	for s < len(old)-p && s < len(new)-p && old[len(old)-1-s] == new[len(new)-1-s] {
		s++
	}
	if i := bytes.IndexByte(old[len(old)-s:], '\n'); i >= 0 {
		s -= i + 1
	} else {
		s = 0
	}
	start := positionOf(old, p)
	end := positionOf(old, len(old)-s)
	return []TextEdit{{Range: Range{Start: start, End: end}, NewText: string(new[p : len(new)-s])}}
}

// positionOf returns the LSP position of a byte offset.
func positionOf(text []byte, off int) Position {
	line := bytes.Count(text[:off], []byte("\n"))
	ls := bytes.LastIndexByte(text[:off], '\n') + 1
	return Position{Line: line, Character: utf16Len(text[ls:off])}
}
