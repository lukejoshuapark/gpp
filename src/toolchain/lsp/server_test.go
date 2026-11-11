package lsp

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// client drives a server in-process over pipes, as an editor would.
type client struct {
	t      *testing.T
	conn   *conn
	nextID int
	msgs   chan *message
	diags  map[string][]Diagnostic
}

func startServer(t *testing.T) *client {
	cr, cw := io.Pipe() // client -> server
	sr, sw := io.Pipe() // server -> client
	go func() {
		if err := Serve(cr, sw); err != nil {
			t.Errorf("Serve: %v", err)
		}
		sw.Close()
	}()
	c := &client{t: t, conn: newConn(sr, cw), msgs: make(chan *message, 100), diags: map[string][]Diagnostic{}}
	go func() {
		for {
			m, err := c.conn.read()
			if err != nil {
				close(c.msgs)
				return
			}
			c.msgs <- m
		}
	}()
	t.Cleanup(func() {
		c.call("shutdown", nil, nil)
		c.notify("exit", nil)
		cw.Close()
	})
	c.call("initialize", map[string]any{}, nil)
	c.notify("initialized", map[string]any{})
	return c
}

func (c *client) notify(method string, params any) {
	c.t.Helper()
	if err := c.conn.notify(method, params); err != nil {
		c.t.Fatal(err)
	}
}

// next returns the next message from the server, recording diagnostics.
func (c *client) next() *message {
	c.t.Helper()
	select {
	case m, ok := <-c.msgs:
		if !ok {
			c.t.Fatal("server closed the connection")
		}
		if m.Method == "textDocument/publishDiagnostics" {
			var p PublishDiagnosticsParams
			if err := json.Unmarshal(m.Params, &p); err != nil {
				c.t.Fatal(err)
			}
			c.diags[p.URI] = p.Diagnostics
		}
		return m
	case <-time.After(30 * time.Second):
		c.t.Fatal("timed out waiting for the server")
		return nil
	}
}

func (c *client) call(method string, params, result any) {
	c.t.Helper()
	c.nextID++
	id := json.RawMessage(strings.TrimSpace(string(mustJSON(c.nextID))))
	raw, _ := json.Marshal(params)
	if err := c.conn.write(&message{ID: &id, Method: method, Params: raw}); err != nil {
		c.t.Fatal(err)
	}
	for {
		m := c.next()
		if m.ID == nil || string(*m.ID) != string(id) {
			continue
		}
		if m.Error != nil {
			c.t.Fatalf("%s: %s", method, m.Error.Message)
		}
		if result != nil {
			b, _ := json.Marshal(m.Result)
			if err := json.Unmarshal(b, result); err != nil {
				c.t.Fatalf("%s: decoding %s: %v", method, b, err)
			}
		}
		return
	}
}

// waitDiagnostics waits for diagnostics on uri, after an edit.
func (c *client) waitDiagnostics(uri string) []Diagnostic {
	c.t.Helper()
	delete(c.diags, uri)
	for {
		if d, ok := c.diags[uri]; ok {
			return d
		}
		c.next()
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// workspace writes a module with the given files and returns its directory.
func workspace(t *testing.T, files map[string]string) string {
	dir := t.TempDir()
	if _, ok := files["go.mod"]; !ok {
		files["go.mod"] = "module example.com/ws\n\ngo 1.27\n"
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func (c *client) open(path, text string) string {
	uri := pathToURI(path)
	c.notify("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: uri, LanguageID: "gpp", Version: 1, Text: text}})
	return uri
}

func (c *client) change(uri string, version int, text string) {
	c.notify("textDocument/didChange", DidChangeTextDocumentParams{
		TextDocument:   VersionedTextDocumentIdentifier{URI: uri, Version: version},
		ContentChanges: []TextDocumentContentChangeEvent{{Text: text}},
	})
}

// at returns the position of the first occurrence of marker in text, plus
// offset characters.
func at(t *testing.T, text, marker string, offset int) Position {
	t.Helper()
	i := strings.Index(text, marker)
	if i < 0 {
		t.Fatalf("marker %q not found", marker)
	}
	i += offset
	line := strings.Count(text[:i], "\n")
	col := i - (strings.LastIndex(text[:i], "\n") + 1)
	return Position{Line: line, Character: col}
}

const mainSrc = `package main

import (
	"fmt"
	"strconv"
)

// Point is a place on a grid.
type Point struct {
	// X is how far across.
	X, Y int
}

// Dist returns the Manhattan distance to the origin.
func (p Point) Dist() int {
	return abs(p.X) + abs(p.Y)
}

func abs(n int) int { return n < 0 ? -n : n }

func parse(s string) (int, error) {
	n := strconv.Atoi(s)?
	return n, nil
}

func main() {
	p := Point{X: 1, Y: -2}
	double := (x int) => x * 2
	_ = double
	fmt.Println(p.Dist(), strconv.Itoa(3))
	fmt.Println(parse("4"))
}
`

func TestDiagnostics(t *testing.T) {
	dir := workspace(t, map[string]string{"main.gpp": mainSrc})
	c := startServer(t)
	uri := c.open(filepath.Join(dir, "main.gpp"), mainSrc)
	if d := c.waitDiagnostics(uri); len(d) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", d)
	}
	broken := strings.Replace(mainSrc, `fmt.Println(parse("4"))`, `fmt.Println(parse(4))`, 1)
	c.change(uri, 2, broken)
	d := c.waitDiagnostics(uri)
	if len(d) != 1 || !strings.Contains(d[0].Message, "cannot use 4") {
		t.Fatalf("got %+v", d)
	}
	if d[0].Range.Start != at(t, broken, "parse(4)", 6) {
		t.Errorf("range %+v", d[0].Range)
	}
}

func TestHover(t *testing.T) {
	dir := workspace(t, map[string]string{"main.gpp": mainSrc})
	c := startServer(t)
	uri := c.open(filepath.Join(dir, "main.gpp"), mainSrc)
	hover := func(marker string, offset int) string {
		var h Hover
		c.call("textDocument/hover", TextDocumentPositionParams{TextDocument: TextDocumentIdentifier{URI: uri}, Position: at(t, mainSrc, marker, offset)}, &h)
		return h.Contents.Value
	}
	if h := hover("p.Dist()", 3); !strings.Contains(h, "func (p Point) Dist() int") || !strings.Contains(h, "Manhattan") {
		t.Errorf("method hover: %q", h)
	}
	if h := hover("Point{X", 1); !strings.Contains(h, "type Point struct") || !strings.Contains(h, "place on a grid") {
		t.Errorf("type hover: %q", h)
	}
	if h := hover("(x int) => x", 1); !strings.Contains(h, "var x int") {
		t.Errorf("lambda param hover: %q", h)
	}
	if h := hover("Atoi(s)?", 7); !strings.Contains(h, "error propagation") {
		t.Errorf("? hover: %q", h)
	}
	if h := hover("strconv.Itoa", 9); !strings.Contains(h, "func strconv.Itoa(i int) string") || !strings.Contains(h, "Itoa") {
		t.Errorf("stdlib hover: %q", h)
	}
}

func TestDefinition(t *testing.T) {
	dir := workspace(t, map[string]string{"main.gpp": mainSrc})
	c := startServer(t)
	uri := c.open(filepath.Join(dir, "main.gpp"), mainSrc)
	var loc Location
	c.call("textDocument/definition", TextDocumentPositionParams{TextDocument: TextDocumentIdentifier{URI: uri}, Position: at(t, mainSrc, "abs(p.X)", 0)}, &loc)
	if loc.URI != uri || loc.Range.Start != at(t, mainSrc, "abs(n int)", 0) {
		t.Errorf("got %+v", loc)
	}
	c.call("textDocument/definition", TextDocumentPositionParams{TextDocument: TextDocumentIdentifier{URI: uri}, Position: at(t, mainSrc, "Println", 0)}, &loc)
	if !strings.HasSuffix(loc.URI, "fmt/print.go") {
		t.Errorf("got %+v", loc)
	}
}

func completionLabels(items []CompletionItem) map[string]CompletionItem {
	m := map[string]CompletionItem{}
	for _, it := range items {
		m[it.Label] = it
	}
	return m
}

func TestCompletion(t *testing.T) {
	dir := workspace(t, map[string]string{"main.gpp": mainSrc})
	c := startServer(t)
	uri := c.open(filepath.Join(dir, "main.gpp"), mainSrc)
	version := 1
	complete := func(insert, marker string, offset int) map[string]CompletionItem {
		src := strings.Replace(mainSrc, "\t_ = double\n", "\t_ = double\n"+insert, 1)
		version++
		c.change(uri, version, src)
		var list CompletionList
		c.call("textDocument/completion", CompletionParams{TextDocument: TextDocumentIdentifier{URI: uri}, Position: at(t, src, marker, offset)}, &list)
		return completionLabels(list.Items)
	}
	members := complete("\tp.\n", "\tp.\n", 3)
	if _, ok := members["Dist"]; !ok {
		t.Errorf("no Dist in %v", members)
	}
	if _, ok := members["X"]; !ok {
		t.Errorf("no X in %v", members)
	}
	unimported := complete("\tstrings.\n", "strings.\n", 8)
	join, ok := unimported["Join"]
	if !ok || len(join.AdditionalTextEdits) != 1 || !strings.Contains(join.AdditionalTextEdits[0].NewText, `"strings"`) {
		t.Errorf("unimported member: %+v", join)
	}
	names := complete("\tfm\n", "\tfm\n", 3)
	for _, want := range []string{"fmt", "p", "double", "abs", "Point", "len"} {
		if _, ok := names[want]; !ok {
			t.Errorf("no %s in scope completion", want)
		}
	}
	pkgs := complete("\tbyt\n", "\tbyt\n", 4)
	if it, ok := pkgs["bytes"]; !ok || len(it.AdditionalTextEdits) == 0 {
		t.Errorf("no importable bytes package: %+v", it)
	}
}

func TestSignatureHelp(t *testing.T) {
	dir := workspace(t, map[string]string{"main.gpp": mainSrc})
	c := startServer(t)
	uri := c.open(filepath.Join(dir, "main.gpp"), mainSrc)
	var h SignatureHelp
	c.call("textDocument/signatureHelp", TextDocumentPositionParams{TextDocument: TextDocumentIdentifier{URI: uri}, Position: at(t, mainSrc, "Itoa(3)", 5)}, &h)
	if len(h.Signatures) != 1 || h.Signatures[0].Label != "strconv.Itoa(i int) string" {
		t.Errorf("got %+v", h)
	}
}

func TestFormattingAndSymbols(t *testing.T) {
	src := "package main\n\nfunc   main( ) {\n\tf := (a,b int)=>a+b\n\t_ = f\n}\n"
	dir := workspace(t, map[string]string{"main.gpp": src})
	c := startServer(t)
	uri := c.open(filepath.Join(dir, "main.gpp"), src)
	var edits []TextEdit
	c.call("textDocument/formatting", DocumentFormattingParams{TextDocument: TextDocumentIdentifier{URI: uri}}, &edits)
	if len(edits) != 1 || !strings.Contains(edits[0].NewText, "func main() {\n\tf := (a, b int) => a + b") {
		t.Errorf("got %+v", edits)
	}
	var syms []DocumentSymbol
	c.call("textDocument/documentSymbol", DocumentSymbolParams{TextDocument: TextDocumentIdentifier{URI: uri}}, &syms)
	if len(syms) != 1 || syms[0].Name != "main" {
		t.Errorf("got %+v", syms)
	}
}

func TestImportQuickFix(t *testing.T) {
	src := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(strings.ToUpper(\"x\"))\n}\n"
	dir := workspace(t, map[string]string{"main.gpp": src})
	c := startServer(t)
	uri := c.open(filepath.Join(dir, "main.gpp"), src)
	d := c.waitDiagnostics(uri)
	if len(d) != 1 {
		t.Fatalf("got %+v", d)
	}
	var actions []CodeAction
	c.call("textDocument/codeAction", CodeActionParams{TextDocument: TextDocumentIdentifier{URI: uri}, Range: d[0].Range, Context: CodeActionContext{Diagnostics: d}}, &actions)
	if len(actions) == 0 || actions[0].Title != `Add import "strings"` {
		t.Fatalf("got %+v", actions)
	}
	e := actions[0].Edit.Changes[uri][0]
	if e.NewText != "import (\n\t\"fmt\"\n\t\"strings\"\n)" {
		t.Errorf("edit %q", e.NewText)
	}
}

const enumSrc = `package main

// Shape is a shape.
type Shape enum {
	Empty
	// Circle has a radius.
	Circle(float64)
}

func main() {
	s := Shape.Circle(2)
	if s is Shape.Circle(r) && r > 1 {
		println(r)
	}
}
`

func TestEnums(t *testing.T) {
	dir := workspace(t, map[string]string{"main.gpp": enumSrc})
	c := startServer(t)
	uri := c.open(filepath.Join(dir, "main.gpp"), enumSrc)
	if d := c.waitDiagnostics(uri); len(d) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", d)
	}
	hover := func(marker string, offset int) string {
		var h Hover
		c.call("textDocument/hover", TextDocumentPositionParams{TextDocument: TextDocumentIdentifier{URI: uri}, Position: at(t, enumSrc, marker, offset)}, &h)
		return h.Contents.Value
	}
	if h := hover("Shape.Circle(2)", 7); !strings.Contains(h, "variant Shape.Circle(float64)") || !strings.Contains(h, "has a radius") {
		t.Errorf("variant hover: %q", h)
	}
	if h := hover("Shape.Circle(2)", 1); !strings.Contains(h, "type Shape enum {\n\tEmpty\n\tCircle(float64)\n}") || strings.Contains(h, "GPP_") {
		t.Errorf("enum hover: %q", h)
	}
	if h := hover("r > 1", 0); !strings.Contains(h, "var r float64") {
		t.Errorf("binding hover: %q", h)
	}

	src := strings.Replace(enumSrc, "s := Shape.Circle(2)", "s := Shape.Circle(2)\n\t_ = Shape.\n\t_ = s.", 1)
	c.change(uri, 2, src)
	var list CompletionList
	c.call("textDocument/completion", CompletionParams{TextDocument: TextDocumentIdentifier{URI: uri}, Position: at(t, src, "_ = Shape.", 10)}, &list)
	got := completionLabels(list.Items)
	if _, ok := got["Circle"]; !ok {
		t.Errorf("variant completion: %v", got)
	}
	if _, ok := got["Empty"]; !ok {
		t.Errorf("variant completion: %v", got)
	}
	c.call("textDocument/completion", CompletionParams{TextDocument: TextDocumentIdentifier{URI: uri}, Position: at(t, src, "_ = s.", 6)}, &list)
	got = completionLabels(list.Items)
	if _, ok := got["String"]; !ok || len(got) != 1 {
		t.Errorf("enum value completion: %v", got)
	}

	c.change(uri, 3, enumSrc)
	var syms []DocumentSymbol
	c.call("textDocument/documentSymbol", DocumentSymbolParams{TextDocument: TextDocumentIdentifier{URI: uri}}, &syms)
	if len(syms) < 1 || syms[0].Kind != SymbolEnum || len(syms[0].Children) != 2 || syms[0].Children[1].Name != "Circle" {
		t.Errorf("symbols: %+v", syms)
	}
}

func TestSemanticTokens(t *testing.T) {
	src := "package main\n\nimport \"fmt\"\n\ntype Shape enum {\n\tEmpty\n\tCircle(float64)\n}\n\ntype Point struct{ X int }\n\nfunc (p Point) Get(n int) int => p.X + n\n\nconst limit = 3\n\nfunc main() {\n\ts := Shape.Circle(2)\n\tq := Point{X: limit}\n\tfmt.Println(s, q.Get(len(\"ab\")))\n}\n"
	dir := workspace(t, map[string]string{"main.gpp": src})
	c := startServer(t)
	uri := c.open(filepath.Join(dir, "main.gpp"), src)
	var toks SemanticTokens
	c.call("textDocument/semanticTokens/full", SemanticTokensParams{TextDocument: TextDocumentIdentifier{URI: uri}}, &toks)
	lines := strings.Split(src, "\n")
	got := map[string]string{} // "line:text" -> type
	line, char := 0, 0
	for i := 0; i+4 < len(toks.Data)+1; i += 5 {
		d := toks.Data[i : i+5]
		if d[0] > 0 {
			char = 0
		}
		line += int(d[0])
		char += int(d[1])
		text := lines[line][char : char+int(d[2])]
		got[fmt.Sprintf("%d:%s", line+1, text)] = semanticTokenTypes[d[3]]
	}
	want := map[string]string{
		"5:Shape": "type", "6:Empty": "enumMember", "7:float64": "type",
		"10:Point": "type", "10:X": "property",
		"12:p": "parameter", "12:Point": "type", "12:Get": "method", "12:n": "parameter",
		"14:limit": "variable",
		"17:s":     "variable", "17:Shape": "type", "17:Circle": "enumMember",
		"18:Point": "type", "18:X": "property", "18:limit": "variable",
		"19:fmt": "namespace", "19:Println": "function", "19:len": "function",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
}
