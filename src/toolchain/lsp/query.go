package lsp

import (
	"bytes"
	"fmt"
	"go/doc/comment"
	"go/token"
	"strings"
	"unicode/utf16"

	"github.com/lukejoshuapark/gpp/src/toolchain/ast"
	"github.com/lukejoshuapark/gpp/src/toolchain/format"
	"github.com/lukejoshuapark/gpp/src/toolchain/types"
)

// qualifier writes package-qualified names relative to pkg.
func qualifier(pkg *types.Package) types.Qualifier {
	return func(p *types.Package) string {
		if p == pkg {
			return ""
		}
		return p.Name()
	}
}

// objectAt returns the identifier at the position and the object it
// denotes.
func (s *server) objectAt(uri string, p Position) (*snapshot, *fileInfo, []byte, *ast.Ident, types.Object) {
	snap, fi, text := s.file(uri)
	if fi == nil {
		return snap, nil, text, nil, nil
	}
	pos := fi.tf.Pos(offsetOf(text, p))
	id := identAt(fi.file, pos)
	if id == nil {
		return snap, fi, text, nil, nil
	}
	info := fi.pkg.Info
	obj := info.Uses[id]
	if obj == nil {
		obj = info.Defs[id]
	}
	return snap, fi, text, id, obj
}

func (s *server) hover(p TextDocumentPositionParams) *Hover {
	snap, fi, text, id, obj := s.objectAt(p.TextDocument.URI, p.Position)
	if fi == nil {
		return nil
	}
	if obj == nil {
		return s.exprHover(snap, fi, text, p.Position)
	}
	decl := describe(obj, fi.pkg.Types)
	doc := s.docOf(snap, obj)
	r := posRange(fi.prog.Fset, text, id.Pos(), id.End())
	return &Hover{Contents: MarkupContent{Kind: "markdown", Value: hoverText(decl, doc)}, Range: &r}
}

// exprHover describes the G++ expression at the position (the ? of a
// ternary or error propagation, or a lambda's arrow).
func (s *server) exprHover(snap *snapshot, fi *fileInfo, text []byte, p Position) *Hover {
	pos := fi.tf.Pos(offsetOf(text, p))
	for _, n := range pathTo(fi.file, pos) {
		var what string
		var x ast.Expr
		switch n := n.(type) {
		case *ast.TryExpr:
			if pos >= n.Question && pos <= n.Question+1 {
				what, x = "error propagation: returns early if the error is not nil", n
			}
		case *ast.TernaryExpr:
			if pos >= n.Question && pos <= n.Question+1 || pos >= n.Colon && pos <= n.Colon+1 {
				what, x = "ternary expression", n
			}
		case *ast.LambdaExpr:
			if pos >= n.Arrow && pos <= n.Arrow+2 {
				what, x = "lambda", n
			}
		}
		if x == nil {
			continue
		}
		tv, ok := fi.pkg.Info.Types[x]
		if !ok {
			return nil
		}
		typ := "(no value)"
		if tv.Type != nil {
			typ = types.TypeString(tv.Type, qualifier(fi.pkg.Types))
		}
		return &Hover{Contents: MarkupContent{Kind: "markdown", Value: fmt.Sprintf("```gpp\n%s\n```\n%s", typ, what)}}
	}
	return nil
}

func hoverText(decl, doc string) string {
	s := "```gpp\n" + decl + "\n```"
	if doc != "" {
		s += "\n\n---\n\n" + doc
	}
	return s
}

// describe returns a declaration-like description of obj.
func describe(obj types.Object, pkg *types.Package) string {
	q := qualifier(pkg)
	switch obj := obj.(type) {
	case *types.PkgName:
		return fmt.Sprintf("package %s (%q)", obj.Imported().Name(), obj.Imported().Path())
	case *types.TypeName:
		if obj.IsAlias() {
			return fmt.Sprintf("type %s = %s", obj.Name(), types.TypeString(types.Unalias(obj.Type()), q))
		}
		under := obj.Type().Underlying()
		var tparams string
		if n, ok := obj.Type().(*types.Named); ok && n.TypeParams().Len() > 0 {
			var b strings.Builder
			b.WriteByte('[')
			for i := range n.TypeParams().Len() {
				tp := n.TypeParams().At(i)
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(tp.Obj().Name() + " " + types.TypeString(tp.Constraint(), q))
			}
			b.WriteByte(']')
			tparams = b.String()
		}
		if _, isParam := obj.Type().(*types.TypeParam); isParam {
			return fmt.Sprintf("type parameter %s %s", obj.Name(), types.TypeString(under, q))
		}
		body := typeBody(under, q)
		if en := types.EnumOf(obj.Type()); en != nil {
			body = enumBody(en, obj.Type(), q)
		}
		s := fmt.Sprintf("type %s%s %s", obj.Name(), tparams, body)
		if n, ok := obj.Type().(*types.Named); ok && n.NumMethods() > 0 {
			if _, isIface := under.(*types.Interface); !isIface {
				s += "\n"
				for i := range n.NumMethods() {
					m := n.Method(i)
					if m.Exported() || m.Pkg() == pkg {
						s += "\n" + types.ObjectString(m, q)
					}
				}
			}
		}
		return s
	case *types.Func:
		sig := obj.Signature()
		s := "func "
		if recv := sig.Recv(); recv != nil {
			name := recv.Name()
			if name == "" || name == "_" {
				s += "(" + types.TypeString(recv.Type(), q) + ") "
			} else {
				s += "(" + name + " " + types.TypeString(recv.Type(), q) + ") "
			}
		} else if obj.Pkg() != nil && obj.Pkg() != pkg {
			s += obj.Pkg().Name() + "."
		}
		return s + obj.Name() + strings.TrimPrefix(types.TypeString(sig, q), "func")
	case *types.Var:
		if obj.IsField() {
			return "field " + obj.Name() + " " + types.TypeString(obj.Type(), q)
		}
	}
	return types.ObjectString(obj, q)
}

// typeBody writes a type's definition across lines.
func typeBody(t types.Type, q types.Qualifier) string {
	switch t := t.(type) {
	case *types.Struct:
		if t.NumFields() == 0 {
			return "struct{}"
		}
		var b strings.Builder
		b.WriteString("struct {\n")
		for i := range t.NumFields() {
			f := t.Field(i)
			if f.Embedded() {
				fmt.Fprintf(&b, "\t%s", types.TypeString(f.Type(), q))
			} else {
				fmt.Fprintf(&b, "\t%s %s", f.Name(), types.TypeString(f.Type(), q))
			}
			if tag := t.Tag(i); tag != "" {
				fmt.Fprintf(&b, " `%s`", tag)
			}
			b.WriteByte('\n')
		}
		b.WriteByte('}')
		return b.String()
	case *types.Interface:
		if t.NumMethods() == 0 && t.NumEmbeddeds() == 0 {
			return "interface{}"
		}
		var b strings.Builder
		b.WriteString("interface {\n")
		for i := range t.NumEmbeddeds() {
			fmt.Fprintf(&b, "\t%s\n", types.TypeString(t.EmbeddedType(i), q))
		}
		for i := range t.NumExplicitMethods() {
			m := t.ExplicitMethod(i)
			sig := types.TypeString(m.Type(), q)
			fmt.Fprintf(&b, "\t%s%s\n", m.Name(), strings.TrimPrefix(sig, "func"))
		}
		b.WriteByte('}')
		return b.String()
	}
	return types.TypeString(t, q)
}

// docOf returns the documentation of obj's declaration, as markdown.
func (s *server) docOf(snap *snapshot, obj types.Object) string {
	if pn, ok := obj.(*types.PkgName); ok {
		return s.packageDoc(snap, pn.Imported())
	}
	if !obj.Pos().IsValid() {
		return ""
	}
	f := snap.astFile(obj.Pos())
	if f == nil {
		return ""
	}
	return markdown(declDoc(f, obj.Pos()))
}

// packageDoc returns the package comment of pkg.
func (s *server) packageDoc(snap *snapshot, pkg *types.Package) string {
	for _, prog := range snap.progs {
		for _, p := range prog.Order {
			if p.Types == pkg {
				for _, f := range p.Syntax {
					if f.Doc != nil {
						return markdown(f.Doc.Text())
					}
				}
			}
		}
	}
	return ""
}

// declDoc finds the documentation comment for the name declared at pos.
func declDoc(f *ast.File, pos token.Pos) string {
	var doc *ast.CommentGroup
	ast.Inspect(f, func(n ast.Node) bool {
		if doc != nil || n == nil || pos < n.Pos() || pos > n.End() {
			return false
		}
		switch n := n.(type) {
		case *ast.FuncDecl:
			if n.Name.Pos() == pos {
				doc = n.Doc
				return false
			}
		case *ast.GenDecl:
			for _, spec := range n.Specs {
				var names []*ast.Ident
				var sdoc, line *ast.CommentGroup
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					names, sdoc, line = []*ast.Ident{spec.Name}, spec.Doc, spec.Comment
				case *ast.ValueSpec:
					names, sdoc, line = spec.Names, spec.Doc, spec.Comment
				}
				for _, name := range names {
					if name.Pos() == pos {
						doc = firstDoc(sdoc, line)
						if doc == nil && len(n.Specs) == 1 {
							doc = n.Doc
						}
						return false
					}
				}
			}
		case *ast.Field:
			for _, name := range n.Names {
				if name.Pos() == pos {
					doc = firstDoc(n.Doc, n.Comment)
					return false
				}
			}
		}
		return true
	})
	if doc == nil {
		return ""
	}
	return doc.Text()
}

func firstDoc(docs ...*ast.CommentGroup) *ast.CommentGroup {
	for _, d := range docs {
		if d != nil {
			return d
		}
	}
	return nil
}

// markdown renders a Go doc comment as markdown.
func markdown(text string) string {
	if text == "" {
		return ""
	}
	var p comment.Parser
	var pr comment.Printer
	return strings.TrimSpace(string(pr.Markdown(p.Parse(text))))
}

func (s *server) definition(p TextDocumentPositionParams) *Location {
	snap, fi, _, _, obj := s.objectAt(p.TextDocument.URI, p.Position)
	if fi == nil || obj == nil || !obj.Pos().IsValid() {
		return nil
	}
	if pn, ok := obj.(*types.PkgName); ok {
		// the package's first file
		for _, prog := range snap.progs {
			for _, pkg := range prog.Order {
				if pkg.Types == pn.Imported() && len(pkg.Syntax) > 0 {
					return s.location(snap, pkg.Syntax[0].Name.Pos(), pkg.Syntax[0].Name.End())
				}
			}
		}
		return nil
	}
	return s.location(snap, obj.Pos(), obj.Pos()+token.Pos(len(obj.Name())))
}

func (s *server) location(snap *snapshot, start, end token.Pos) *Location {
	if snap.fset == nil {
		return nil
	}
	path := snap.fset.PositionFor(start, false).Filename
	return &Location{URI: pathToURI(path), Range: posRange(snap.fset, snap.text(path), start, end)}
}

// ---------------------------------------------------------------------------
// Outline

func (s *server) documentSymbols(p DocumentSymbolParams) []DocumentSymbol {
	snap, fi, text := s.file(p.TextDocument.URI)
	out := []DocumentSymbol{}
	if fi == nil {
		return out
	}
	fset := snap.fset
	sym := func(name, detail string, kind int, n ast.Node, id *ast.Ident) DocumentSymbol {
		// In incomplete code a node may lack an end (or even a start);
		// the range must still contain the name.
		start, end := n.Pos(), n.End()
		if !start.IsValid() || start > id.Pos() {
			start = id.Pos()
		}
		if !end.IsValid() || end < id.End() {
			end = id.End()
		}
		return DocumentSymbol{Name: name, Detail: detail, Kind: kind, Range: posRange(fset, text, start, end), SelectionRange: posRange(fset, text, id.Pos(), id.End())}
	}
	q := qualifier(fi.pkg.Types)
	for _, d := range fi.file.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			name, kind := d.Name.Name, SymbolFunction
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = SymbolMethod
				name = fmt.Sprintf("(%s) %s", recvString(d.Recv.List[0].Type), name)
			}
			detail := ""
			if obj := fi.pkg.Info.Defs[d.Name]; obj != nil {
				detail = strings.TrimPrefix(types.TypeString(obj.Type(), q), "func")
			}
			out = append(out, sym(name, detail, kind, d, d.Name))
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					kind := SymbolClass
					var children []DocumentSymbol
					switch t := spec.Type.(type) {
					case *ast.StructType:
						kind = SymbolStruct
						for _, f := range t.Fields.List {
							for _, n := range f.Names {
								children = append(children, sym(n.Name, typeStringOf(fi, f.Type), SymbolField, f, n))
							}
						}
					case *ast.InterfaceType:
						kind = SymbolInterface
						for _, f := range t.Methods.List {
							for _, n := range f.Names {
								children = append(children, sym(n.Name, "", SymbolMethod, f, n))
							}
						}
					case *ast.EnumType:
						kind = SymbolEnum
						for _, f := range t.Variants.List {
							detail := ""
							if f.Type != nil {
								detail = typeStringOf(fi, f.Type)
							}
							children = append(children, sym(f.Names[0].Name, detail, SymbolEnumMember, f, f.Names[0]))
						}
					}
					ds := sym(spec.Name.Name, "", kind, spec, spec.Name)
					ds.Children = children
					out = append(out, ds)
				case *ast.ValueSpec:
					kind := SymbolVariable
					if d.Tok == token.CONST {
						kind = SymbolConstant
					}
					for _, n := range spec.Names {
						detail := ""
						if obj := fi.pkg.Info.Defs[n]; obj != nil {
							detail = types.TypeString(obj.Type(), q)
						}
						out = append(out, sym(n.Name, detail, kind, spec, n))
					}
				}
			}
		}
	}
	return out
}

func recvString(x ast.Expr) string {
	switch x := x.(type) {
	case *ast.StarExpr:
		return "*" + recvString(x.X)
	case *ast.IndexExpr:
		return recvString(x.X)
	case *ast.IndexListExpr:
		return recvString(x.X)
	case *ast.Ident:
		return x.Name
	}
	return "?"
}

func typeStringOf(fi *fileInfo, x ast.Expr) string {
	if tv, ok := fi.pkg.Info.Types[x]; ok && tv.Type != nil {
		return types.TypeString(tv.Type, qualifier(fi.pkg.Types))
	}
	return ""
}

// ---------------------------------------------------------------------------
// Formatting

func (s *server) format(p DocumentFormattingParams) []TextEdit {
	path := uriToPath(p.TextDocument.URI)
	s.mu.Lock()
	d := s.docs[path]
	s.mu.Unlock()
	if d == nil {
		return nil
	}
	out, err := format.Source(d.text)
	if err != nil || bytes.Equal(out, d.text) {
		return []TextEdit{}
	}
	lines := bytes.Count(d.text, []byte("\n"))
	end := Position{Line: lines + 1, Character: 0}
	return []TextEdit{{Range: Range{Start: Position{}, End: end}, NewText: string(out)}}
}

// ---------------------------------------------------------------------------
// Signature help

func (s *server) signatureHelp(p TextDocumentPositionParams) *SignatureHelp {
	_, fi, text := s.file(p.TextDocument.URI)
	if fi == nil {
		return nil
	}
	pos := fi.tf.Pos(offsetOf(text, p.Position))
	var call *ast.CallExpr
	for _, n := range pathTo(fi.file, pos) {
		if c, ok := n.(*ast.CallExpr); ok && pos > c.Lparen && (pos <= c.Rparen || !c.Rparen.IsValid()) {
			call = c
			break
		}
		if _, ok := n.(*ast.LambdaExpr); ok {
			// inside a lambda body: only calls inside it
			if call == nil {
				continue
			}
		}
	}
	if call == nil {
		return nil
	}
	tv, ok := fi.pkg.Info.Types[call.Fun]
	if !ok || tv.IsType() {
		return nil
	}
	sig, ok := tv.Type.Underlying().(*types.Signature)
	if !ok {
		return nil
	}
	q := qualifier(fi.pkg.Types)
	name := types.ExprString(call.Fun)
	var label strings.Builder
	label.WriteString(name + "(")
	var params []ParameterInformation
	for i := range sig.Params().Len() {
		if i > 0 {
			label.WriteString(", ")
		}
		v := sig.Params().At(i)
		start := utf16.Encode([]rune(label.String()))
		ptype := types.TypeString(v.Type(), q)
		if sig.Variadic() && i == sig.Params().Len()-1 {
			ptype = "..." + strings.TrimPrefix(ptype, "[]")
		}
		part := ptype
		if v.Name() != "" && v.Name() != "_" {
			part = v.Name() + " " + ptype
		}
		label.WriteString(part)
		params = append(params, ParameterInformation{Label: [2]int{len(start), len(start) + len(utf16.Encode([]rune(part)))}})
	}
	label.WriteString(")")
	if res := sig.Results(); res.Len() > 0 {
		r := types.TypeString(res, q)
		if res.Len() == 1 && res.At(0).Name() == "" {
			r = strings.TrimSuffix(strings.TrimPrefix(r, "("), ")")
		}
		label.WriteString(" " + r)
	}
	active := 0
	for i, a := range call.Args {
		if pos > a.End() {
			active = i + 1
		}
	}
	if n := sig.Params().Len(); active >= n && n > 0 {
		if sig.Variadic() {
			active = n - 1
		}
	}
	info := SignatureInformation{Label: label.String(), Parameters: params}
	snap := s.current()
	if obj := calleeObject(fi, call.Fun); obj != nil {
		if doc := s.docOf(snap, obj); doc != "" {
			info.Documentation = &MarkupContent{Kind: "markdown", Value: doc}
		}
	}
	return &SignatureHelp{Signatures: []SignatureInformation{info}, ActiveParameter: active}
}

func calleeObject(fi *fileInfo, fun ast.Expr) types.Object {
	switch f := ast.Unparen(fun).(type) {
	case *ast.Ident:
		return fi.pkg.Info.Uses[f]
	case *ast.SelectorExpr:
		return fi.pkg.Info.Uses[f.Sel]
	case *ast.IndexExpr:
		return calleeObject(fi, f.X)
	case *ast.IndexListExpr:
		return calleeObject(fi, f.X)
	}
	return nil
}

// enumBody writes an enum type's variants.
func enumBody(en *types.Enum, t types.Type, q types.Qualifier) string {
	var b strings.Builder
	b.WriteString("enum {\n")
	for i := range en.Len() {
		v := en.Variant(i)
		b.WriteString("\t" + v.Name())
		if vt := v.ValueType(t); vt != nil {
			b.WriteString("(" + types.TypeString(vt, q) + ")")
		}
		b.WriteByte('\n')
	}
	b.WriteByte('}')
	return b.String()
}
