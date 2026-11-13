// Package lsp implements the G++ language server.
//
// The server type-checks the packages of the open .gpp files with the same
// front end as the gpp command (package driver), using the editor's
// unsaved text, and answers queries from the resulting syntax trees and
// type information.
package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lukejoshuapark/gpp/src/toolchain/ast"
	"github.com/lukejoshuapark/gpp/src/toolchain/driver"
)

// Serve runs the language server over r and w until the client exits.
func Serve(r io.Reader, w io.Writer) error {
	s := &server{
		conn:      newConn(r, w),
		log:       log.New(os.Stderr, "gpp lsp: ", log.Ltime),
		docs:      map[string]*document{},
		cache:     driver.NewCache(),
		published: map[string]bool{},
	}
	return s.run()
}

type server struct {
	conn *conn
	log  *log.Logger

	mu       sync.Mutex
	docs     map[string]*document // open documents by path
	version  int                  // incremented on every document change
	shutdown bool
	timer    *time.Timer

	analysisMu sync.Mutex // serializes analysis; guards the fields below
	cache      *driver.Cache
	snap       *snapshot
	published  map[string]bool // paths with published diagnostics

	stdOnce sync.Once
	std     []string // standard library import paths
}

type document struct {
	uri     string
	version int
	text    []byte
}

// A snapshot is the result of analyzing the open documents at one version.
type snapshot struct {
	version int
	progs   []*driver.Program
	files   map[string]*fileInfo // by path: .gpp and .go files of loaded packages
	texts   map[string][]byte    // the open documents' text at this version
	fset    *token.FileSet

	astOnce sync.Once
	byFile  map[*token.File]*ast.File // every parsed file, for documentation
}

// fileInfo describes a file of a loaded package.
type fileInfo struct {
	path string
	pkg  *driver.Package
	prog *driver.Program
	file *ast.File
	tf   *token.File
}

func (s *server) run() error {
	for {
		m, err := s.conn.read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if m.Method == "exit" {
			if s.shutdown {
				return nil
			}
			return errors.New("exit before shutdown")
		}
		s.handle(m)
	}
}

func (s *server) handle(m *message) {
	result, rerr := s.dispatch(m)
	if m.ID == nil {
		if rerr != nil && rerr.Code != codeMethodNotFound {
			s.log.Printf("%s: %s", m.Method, rerr.Message)
		}
		return
	}
	if err := s.conn.reply(m.ID, result, rerr); err != nil {
		s.log.Printf("reply: %v", err)
	}
}

func (s *server) dispatch(m *message) (result any, rerr *rpcError) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Printf("panic in %s: %v\n%s", m.Method, r, debug.Stack())
			result, rerr = nil, &rpcError{Code: codeInternalError, Message: fmt.Sprint(r)}
		}
	}()
	decode := func(v any) *rpcError {
		if err := json.Unmarshal(m.Params, v); err != nil {
			return &rpcError{Code: codeInvalidParams, Message: err.Error()}
		}
		return nil
	}

	switch m.Method {
	case "initialize":
		go s.stdPackages() // warm up
		return InitializeResult{
			Capabilities: ServerCapabilities{
				TextDocumentSync:       TextDocumentSyncOptions{OpenClose: true, Change: 1},
				HoverProvider:          true,
				CompletionProvider:     &CompletionOptions{TriggerCharacters: []string{"."}},
				CodeActionProvider:     &CodeActionOptions{CodeActionKinds: []string{"quickfix", "source.organizeImports"}},
				SignatureHelpProvider:  &SignatureHelpOptions{TriggerCharacters: []string{"(", ","}, RetriggerCharacters: []string{")"}},
				DefinitionProvider:     true,
				DocumentSymbolProvider: true,
				FormattingProvider:     true,
				ReferencesProvider:     true,
				RenameProvider:         &RenameOptions{PrepareProvider: true},
				SemanticTokensProvider: &SemanticTokensOptions{
					Legend: SemanticTokensLegend{TokenTypes: semanticTokenTypes, TokenModifiers: semanticTokenModifiers},
					Full:   true,
				},
			},
			ServerInfo: ServerInfo{Name: "gpp", Version: "0.2.0"},
		}, nil
	case "initialized":
		return nil, nil
	case "shutdown":
		s.mu.Lock()
		s.shutdown = true
		s.mu.Unlock()
		return nil, nil

	case "textDocument/didOpen":
		var p DidOpenTextDocumentParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		s.setDoc(p.TextDocument.URI, p.TextDocument.Version, []byte(p.TextDocument.Text))
		return nil, nil
	case "textDocument/didChange":
		var p DidChangeTextDocumentParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		if n := len(p.ContentChanges); n > 0 {
			s.setDoc(p.TextDocument.URI, p.TextDocument.Version, []byte(p.ContentChanges[n-1].Text))
		}
		return nil, nil
	case "textDocument/didClose":
		var p DidCloseTextDocumentParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		s.closeDoc(p.TextDocument.URI)
		return nil, nil
	case "workspace/didChangeWatchedFiles", "textDocument/didSave":
		// Files on disk changed; open documents may depend on them.
		s.mu.Lock()
		s.version++
		s.scheduleLocked()
		s.mu.Unlock()
		return nil, nil
	case "$/cancelRequest", "$/setTrace", "workspace/didChangeConfiguration":
		return nil, nil

	case "textDocument/hover":
		var p TextDocumentPositionParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		if h := s.hover(p); h != nil {
			return h, nil
		}
		return nil, nil
	case "textDocument/definition":
		var p TextDocumentPositionParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		if l := s.definition(p); l != nil {
			return l, nil
		}
		return nil, nil
	case "textDocument/completion":
		var p CompletionParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return s.complete(p), nil
	case "textDocument/codeAction":
		var p CodeActionParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return s.codeActions(p), nil
	case "textDocument/signatureHelp":
		var p TextDocumentPositionParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		if h := s.signatureHelp(p); h != nil {
			return h, nil
		}
		return nil, nil
	case "textDocument/documentSymbol":
		var p DocumentSymbolParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return s.documentSymbols(p), nil
	case "textDocument/references":
		var p ReferenceParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return s.findReferences(p), nil
	case "textDocument/prepareRename":
		var p TextDocumentPositionParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		r, err := s.prepareRename(p)
		if err != nil {
			return nil, &rpcError{Code: codeRequestFailed, Message: err.Error()}
		}
		return r, nil
	case "textDocument/rename":
		var p RenameParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		edit, err := s.rename(p)
		if err != nil {
			return nil, &rpcError{Code: codeRequestFailed, Message: err.Error()}
		}
		return edit, nil
	case "textDocument/semanticTokens/full":
		var p SemanticTokensParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return s.semanticTokens(p), nil
	case "textDocument/formatting":
		var p DocumentFormattingParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return s.format(p), nil
	}
	return nil, &rpcError{Code: codeMethodNotFound, Message: "method not supported: " + m.Method}
}

// ---------------------------------------------------------------------------
// Documents and analysis

func uriToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return uri
	}
	return filepath.FromSlash(u.Path)
}

func pathToURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

func (s *server) setDoc(uri string, version int, text []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[uriToPath(uri)] = &document{uri: uri, version: version, text: text}
	s.version++
	s.scheduleLocked()
}

func (s *server) closeDoc(uri string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.docs, uriToPath(uri))
	s.version++
	s.scheduleLocked()
}

// scheduleLocked analyzes the documents shortly, after a pause in typing.
func (s *server) scheduleLocked() {
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(150*time.Millisecond, func() { s.current() })
}

// current returns a snapshot of the current documents, analyzing them if
// they changed since the last snapshot.
func (s *server) current() *snapshot {
	s.analysisMu.Lock()
	defer s.analysisMu.Unlock()
	s.mu.Lock()
	version := s.version
	texts := map[string][]byte{}
	for path, d := range s.docs {
		texts[path] = d.text
	}
	s.mu.Unlock()
	if s.snap != nil && s.snap.version == version {
		return s.snap
	}
	snap := s.analyze(version, texts)
	s.snap = snap
	return snap
}

// moduleRoot returns the directory of the go.mod governing dir, or "".
func moduleRoot(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

func (s *server) analyze(version int, texts map[string][]byte) *snapshot {
	snap := &snapshot{version: version, files: map[string]*fileInfo{}, texts: texts}
	diags := map[string][]driver.Diagnostic{}

	// Load the packages of the open .gpp files, one load per module.
	byModule := map[string][]string{}
	var noModule []string
	for path := range texts {
		if !strings.HasSuffix(path, ".gpp") {
			continue
		}
		dir := filepath.Dir(path)
		root := moduleRoot(dir)
		if root == "" {
			noModule = append(noModule, path)
			continue
		}
		if !slices.Contains(byModule[root], dir) {
			byModule[root] = append(byModule[root], dir)
		}
	}
	for _, path := range noModule {
		diags[path] = append(diags[path], driver.Diagnostic{
			Pos: token.Position{Filename: path, Line: 1, Column: 1},
			Msg: "no go.mod file found in this directory or any parent: run `gpp mod init <module>` to create one",
		})
	}

	for root, dirs := range byModule {
		slices.Sort(dirs)
		prog, err := driver.Load(&driver.Config{Dir: root, Overlay: texts, Tests: true, Cache: s.cache}, dirs...)
		if err != nil {
			for path := range texts {
				if within(path, root) {
					diags[path] = append(diags[path], driver.Diagnostic{Pos: token.Position{Filename: path, Line: 1, Column: 1}, Msg: err.Error()})
				}
			}
			continue
		}
		snap.fset = prog.Fset
		snap.progs = append(snap.progs, prog)
		for _, p := range prog.Order {
			for _, f := range p.Syntax {
				tf := prog.Fset.File(f.Pos())
				if tf == nil {
					continue
				}
				name := tf.Name()
				// Prefer the test variant of a package: it has every file.
				if old, ok := snap.files[name]; ok && (old.pkg.ForTest != "" || p.DepOnly) {
					continue
				}
				snap.files[name] = &fileInfo{path: name, pkg: p, prog: prog, file: f, tf: tf}
			}
		}
		for _, d := range prog.Diagnostics() {
			diags[d.Pos.Filename] = append(diags[d.Pos.Filename], d)
		}
		if len(prog.Errors) > 0 {
			for _, dir := range dirs {
				for path := range texts {
					if filepath.Dir(path) == dir {
						for _, e := range prog.Errors {
							diags[path] = append(diags[path], driver.Diagnostic{Pos: token.Position{Filename: path, Line: 1, Column: 1}, Msg: e})
						}
					}
				}
			}
		}
	}
	s.publish(snap, diags)
	return snap
}

// publish sends the diagnostics of .gpp files (gopls reports .go files).
func (s *server) publish(snap *snapshot, byFile map[string][]driver.Diagnostic) {
	next := map[string]bool{}
	for path := range snap.texts {
		if _, ok := byFile[path]; !ok {
			byFile[path] = nil // clear
		}
	}
	for path, list := range byFile {
		if !strings.HasSuffix(path, ".gpp") {
			continue
		}
		text := snap.text(path)
		out := []Diagnostic{}
		seen := map[string]bool{}
		for _, d := range list {
			key := fmt.Sprintf("%d:%d:%s", d.Pos.Line, d.Pos.Column, d.Msg)
			if seen[key] {
				continue
			}
			seen[key] = true
			sev := SeverityError
			out = append(out, Diagnostic{Range: diagRange(text, d), Severity: sev, Source: "gpp", Message: d.Msg})
		}
		s.conn.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{URI: pathToURI(path), Diagnostics: out})
		next[path] = true
	}
	for path := range s.published {
		if !next[path] {
			s.conn.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{URI: pathToURI(path), Diagnostics: []Diagnostic{}})
		}
	}
	s.published = next
}

// text returns the text of a file in the snapshot (its document, or the
// file on disk).
func (snap *snapshot) text(path string) []byte {
	if t, ok := snap.texts[path]; ok {
		return t
	}
	b, _ := os.ReadFile(path)
	return b
}

// file returns the file information and text for a document.
func (s *server) file(uri string) (*snapshot, *fileInfo, []byte) {
	snap := s.current()
	path := uriToPath(uri)
	fi := snap.files[path]
	return snap, fi, snap.text(path)
}

// astFile returns the syntax tree of the file containing pos, among all
// loaded packages (for documentation of declarations).
func (snap *snapshot) astFile(pos token.Pos) *ast.File {
	snap.astOnce.Do(func() {
		snap.byFile = map[*token.File]*ast.File{}
		for _, prog := range snap.progs {
			for _, p := range prog.Order {
				for _, f := range p.Syntax {
					if tf := prog.Fset.File(f.Pos()); tf != nil {
						snap.byFile[tf] = f
					}
				}
			}
		}
	})
	if snap.fset == nil {
		return nil
	}
	return snap.byFile[snap.fset.File(pos)]
}

// stdPackages returns the import paths of the standard library.
func (s *server) stdPackages() []string {
	s.stdOnce.Do(func() {
		out, err := exec.Command("go", "list", "std").Output()
		if err != nil {
			return
		}
		for _, p := range strings.Fields(string(out)) {
			if !strings.Contains(p, "internal") && !strings.HasPrefix(p, "vendor/") {
				s.std = append(s.std, p)
			}
		}
	})
	return s.std
}

func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}
