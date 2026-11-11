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
	"go/token"
	"slices"
	"strconv"

	"gpp/ast"
	"gpp/types"
)

// enumDecl lowers the enum type declaration spec, returning the String
// method to add, if any.
func (l *lowerer) enumDecl(spec *ast.TypeSpec, et *ast.EnumType) *ast.FuncDecl {
	pos := et.Enum
	obj, _ := l.info.Defs[spec.Name].(*types.TypeName)
	tagType := "uint8"
	if len(et.Variants.List) > 256 {
		tagType = "uint16"
	}
	fields := &ast.FieldList{Opening: et.Variants.Opening, Closing: et.Variants.Closing}
	fields.List = append(fields.List, &ast.Field{
		Names: []*ast.Ident{{NamePos: pos, Name: types.EnumTagField}},
		Type:  &ast.Ident{NamePos: pos, Name: tagType},
	})
	for _, f := range et.Variants.List {
		if f.Type != nil {
			name := f.Names[0]
			fields.List = append(fields.List, &ast.Field{
				Doc:   f.Doc,
				Names: []*ast.Ident{{NamePos: name.Pos(), Name: "GPP_" + name.Name}},
				Type:  f.Type,
			})
		}
	}
	spec.Type = &ast.StructType{Struct: pos, Fields: fields}

	if obj == nil {
		return nil
	}
	named, _ := obj.Type().(*types.Named)
	if named == nil || !types.EnumHasGeneratedString(named) {
		return nil
	}
	return l.enumString(spec, named, pos)
}

// enumString generates the String method of an enum.
func (l *lowerer) enumString(spec *ast.TypeSpec, named *types.Named, pos token.Pos) *ast.FuncDecl {
	id := func(name string) *ast.Ident { return &ast.Ident{NamePos: pos, Name: name} }
	str := func(s string) *ast.BasicLit {
		return &ast.BasicLit{ValuePos: pos, Kind: token.STRING, Value: strconv.Quote(s)}
	}
	recv := l.temp(pos, "e")
	sprintf := func(format string, arg ast.Expr) ast.Expr {
		return &ast.CallExpr{Fun: &ast.SelectorExpr{X: id(l.importName("fmt")), Sel: id("Sprintf")}, Lparen: pos, Args: []ast.Expr{str(format), arg}, Rparen: pos}
	}
	field := func(name string) ast.Expr { return &ast.SelectorExpr{X: ident(recv), Sel: id(name)} }

	en := types.EnumOf(named)
	var clauses []ast.Stmt
	for i := range en.Len() {
		v := en.Variant(i)
		var result ast.Expr = str(v.Name())
		if v.HasValue() {
			result = sprintf(v.Name()+"(%v)", field(v.FieldName()))
		}
		clauses = append(clauses, &ast.CaseClause{
			Case: pos, Colon: pos,
			List: []ast.Expr{&ast.BasicLit{ValuePos: pos, Kind: token.INT, Value: strconv.Itoa(i)}},
			Body: []ast.Stmt{&ast.ReturnStmt{Return: pos, Results: []ast.Expr{result}}},
		})
	}
	body := []ast.Stmt{
		&ast.SwitchStmt{Switch: pos, Tag: field(types.EnumTagField), Body: &ast.BlockStmt{Lbrace: pos, List: clauses, Rbrace: pos}},
		&ast.ReturnStmt{Return: pos, Results: []ast.Expr{sprintf(named.Obj().Name()+"(%d)", field(types.EnumTagField))}},
	}

	// receiver type: T or T[P, Q]
	var rtype ast.Expr = id(spec.Name.Name)
	if spec.TypeParams != nil {
		var args []ast.Expr
		for _, f := range spec.TypeParams.List {
			for _, n := range f.Names {
				args = append(args, id(n.Name))
			}
		}
		if len(args) == 1 {
			rtype = &ast.IndexExpr{X: rtype, Lbrack: pos, Index: args[0], Rbrack: pos}
		} else {
			rtype = &ast.IndexListExpr{X: rtype, Lbrack: pos, Indices: args, Rbrack: pos}
		}
	}
	return &ast.FuncDecl{
		Recv: &ast.FieldList{Opening: pos, List: []*ast.Field{{Names: []*ast.Ident{recv}, Type: rtype}}, Closing: pos},
		Name: id("String"),
		Type: &ast.FuncType{Func: pos, Params: &ast.FieldList{Opening: pos, Closing: pos}, Results: &ast.FieldList{List: []*ast.Field{{Type: id("string")}}}},
		Body: &ast.BlockStmt{Lbrace: pos, List: body, Rbrace: pos},
	}
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
func (l *lowerer) bindings(cond ast.Expr, body ast.Node) []ast.Stmt {
	var decls []ast.Stmt
	renames := map[types.Object]string{}
	ast.Inspect(cond, func(n ast.Node) bool {
		is, ok := n.(*ast.IsExpr)
		if !ok {
			return true
		}
		if _, bind := l.patternParts(is.Pattern); bind != nil {
			if obj := l.info.Defs[bind]; obj != nil {
				name := l.temp(bind.Pos(), bind.Name+"_")
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
