package lower

// Lowering of G++ enums. An enum is a struct with a tag and one field per
// variant value (see package types):
//
//	type Shape enum { Empty; Circle(float64) }
//	    type Shape struct { GPP_tag uint8; GPP_Circle float64 }
//	Shape.Circle(r)
//	    (Shape{GPP_tag: 1, GPP_Circle: r})
//	switch s { case Shape.Circle(r): ... }
//	    { __e := s; switch __e.GPP_tag { case 1: r := __e.GPP_Circle; ... } }
//	if s is Shape.Circle(r) && r > 1 { ... }
//	    { var r float64; if func() bool { e := s; if e.GPP_tag != 1 { return false };
//	          r = e.GPP_Circle; return true }() && r > 1 { ... } }
//
// plus a String method printing Empty or Circle(2), unless the enum
// declares one.

import (
	"bytes"
	"fmt"
	"go/token"
	"slices"
	"strconv"
	"strings"

	"github.com/lukejoshuapark/gpp/src/toolchain/ast"
	"github.com/lukejoshuapark/gpp/src/toolchain/printer"
	"github.com/lukejoshuapark/gpp/src/toolchain/types"
)

// enumDecl lowers the enum type declaration spec, and generates its
// methods (see enumMethods).
func (l *lowerer) enumDecl(spec *ast.TypeSpec, et *ast.EnumType) {
	pos := et.Enum
	obj, _ := l.info.Defs[spec.Name].(*types.TypeName)
	tagType := "uint8"
	if len(et.Variants.List) > 256 {
		tagType = "uint16"
	}
	payloads := map[string]ast.Expr{} // variant -> value type
	fields := &ast.FieldList{Opening: et.Variants.Opening, Closing: et.Variants.Closing}
	fields.List = append(fields.List, &ast.Field{
		Names: []*ast.Ident{{NamePos: pos, Name: types.EnumTagField}},
		Type:  &ast.Ident{NamePos: pos, Name: tagType},
	})
	for _, f := range et.Variants.List {
		if f.Type != nil {
			name := f.Names[0]
			payloads[name.Name] = f.Type
			fields.List = append(fields.List, &ast.Field{
				Doc:   f.Doc,
				Names: []*ast.Ident{{NamePos: name.Pos(), Name: "GPP_" + name.Name}},
				Type:  f.Type,
			})
		}
	}
	spec.Type = &ast.StructType{Struct: pos, Fields: fields}

	if obj == nil {
		return
	}
	if named, _ := obj.Type().(*types.Named); named != nil {
		l.enumMethods(spec, named, payloads)
	}
}

// enumMethods generates the methods of an enum (unless it declares them):
//
//   - String: Circle(2), or Empty for a variant without a value.
//   - For an enum without values, MarshalText and UnmarshalText: a variant
//     encodes as its name (or the name in its json tag), which also makes
//     the enum work as a JSON object key.
//   - For an enum with values, MarshalJSON and UnmarshalJSON: a variant
//     encodes as {"type": name} or {"type": name, "value": value}.
//
// The methods are written as Go source appended to the file.
func (l *lowerer) enumMethods(spec *ast.TypeSpec, named *types.Named, payloads map[string]ast.Expr) {
	en := types.EnumOf(named)
	name := spec.Name.Name
	typ := name // with type parameters: Option[T]
	if spec.TypeParams != nil {
		var tps []string
		for _, f := range spec.TypeParams.List {
			for _, n := range f.Names {
				tps = append(tps, n.Name)
			}
		}
		typ += "[" + strings.Join(tps, ", ") + "]"
	}
	tag := types.EnumTagField
	fmtPkg := func() string { return l.importName("fmt") }
	jsonPkg := func() string { return l.importName("encoding/json") }
	var want []string // "a", "b" or "c"
	for i := range en.Len() {
		want = append(want, strconv.Quote(en.Variant(i).JSONName()))
	}
	wantList := strings.Join(want, ", ")
	if len(want) > 1 {
		wantList = strings.Join(want[:len(want)-1], ", ") + " or " + want[len(want)-1]
	}
	construct := func(v *types.Variant, val string) string {
		elts := []string{}
		if v.Index() != 0 {
			elts = append(elts, fmt.Sprintf("%s: %d", tag, v.Index()))
		}
		if val != "" {
			elts = append(elts, fmt.Sprintf("%s: %s", v.FieldName(), val))
		}
		return typ + "{" + strings.Join(elts, ", ") + "}"
	}

	var b strings.Builder
	for _, m := range types.EnumGeneratedMethods(named) {
		switch m {
		case "String":
			fmt.Fprintf(&b, "\nfunc (e %s) String() string {\n\tswitch e.%s {\n", typ, tag)
			for i := range en.Len() {
				v := en.Variant(i)
				if v.HasValue() {
					fmt.Fprintf(&b, "\tcase %d:\n\t\treturn %s.Sprintf(%q, e.%s)\n", i, fmtPkg(), v.Name()+"(%v)", v.FieldName())
				} else {
					fmt.Fprintf(&b, "\tcase %d:\n\t\treturn %q\n", i, v.Name())
				}
			}
			fmt.Fprintf(&b, "\t}\n\treturn %s.Sprintf(%q, e.%s)\n}\n", fmtPkg(), name+"(%d)", tag)

		case "MarshalText":
			fmt.Fprintf(&b, "\nfunc (e %s) MarshalText() ([]byte, error) {\n\tswitch e.%s {\n", typ, tag)
			for i := range en.Len() {
				fmt.Fprintf(&b, "\tcase %d:\n\t\treturn []byte(%q), nil\n", i, en.Variant(i).JSONName())
			}
			fmt.Fprintf(&b, "\t}\n\treturn nil, %s.Errorf(%q, e.%s)\n}\n", fmtPkg(), "invalid "+name+" value %d", tag)

		case "UnmarshalText":
			fmt.Fprintf(&b, "\nfunc (e *%s) UnmarshalText(b []byte) error {\n\tswitch string(b) {\n", typ)
			for i := range en.Len() {
				v := en.Variant(i)
				fmt.Fprintf(&b, "\tcase %q:\n\t\t*e = %s\n", v.JSONName(), construct(v, ""))
			}
			fmt.Fprintf(&b, "\tdefault:\n\t\treturn %s.Errorf(%q, b)\n\t}\n\treturn nil\n}\n", fmtPkg(), "unknown "+name+" variant %q (want "+wantList+")")

		case "MarshalJSON":
			fmt.Fprintf(&b, "\nfunc (e %s) MarshalJSON() ([]byte, error) {\n\tswitch e.%s {\n", typ, tag)
			for i := range en.Len() {
				v := en.Variant(i)
				if v.HasValue() {
					fmt.Fprintf(&b, "\tcase %d:\n\t\treturn %s.Marshal(struct {\n\t\t\tType  string `json:\"type\"`\n\t\t\tValue %s `json:\"value\"`\n\t\t}{%q, e.%s})\n",
						i, jsonPkg(), l.source(payloads[v.Name()]), v.JSONName(), v.FieldName())
				} else {
					fmt.Fprintf(&b, "\tcase %d:\n\t\treturn %s.Marshal(struct {\n\t\t\tType string `json:\"type\"`\n\t\t}{%q})\n", i, jsonPkg(), v.JSONName())
				}
			}
			fmt.Fprintf(&b, "\t}\n\treturn nil, %s.Errorf(%q, e.%s)\n}\n", fmtPkg(), "invalid "+name+" value %d", tag)

		case "UnmarshalJSON":
			fmt.Fprintf(&b, "\nfunc (e *%s) UnmarshalJSON(b []byte) error {\n", typ)
			fmt.Fprintf(&b, "\tif string(b) == \"null\" {\n\t\treturn nil\n\t}\n")
			fmt.Fprintf(&b, "\tif len(b) == 0 || b[0] != '{' {\n\t\treturn %s.Errorf(%q, b)\n\t}\n", fmtPkg(), "decoding "+name+`: want an object like {"type": ...}, not %s`)
			fmt.Fprintf(&b, "\tvar v struct {\n\t\tType  *string `json:\"type\"`\n\t\tValue %s.RawMessage `json:\"value\"`\n\t}\n", jsonPkg())
			fmt.Fprintf(&b, "\tif err := %s.Unmarshal(b, &v); err != nil {\n\t\treturn %s.Errorf(%q, err)\n\t}\n", jsonPkg(), fmtPkg(), "decoding "+name+": %w")
			fmt.Fprintf(&b, "\tif v.Type == nil {\n\t\treturn %s.Errorf(%q)\n\t}\n", fmtPkg(), "decoding "+name+`: missing "type"`)
			fmt.Fprintf(&b, "\tswitch *v.Type {\n")
			for i := range en.Len() {
				v := en.Variant(i)
				fmt.Fprintf(&b, "\tcase %q:\n", v.JSONName())
				if !v.HasValue() {
					fmt.Fprintf(&b, "\t\t*e = %s\n", construct(v, ""))
					continue
				}
				fmt.Fprintf(&b, "\t\tif v.Value == nil {\n\t\t\treturn %s.Errorf(%q)\n\t\t}\n", fmtPkg(), "decoding "+name+": variant "+strconv.Quote(v.JSONName())+` needs a "value"`)
				fmt.Fprintf(&b, "\t\tvar x %s\n", l.source(payloads[v.Name()]))
				fmt.Fprintf(&b, "\t\tif err := %s.Unmarshal(v.Value, &x); err != nil {\n\t\t\treturn %s.Errorf(%q, err)\n\t\t}\n", jsonPkg(), fmtPkg(), "decoding "+name+" "+strconv.Quote(v.JSONName())+": %w")
				fmt.Fprintf(&b, "\t\t*e = %s\n", construct(v, "x"))
			}
			fmt.Fprintf(&b, "\tdefault:\n\t\treturn %s.Errorf(%q, *v.Type)\n\t}\n\treturn nil\n}\n", fmtPkg(), "unknown "+name+" variant %q (want "+wantList+")")
		}
	}
	l.generated = append(l.generated, b.String())
}

// source prints a (Go) expression.
func (l *lowerer) source(x ast.Expr) string {
	var b bytes.Buffer
	printer.Fprint(&b, l.fset, x)
	return b.String()
}

// variantOf returns the variant a selector denotes, or nil.
func (l *lowerer) variantOf(x ast.Expr) *types.Variant {
	if sel, ok := ast.Unparen(x).(*ast.SelectorExpr); ok {
		v, _ := l.info.Uses[sel.Sel].(*types.Variant)
		return v
	}
	return nil
}

// construct returns the value of variant v of enum type t, carrying val
// (nil for a variant without a value).
func (l *lowerer) construct(t types.Type, v *types.Variant, val ast.Expr, pos token.Pos) ast.Expr {
	id := func(name string) *ast.Ident { return &ast.Ident{NamePos: pos, Name: name} }
	lit := &ast.CompositeLit{Type: l.typeExpr(t, pos), Lbrace: pos, Rbrace: pos}
	if v.Index() != 0 {
		lit.Elts = append(lit.Elts, &ast.KeyValueExpr{Key: id(types.EnumTagField), Colon: pos, Value: &ast.BasicLit{ValuePos: pos, Kind: token.INT, Value: strconv.Itoa(v.Index())}})
	}
	if val != nil {
		lit.Elts = append(lit.Elts, &ast.KeyValueExpr{Key: id(v.FieldName()), Colon: pos, Value: val})
	}
	// parenthesized, so it can appear in if and for headers
	return &ast.ParenExpr{Lparen: pos, X: lit, Rparen: pos}
}

// variantValue lowers a variant used as a value: T.V for a variant without
// a value, or a constructor function value T.V.
func (l *lowerer) variantValue(x *ast.SelectorExpr, v *types.Variant) ast.Expr {
	pos := x.Pos()
	t := l.info.Types[x].Type
	sig, isFunc := t.(*types.Signature)
	if !isFunc {
		return l.construct(t, v, nil, pos)
	}
	// func(v P) T { return T{...} }
	p := l.temp(pos, "v")
	enumType := sig.Results().At(0).Type()
	return &ast.FuncLit{
		Type: l.funcTypeNamed(sig, pos, p),
		Body: &ast.BlockStmt{Lbrace: pos, List: []ast.Stmt{&ast.ReturnStmt{Return: pos, Results: []ast.Expr{l.construct(enumType, v, ident(p), pos)}}}, Rbrace: pos},
	}
}

// funcTypeNamed is funcType with the (single) parameter named p.
func (l *lowerer) funcTypeNamed(sig *types.Signature, pos token.Pos, p *ast.Ident) *ast.FuncType {
	ft := l.funcType(sig, pos)
	ft.Params.List[0].Names = []*ast.Ident{p}
	return ft
}

// patternParts returns the variant of a pattern and the identifier bound
// to its value (nil if none, or _).
func (l *lowerer) patternParts(p ast.Expr) (*types.Variant, *ast.Ident) {
	switch p := ast.Unparen(p).(type) {
	case *ast.SelectorExpr:
		return l.variantOf(p), nil
	case *ast.CallExpr:
		v := l.variantOf(p.Fun)
		if id, ok := p.Args[0].(*ast.Ident); ok && id.Name != "_" {
			return v, id
		}
		return v, nil
	}
	return nil, nil
}

func tagSelector(x ast.Expr) ast.Expr {
	return &ast.SelectorExpr{X: primary(x), Sel: &ast.Ident{NamePos: x.Pos(), Name: types.EnumTagField}}
}

func intLit(pos token.Pos, n int) *ast.BasicLit {
	return &ast.BasicLit{ValuePos: pos, Kind: token.INT, Value: strconv.Itoa(n)}
}

// primary parenthesizes x unless it is a primary expression.
func primary(x ast.Expr) ast.Expr {
	switch x.(type) {
	case *ast.Ident, *ast.SelectorExpr, *ast.CallExpr, *ast.IndexExpr, *ast.IndexListExpr, *ast.ParenExpr:
		return x
	}
	return &ast.ParenExpr{Lparen: x.Pos(), X: x, Rparen: x.End()}
}

// enumSwitch lowers a switch on an enum value.
func (l *lowerer) enumSwitch(s *ast.SwitchStmt) []ast.Stmt {
	pos := s.Switch
	var stmts []ast.Stmt
	if s.Init != nil {
		stmts = l.stmt(s.Init)
	}
	tag := l.expr(s.Tag, &stmts)
	e := l.temp(pos, "e")
	stmts = append(stmts, define([]ast.Expr{e}, []ast.Expr{tag}))

	hasDefault := false
	for _, c := range s.Body.List {
		cc := c.(*ast.CaseClause)
		if cc.List == nil {
			hasDefault = true
		}
		var binds []ast.Stmt
		for i, p := range cc.List {
			v, bind := l.patternParts(p)
			cc.List[i] = intLit(p.Pos(), v.Index())
			if bind != nil {
				val := &ast.SelectorExpr{X: ident(e), Sel: &ast.Ident{NamePos: bind.Pos(), Name: v.FieldName()}}
				binds = append(binds, define([]ast.Expr{bind}, []ast.Expr{val}))
			}
		}
		cc.Body = append(binds, l.stmts(cc.Body)...)
	}
	if !hasDefault {
		// exhaustive: Go needs to know the switch terminates
		s.Body.List = append(s.Body.List, &ast.CaseClause{Case: s.Body.Rbrace, Colon: s.Body.Rbrace, Body: []ast.Stmt{
			&ast.ExprStmt{X: &ast.CallExpr{Fun: &ast.Ident{NamePos: s.Body.Rbrace, Name: "panic"}, Lparen: s.Body.Rbrace, Args: []ast.Expr{&ast.BasicLit{ValuePos: s.Body.Rbrace, Kind: token.STRING, Value: `"invalid enum value"`}}, Rparen: s.Body.Rbrace}},
		}})
	}
	s.Init = nil
	s.Tag = tagSelector(ident(e))
	b := &ast.BlockStmt{Lbrace: pos, List: append(stmts, s), Rbrace: s.End()}
	l.wrapped[b] = true
	return []ast.Stmt{b}
}

// isExpr lowers x is T.V or x is T.V(name). A binding assigns the
// variable declared ahead of the enclosing if statement (see ifBindings).
func (l *lowerer) isExpr(x *ast.IsExpr, pre *[]ast.Stmt) ast.Expr {
	pos := x.Is
	subject := l.expr(x.X, pre)
	v, bind := l.patternParts(x.Pattern)
	if v == nil {
		l.errorf(x.Pos(), "internal error: is without variant")
		return x
	}
	test := &ast.BinaryExpr{X: tagSelector(subject), OpPos: pos, Op: token.EQL, Y: intLit(pos, v.Index())}
	if bind == nil {
		return test
	}
	// func() bool { e := x; if e.GPP_tag != i { return false }; name = e.GPP_V; return true }()
	e := l.temp(pos, "e")
	id := func(name string) *ast.Ident { return &ast.Ident{NamePos: pos, Name: name} }
	body := []ast.Stmt{
		define([]ast.Expr{e}, []ast.Expr{subject}),
		&ast.IfStmt{If: pos, Cond: &ast.BinaryExpr{X: tagSelector(ident(e)), OpPos: pos, Op: token.NEQ, Y: intLit(pos, v.Index())},
			Body: &ast.BlockStmt{Lbrace: pos, List: []ast.Stmt{&ast.ReturnStmt{Return: pos, Results: []ast.Expr{id("false")}}}, Rbrace: pos}},
		assign([]ast.Expr{ident(bind)}, []ast.Expr{&ast.SelectorExpr{X: ident(e), Sel: id(v.FieldName())}}),
		&ast.ReturnStmt{Return: pos, Results: []ast.Expr{id("true")}},
	}
	lit := &ast.FuncLit{
		Type: &ast.FuncType{Func: pos, Params: &ast.FieldList{Opening: pos, Closing: pos}, Results: &ast.FieldList{List: []*ast.Field{{Type: id("bool")}}}},
		Body: &ast.BlockStmt{Lbrace: pos, List: body, Rbrace: pos},
	}
	return &ast.CallExpr{Fun: lit, Lparen: pos, Rparen: pos}
}

// bindings prepares the names bound by is expressions in the condition of
// an if statement or ternary expression: they are renamed (so they cannot
// capture names the condition uses before them) in cond and body, and
// returned as variable declarations to put ahead of the condition.
//
// A bound name keeps its name (as debuggers show it) unless the statement
// (scope) uses the same name for something else.
func (l *lowerer) bindings(cond ast.Expr, body, scope ast.Node) []ast.Stmt {
	var decls []ast.Stmt
	renames := map[types.Object]string{}
	conflicts := func(name string, obj types.Object) bool {
		found := false
		ast.Inspect(scope, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == name {
				other := l.info.Uses[id]
				if other == nil {
					other = l.info.Defs[id]
				}
				if other != obj {
					found = true
				}
			}
			return !found
		})
		return found
	}
	ast.Inspect(cond, func(n ast.Node) bool {
		is, ok := n.(*ast.IsExpr)
		if !ok {
			return true
		}
		if _, bind := l.patternParts(is.Pattern); bind != nil {
			if obj := l.info.Defs[bind]; obj != nil {
				name := &ast.Ident{NamePos: bind.Pos(), Name: bind.Name}
				if conflicts(bind.Name, obj) {
					name = l.temp(bind.Pos(), bind.Name+"_")
				}
				renames[obj] = name.Name
				decls = append(decls, &ast.DeclStmt{Decl: &ast.GenDecl{TokPos: bind.Pos(), Tok: token.VAR, Specs: []ast.Spec{
					&ast.ValueSpec{Names: []*ast.Ident{name}, Type: l.typeExpr(obj.Type(), bind.Pos())},
				}}})
			}
		}
		return true
	})
	if len(renames) == 0 {
		return nil
	}
	rename := func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			obj := l.info.Uses[id]
			if obj == nil {
				obj = l.info.Defs[id]
			}
			if name, ok := renames[obj]; ok {
				id.Name = name
			}
		}
		return true
	}
	ast.Inspect(cond, rename)
	ast.Inspect(body, rename)
	return decls
}

// wrapBindings puts the declarations of bound names ahead of the lowered
// if statement, in a block.
func (l *lowerer) wrapBindings(decls, stmts []ast.Stmt) []ast.Stmt {
	if len(decls) == 0 {
		return stmts
	}
	b := &ast.BlockStmt{Lbrace: stmts[0].Pos(), List: slices.Concat(decls, stmts), Rbrace: stmts[len(stmts)-1].End()}
	l.wrapped[b] = true
	return []ast.Stmt{b}
}
