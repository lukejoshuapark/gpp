package lsp

import (
	"fmt"
	"go/token"
	"path"
	"strconv"
	"strings"

	"github.com/lukejoshuapark/gpp/src/toolchain/ast"
)

// importEdits returns the edits adding an import of importPath to the
// file: into its import block, turning a single import into a block, or
// after the package clause.
func importEdits(fi *fileInfo, text []byte, importPath string) []TextEdit {
	fset := fi.prog.Fset
	quoted := strconv.Quote(importPath)
	var decls []*ast.GenDecl
	for _, d := range fi.file.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			decls = append(decls, gd)
		}
	}
	if len(decls) == 0 {
		end := posRange(fset, text, fi.file.Name.End(), fi.file.Name.End()).End
		return []TextEdit{{Range: Range{Start: end, End: end}, NewText: "\n\nimport " + quoted}}
	}
	for _, gd := range decls {
		if !gd.Lparen.IsValid() {
			continue
		}
		// Insert in order, before the first spec that sorts after it.
		for _, spec := range gd.Specs {
			is := spec.(*ast.ImportSpec)
			if p, err := strconv.Unquote(is.Path.Value); err == nil && p > importPath {
				at := posRange(fset, text, is.Pos(), is.Pos()).Start
				return []TextEdit{{Range: Range{Start: at, End: at}, NewText: quoted + "\n\t"}}
			}
		}
		rp := posRange(fset, text, gd.Rparen, gd.Rparen).Start
		at := Position{Line: rp.Line, Character: 0}
		if rp.Character > 0 && strings.TrimSpace(string(lineAt(text, rp.Line+1)[:min(len(lineAt(text, rp.Line+1)), rp.Character)])) != "" {
			// ) is not on a line of its own
			return []TextEdit{{Range: Range{Start: rp, End: rp}, NewText: "; " + quoted}}
		}
		return []TextEdit{{Range: Range{Start: at, End: at}, NewText: "\t" + quoted + "\n"}}
	}
	// import "x" -> import ( "x"; "new" )
	gd := decls[0]
	spec := gd.Specs[0].(*ast.ImportSpec)
	start, end := fset.PositionFor(spec.Pos(), false).Offset, fset.PositionFor(spec.End(), false).Offset
	old := string(text[start:end])
	lines := []string{old, quoted}
	if p, err := strconv.Unquote(spec.Path.Value); err == nil && p > importPath {
		lines = []string{quoted, old}
	}
	return []TextEdit{{
		Range:   posRange(fset, text, gd.Pos(), gd.End()),
		NewText: "import (\n\t" + strings.Join(lines, "\n\t") + "\n)",
	}}
}

// codeActions offers to import packages named in "undefined: name" errors.
func (s *server) codeActions(p CodeActionParams) []CodeAction {
	actions := []CodeAction{}
	snap, fi, text := s.file(p.TextDocument.URI)
	if fi == nil {
		return actions
	}
	wants := func(kind string) bool {
		if len(p.Context.Only) == 0 {
			return true
		}
		for _, k := range p.Context.Only {
			if kind == k || strings.HasPrefix(kind, k+".") {
				return true
			}
		}
		return false
	}
	if wants("source.organizeImports") {
		if edits := s.organizeImports(snap, fi, text); len(edits) > 0 {
			actions = append(actions, CodeAction{
				Title: "Organize imports",
				Kind:  "source.organizeImports",
				Edit:  &WorkspaceEdit{Changes: map[string][]TextEdit{p.TextDocument.URI: edits}},
			})
		}
	}
	if !wants("quickfix") {
		return actions
	}
	for _, d := range p.Context.Diagnostics {
		name, ok := strings.CutPrefix(d.Message, "undefined: ")
		if !ok || strings.Contains(name, ".") {
			continue
		}
		for _, ip := range s.importable(snap, fi) {
			if path.Base(ip) != name {
				continue
			}
			actions = append(actions, CodeAction{
				Title:       fmt.Sprintf("Add import %q", ip),
				Kind:        "quickfix",
				Diagnostics: []Diagnostic{d},
				IsPreferred: !strings.Contains(ip, "/"),
				Edit:        &WorkspaceEdit{Changes: map[string][]TextEdit{p.TextDocument.URI: importEdits(fi, text, ip)}},
			})
		}
	}
	return actions
}
