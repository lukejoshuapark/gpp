package lsp

import (
	"fmt"
	"go/token"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/lukejoshuapark/gpp/src/toolchain/ast"
	"github.com/lukejoshuapark/gpp/src/toolchain/driver"
	"github.com/lukejoshuapark/gpp/src/toolchain/types"
)

var keywords = []string{
	"break", "case", "chan", "const", "continue", "default", "defer", "else",
	"fallthrough", "for", "func", "go", "goto", "if", "import", "interface",
	"map", "package", "range", "return", "select", "struct", "switch", "type", "var",
}

func (s *server) complete(p CompletionParams) *CompletionList {
	list := &CompletionList{Items: []CompletionItem{}}
	snap, fi, text := s.file(p.TextDocument.URI)
	if fi == nil {
		return list
	}
	off := offsetOf(text, p.Position)
	start := off
	for start > 0 {
		r, size := utf8.DecodeLastRune(text[:start])
		if !isWordRune(r) {
			break
		}
		start -= size
	}
	prefix := string(text[start:off])
	pos := fi.tf.Pos(off)
	if inCommentOrString(fi.file, pos) {
		return list
	}

	if start > 0 && text[start-1] == '.' {
		list.Items = s.memberItems(snap, fi, text, fi.tf.Pos(start-1))
		return list
	}
	list.Items = s.scopeItems(snap, fi, text, pos, prefix)
	return list
}

// inCommentOrString reports whether pos is inside a comment or a string
// or character literal.
func inCommentOrString(f *ast.File, pos token.Pos) bool {
	for _, g := range f.Comments {
		if g.Pos() < pos && pos <= g.End() {
			// a line comment extends to the end of its line
			return true
		}
	}
	for _, n := range pathTo(f, pos) {
		if lit, ok := n.(*ast.BasicLit); ok && (lit.Kind == token.STRING || lit.Kind == token.CHAR) && lit.Pos() < pos && pos < lit.End() {
			return true
		}
	}
	return false
}

// memberItems completes after x. (dot is the position of the '.').
func (s *server) memberItems(snap *snapshot, fi *fileInfo, text []byte, dot token.Pos) []CompletionItem {
	var x ast.Expr
	ast.Inspect(fi.file, func(n ast.Node) bool {
		if x != nil || n == nil || n.Pos() > dot || n.End() < dot {
			return false
		}
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.X.End() == dot {
			x = sel.X
			return false
		}
		return true
	})
	if x == nil {
		return []CompletionItem{}
	}
	info := fi.pkg.Info
	if id, ok := x.(*ast.Ident); ok {
		switch obj := info.Uses[id].(type) {
		case *types.PkgName:
			return s.packageItems(snap, fi, obj.Imported(), nil)
		case nil:
			if info.Defs[id] == nil {
				// maybe a package that is not imported yet
				return s.unimportedMembers(snap, fi, text, id.Name)
			}
		}
	}
	tv, ok := info.Types[x]
	if !ok || tv.Type == nil {
		return []CompletionItem{}
	}
	if tv.IsType() {
		return methodExprItems(fi, tv.Type)
	}
	return memberItemsOf(snap, s, fi, tv.Type)
}

// memberItemsOf lists the fields and methods of values of type t.
func memberItemsOf(snap *snapshot, s *server, fi *fileInfo, t types.Type) []CompletionItem {
	q := qualifier(fi.pkg.Types)
	visible := func(obj types.Object) bool { return obj.Exported() || obj.Pkg() == fi.pkg.Types }
	var items []CompletionItem
	seen := map[string]bool{}

	// fields, shallowest first
	type entry struct {
		t     types.Type
		depth int
	}
	queue := []entry{{t, 0}}
	visited := map[types.Type]bool{}
	for len(queue) > 0 {
		e := queue[0]
		queue = queue[1:]
		u := types.Unalias(e.t)
		if p, ok := u.Underlying().(*types.Pointer); ok {
			u = p.Elem()
		}
		if visited[u] {
			continue
		}
		visited[u] = true
		st, ok := u.Underlying().(*types.Struct)
		if !ok {
			continue
		}
		for i := range st.NumFields() {
			f := st.Field(i)
			if types.EnumOf(u) != nil {
				break // an enum's fields are internal
			}
			if !seen[f.Name()] && visible(f) {
				seen[f.Name()] = true
				items = append(items, CompletionItem{
					Label: f.Name(), Kind: CompletionField, Detail: types.TypeString(f.Type(), q),
					SortText: fmt.Sprintf("%d%s", e.depth, f.Name()), Documentation: docContent(s.docOf(snap, f)),
				})
			}
			if f.Embedded() {
				queue = append(queue, entry{f.Type(), e.depth + 1})
			}
		}
	}

	// methods
	mt := t
	if _, isPtr := t.Underlying().(*types.Pointer); !isPtr && !types.IsInterface(t) {
		mt = types.NewPointer(t)
	}
	ms := types.NewMethodSet(mt)
	for i := range ms.Len() {
		obj := ms.At(i).Obj()
		if seen[obj.Name()] || !visible(obj) {
			continue
		}
		seen[obj.Name()] = true
		items = append(items, CompletionItem{
			Label: obj.Name(), Kind: CompletionMethod,
			Detail:        strings.TrimPrefix(types.TypeString(obj.Type(), q), "func"),
			SortText:      fmt.Sprintf("%d%s", len(ms.At(i).Index())-1, obj.Name()),
			Documentation: docContent(s.docOf(snap, obj)),
		})
	}
	if items == nil {
		items = []CompletionItem{}
	}
	return items
}

// methodExprItems lists the methods of a type, for method expressions
// (T.Method).
func methodExprItems(fi *fileInfo, t types.Type) []CompletionItem {
	q := qualifier(fi.pkg.Types)
	items := []CompletionItem{}
	if en := types.EnumOf(t); en != nil {
		for i := range en.Len() {
			v := en.Variant(i)
			item := CompletionItem{Label: v.Name(), Kind: CompletionEnumMember, SortText: fmt.Sprintf("0%03d", i)}
			item.Detail = variantValues(v, t, q)
			items = append(items, item)
		}
	}
	ms := types.NewMethodSet(t)
	for i := range ms.Len() {
		obj := ms.At(i).Obj()
		if obj.Exported() || obj.Pkg() == fi.pkg.Types {
			items = append(items, CompletionItem{Label: obj.Name(), Kind: CompletionMethod, Detail: types.TypeString(obj.Type(), q)})
		}
	}
	return items
}

// packageItems lists the exported members of pkg. edits, if any, are
// added to each item (to import the package).
func (s *server) packageItems(snap *snapshot, fi *fileInfo, pkg *types.Package, edits []TextEdit) []CompletionItem {
	items := []CompletionItem{}
	q := qualifier(pkg)
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		item := objectItem(obj, q)
		item.AdditionalTextEdits = edits
		item.Documentation = docContent(s.docOf(snap, obj))
		items = append(items, item)
	}
	return items
}

func objectItem(obj types.Object, q types.Qualifier) CompletionItem {
	item := CompletionItem{Label: obj.Name()}
	switch obj := obj.(type) {
	case *types.Func:
		item.Kind = CompletionFunction
		item.Detail = strings.TrimPrefix(types.TypeString(obj.Type(), q), "func")
	case *types.Var:
		item.Kind = CompletionVariable
		item.Detail = types.TypeString(obj.Type(), q)
	case *types.Const:
		item.Kind = CompletionConstant
		item.Detail = types.TypeString(obj.Type(), q)
		if obj.Val() != nil && len(obj.Val().ExactString()) < 30 {
			item.Detail += " = " + obj.Val().ExactString()
		}
	case *types.TypeName:
		item.Kind = CompletionClass
		switch obj.Type().Underlying().(type) {
		case *types.Struct:
			item.Kind = CompletionStruct
		case *types.Interface:
			item.Kind = CompletionInterface
		}
		if _, ok := obj.Type().(*types.TypeParam); ok {
			item.Kind = CompletionTypeParameter
		}
		item.Detail = "type"
	case *types.PkgName:
		item.Kind = CompletionModule
		item.Detail = fmt.Sprintf("%q", obj.Imported().Path())
	case *types.Builtin:
		item.Kind = CompletionFunction
		item.Detail = "builtin"
	case *types.Nil:
		item.Kind = CompletionValue
	}
	return item
}

func docContent(doc string) *MarkupContent {
	if doc == "" {
		return nil
	}
	return &MarkupContent{Kind: "markdown", Value: doc}
}

// scopeItems completes a name at pos: everything in scope, keywords, and
// packages that could be imported.
func (s *server) scopeItems(snap *snapshot, fi *fileInfo, text []byte, pos token.Pos, prefix string) []CompletionItem {
	items := []CompletionItem{}
	seen := map[string]bool{}
	q := qualifier(fi.pkg.Types)
	pkgScope := fi.pkg.Types.Scope()
	scope := pkgScope.Innermost(pos)
	if scope == nil {
		scope = pkgScope
	}
	depth := 0
	for sc := scope; sc != nil; sc = sc.Parent() {
		local := sc != pkgScope && sc != types.Universe && sc.Parent() != types.Universe
		for _, name := range sc.Names() {
			obj := sc.Lookup(name)
			if seen[name] || name == "_" {
				continue
			}
			if local && obj.Pos() > pos {
				continue // declared later
			}
			seen[name] = true
			item := objectItem(obj, q)
			item.SortText = fmt.Sprintf("%02d%s", depth, name)
			if sc != types.Universe && depth < 3 {
				item.Documentation = docContent(s.docOf(snap, obj))
			}
			items = append(items, item)
		}
		depth++
	}
	if prefix == "" {
		return items
	}
	for _, kw := range keywords {
		if strings.HasPrefix(kw, prefix) {
			items = append(items, CompletionItem{Label: kw, Kind: CompletionKeyword, SortText: "50" + kw})
		}
	}
	// packages that are not imported yet
	imported := map[string]bool{}
	for _, imp := range fi.file.Imports {
		imported[strings.Trim(imp.Path.Value, `"`)] = true
	}
	for _, p := range s.importable(snap, fi) {
		name := path.Base(p)
		if seen[name] || imported[p] || !strings.HasPrefix(name, prefix) {
			continue
		}
		items = append(items, CompletionItem{
			Label: name, Kind: CompletionModule, Detail: fmt.Sprintf("%q (import)", p),
			LabelDetails:        &CompletionItemLabelDetails{Description: p},
			SortText:            "60" + name,
			AdditionalTextEdits: importEdits(fi, text, p),
		})
	}
	return items
}

// importable returns the import paths that could be added to the file:
// the standard library and the packages of the loaded modules.
func (s *server) importable(snap *snapshot, fi *fileInfo) []string {
	paths := slices.Clone(s.stdPackages())
	if fi.pkg.Module != nil {
		for _, p := range fi.prog.Order {
			if p.Module != nil && p.Module.Main && p.Name != "main" && p.PkgPath != fi.pkg.PkgPath && !strings.Contains(p.ID, " [") && !slices.Contains(paths, p.PkgPath) {
				paths = append(paths, p.PkgPath)
			}
		}
	}
	return paths
}

// unimportedMembers completes name.X where name is a package that is not
// imported yet: accepting an item adds the import.
func (s *server) unimportedMembers(snap *snapshot, fi *fileInfo, text []byte, name string) []CompletionItem {
	for _, p := range s.importable(snap, fi) {
		if path.Base(p) != name {
			continue
		}
		pkg := s.loadTypes(fi, p)
		if pkg == nil {
			continue
		}
		return s.packageItems(snap, fi, pkg, importEdits(fi, text, p))
	}
	return []CompletionItem{}
}

// loadTypes type-checks the package with the given import path (in the
// module of fi), reusing the cache.
func (s *server) loadTypes(fi *fileInfo, importPath string) *types.Package {
	for _, p := range fi.prog.Order {
		if p.PkgPath == importPath && p.Types != nil {
			return p.Types
		}
	}
	dir := fi.pkg.Dir
	if fi.pkg.Module != nil && fi.pkg.Module.Dir != "" {
		dir = fi.pkg.Module.Dir
	}
	prog, err := driver.Load(&driver.Config{Dir: dir, Cache: s.cache}, importPath)
	if err != nil {
		return nil
	}
	if p := prog.Packages[importPath]; p != nil {
		return p.Types
	}
	return nil
}
