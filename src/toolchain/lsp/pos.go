package lsp

import (
	"bytes"
	"go/token"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"gpp/ast"
	"gpp/driver"
)

// Positions: Go positions are 1-based with byte columns; LSP positions are
// 0-based with UTF-16 columns.

func lineAt(text []byte, line int) []byte {
	for i := 1; i < line; i++ {
		j := bytes.IndexByte(text, '\n')
		if j < 0 {
			return nil
		}
		text = text[j+1:]
	}
	if j := bytes.IndexByte(text, '\n'); j >= 0 {
		text = text[:j]
	}
	return text
}

func utf16Len(b []byte) int {
	n := 0
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		n += len(utf16.Encode([]rune{r}))
		b = b[size:]
	}
	return n
}

// toLSP converts a line and byte column to an LSP position.
func toLSP(text []byte, line, col int) Position {
	l := lineAt(text, line)
	col = max(1, min(col, len(l)+1))
	return Position{Line: max(0, line-1), Character: utf16Len(l[:col-1])}
}

// offsetOf converts an LSP position to a byte offset in text.
func offsetOf(text []byte, p Position) int {
	off := 0
	for i := 0; i < p.Line; i++ {
		j := bytes.IndexByte(text[off:], '\n')
		if j < 0 {
			return len(text)
		}
		off += j + 1
	}
	units := 0
	for off < len(text) && text[off] != '\n' && units < p.Character {
		r, size := utf8.DecodeRune(text[off:])
		units += len(utf16.Encode([]rune{r}))
		off += size
	}
	return off
}

// posRange returns the LSP range of [start, end) in the file.
func posRange(fset *token.FileSet, text []byte, start, end token.Pos) Range {
	s, e := fset.PositionFor(start, false), fset.PositionFor(end, false)
	return Range{Start: toLSP(text, s.Line, s.Column), End: toLSP(text, e.Line, e.Column)}
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// diagRange returns the range of a diagnostic, widening an empty range to
// the word (or character) at its start.
func diagRange(text []byte, d driver.Diagnostic) Range {
	start := toLSP(text, d.Pos.Line, d.Pos.Column)
	if d.End.IsValid() && (d.End.Line > d.Pos.Line || d.End.Line == d.Pos.Line && d.End.Column > d.Pos.Column) {
		return Range{Start: start, End: toLSP(text, d.End.Line, d.End.Column)}
	}
	l := lineAt(text, d.Pos.Line)
	i := max(0, d.Pos.Column-1)
	j := i
	for j < len(l) {
		r, size := utf8.DecodeRune(l[j:])
		if !isWordRune(r) {
			break
		}
		j += size
	}
	if j == i && j < len(l) {
		_, size := utf8.DecodeRune(l[j:])
		j += size
	}
	return Range{Start: start, End: toLSP(text, d.Pos.Line, j+1)}
}

// pathTo returns the nodes enclosing pos, innermost first.
func pathTo(f *ast.File, pos token.Pos) []ast.Node {
	var path []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if n.Pos() <= pos && pos <= n.End() {
			path = append(path, n)
			return true
		}
		return false
	})
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

// identAt returns the identifier at (or just before) pos.
func identAt(f *ast.File, pos token.Pos) *ast.Ident {
	var found *ast.Ident
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil || found != nil {
			return false
		}
		if n.Pos() > pos || n.End() < pos {
			return false
		}
		if id, ok := n.(*ast.Ident); ok {
			found = id
			return false
		}
		return true
	})
	return found
}
