package lsp

import (
	"path/filepath"
	"testing"
)

// The outline must stay valid while code is incomplete: VS Code rejects a
// symbol whose range does not contain its name (seen with unclosed braces).
func TestSymbolRangesWhileTyping(t *testing.T) {
	src := "package main\n\ntype Shape interface {\n    Area() float64\n}\n\ntype Circle struct {\n    radius float64\n}\n\nfunc NewCircle(radius float64) *Circle => &Circle{\n    radius,\n}\n\nfunc (c *Circle) Area() float64 => 3.14 * c.radius * c.radius\n\nfunc main() {\n\n}\n"
	dir := workspace(t, map[string]string{"main.gpp": src[:14]})
	c := startServer(t)
	uri := c.open(filepath.Join(dir, "main.gpp"), src[:14])
	before := func(a, b Position) bool { return a.Line < b.Line || a.Line == b.Line && a.Character <= b.Character }
	for n := 14; n <= len(src); n++ {
		c.change(uri, n, src[:n])
		var syms []DocumentSymbol
		c.call("textDocument/documentSymbol", DocumentSymbolParams{TextDocument: TextDocumentIdentifier{URI: uri}}, &syms)
		var check func([]DocumentSymbol)
		check = func(list []DocumentSymbol) {
			for _, s := range list {
				if !before(s.Range.Start, s.SelectionRange.Start) || !before(s.SelectionRange.End, s.Range.End) {
					t.Errorf("after %q: %s: selection %+v not in range %+v", src[n-10:n], s.Name, s.SelectionRange, s.Range)
				}
				check(s.Children)
			}
		}
		check(syms)
	}
}
