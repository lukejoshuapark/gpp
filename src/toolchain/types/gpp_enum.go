// Copyright 2026 The G++ Authors. Portions copyright The Go Authors.
// Use of this source code is governed by a BSD-style license.

// This file implements G++ enums: named types whose values are one of a
// fixed set of variants, each carrying at most one value.
//
// An enum's underlying type is a struct holding a tag (the variant's
// index) and one field per variant with a value:
//
//	type Shape enum { Empty; Circle(float64); Rect(Size) }
//
// has the underlying type
//
//	struct { GPP_tag uint8; GPP_Circle float64; GPP_Rect Size }
//
// so enums need no allocation, are comparable when their values are, and
// their zero value is the first variant. The fields are reserved: G++ code
// uses variants (Shape.Circle(1)), switch patterns and "is".

package types

import (
	"go/token"
	"reflect"
	"strconv"
	"strings"

	"github.com/lukejoshuapark/gpp/src/toolchain/ast"
	. "github.com/lukejoshuapark/gpp/src/toolchain/internal/typeerrors"
)

// EnumTagField is the name of the tag field of an enum's underlying struct.
const EnumTagField = "GPP_tag"

// enumFieldPrefix starts the names of the fields of enum structs.
const enumFieldPrefix = "GPP_"

// An Enum describes the variants of an enum type.
type Enum struct {
	variants []*Variant
}

// Len returns the number of variants.
func (e *Enum) Len() int { return len(e.variants) }

// Variant returns the i'th variant.
func (e *Enum) Variant(i int) *Variant { return e.variants[i] }

// Lookup returns the variant with the given name, or nil.
func (e *Enum) Lookup(name string) *Variant {
	for _, v := range e.variants {
		if v.name == name {
			return v
		}
	}
	return nil
}

// A Variant is a variant of an enum type. Its type is the enum type for a
// variant without values, and the constructor's signature func(T1, T2) E
// for one with values.
type Variant struct {
	object
	index    int
	values   []string // the names of the variant's values ("" if unnamed)
	jsonName string   // from the variant's json tag, or ""
}

// JSONName returns the variant's name in JSON (and text): its json tag's
// name, or else its name.
func (v *Variant) JSONName() string {
	if v.jsonName != "" {
		return v.jsonName
	}
	return v.name
}

// HasValues reports whether any variant of the enum carries a value.
func (e *Enum) HasValues() bool {
	for _, v := range e.variants {
		if len(v.values) > 0 {
			return true
		}
	}
	return false
}

// enumType returns the enum type the variant belongs to.
func (v *Variant) enumType() Type {
	if sig, ok := v.typ.(*Signature); ok {
		return sig.results.vars[0].typ
	}
	return v.typ
}

// Index returns the variant's tag value.
func (v *Variant) Index() int { return v.index }

// HasValue reports whether the variant carries values.
func (v *Variant) HasValue() bool { return len(v.values) > 0 }

// NumValues returns the number of values the variant carries.
func (v *Variant) NumValues() int { return len(v.values) }

// ValueName returns the declared name of the variant's i'th value, or "".
func (v *Variant) ValueName(i int) string { return v.values[i] }

// NamedValues reports whether the variant's values have names.
func (v *Variant) NamedValues() bool { return len(v.values) > 0 && v.values[0] != "" }

// FieldName returns the name of the struct field holding the variant's
// i'th value: GPP_V for a variant with one value, and GPP_0_V, GPP_1_V,
// ... for one with several (variant names cannot start with a digit).
func (v *Variant) FieldName(i int) string { return EnumFieldName(v.name, i, len(v.values)) }

// EnumFieldName returns the name of the struct field holding the i'th of
// the n values of the variant named variant (see Variant.FieldName).
func EnumFieldName(variant string, i, n int) string {
	if n == 1 {
		return enumFieldPrefix + variant
	}
	return enumFieldPrefix + strconv.Itoa(i) + "_" + variant
}

// ValueTypes returns the types of the variant's values in the enum type t
// (which may be an instance of a generic enum).
func (v *Variant) ValueTypes(t Type) []Type {
	if len(v.values) == 0 {
		return nil
	}
	st, ok := t.Underlying().(*Struct)
	if !ok {
		return nil
	}
	types := make([]Type, len(v.values))
	for i := range v.values {
		name := v.FieldName(i)
		for _, f := range st.fields {
			if f.name == name {
				types[i] = f.typ
			}
		}
		if types[i] == nil {
			types[i] = Typ[Invalid]
		}
	}
	return types
}

// valueParams returns the parameters of the constructor of v in the enum
// type t.
func (v *Variant) valueParams(t Type, pos token.Pos, pkg *Package) *Tuple {
	var params []*Var
	for i, vt := range v.ValueTypes(t) {
		params = append(params, NewParam(pos, pkg, v.values[i], vt))
	}
	return NewTuple(params...)
}

// EnumOf returns the enum description of t, or nil if t is not an enum.
func EnumOf(t Type) *Enum {
	if t == nil {
		return nil
	}
	if n, ok := Unalias(t).(*Named); ok {
		return n.Origin().enum
	}
	return nil
}

// EnumGeneratedMethods returns the methods generated for the enum type n
// (those of String, MarshalText, UnmarshalText, MarshalJSON and
// UnmarshalJSON that apply and that it does not declare itself).
func EnumGeneratedMethods(n *Named) []string {
	o := n.Origin()
	if o.enum == nil {
		return nil
	}
	return o.enumGen
}

// enumType type-checks an enum type expression, the RHS of the
// declaration of def.
func (check *Checker) enumType(e *ast.EnumType, def *TypeName) Type {
	var named *Named
	if def != nil {
		named, _ = def.typ.(*Named)
	}
	if named == nil {
		check.error(e, NotAnExpr, "an enum must be declared as a named type: type Name enum { ... }")
		return Typ[Invalid]
	}

	tag := Typ[Uint8]
	if len(e.Variants.List) > 256 {
		tag = Typ[Uint16]
	}
	fields := []*Var{NewField(e.Enum, check.pkg, EnumTagField, tag, false)}
	enum := &Enum{}
	seen := map[string]bool{}
	jsonNames := map[string]string{} // JSON name -> variant
	for i, f := range e.Variants.List {
		name := f.Names[0]
		v := &Variant{object: object{pos: name.Pos(), pkg: check.pkg, name: name.Name}, index: i}
		switch {
		case name.Name == "_":
			check.error(name, NotAnExpr, "an enum variant cannot be named _")
		case strings.HasPrefix(name.Name, enumFieldPrefix):
			check.errorf(name, NotAnExpr, "names starting with %s are reserved", enumFieldPrefix)
		case seen[name.Name]:
			check.errorf(name, DuplicateDecl, "variant %s redeclared in enum %s", name.Name, def.name)
			continue
		}
		seen[name.Name] = true
		if ft, ok := f.Type.(*ast.FuncType); ok {
			var params []*Var
			type value struct {
				pos  token.Pos
				name string
				typ  Type
			}
			var vals []value
			for _, pf := range ft.Params.List {
				pt := check.varType(pf.Type)
				if len(pf.Names) == 0 {
					vals = append(vals, value{pf.Type.Pos(), "", pt})
				}
				for _, n := range pf.Names {
					vals = append(vals, value{n.Pos(), n.Name, pt})
				}
			}
			valueNames := map[string]bool{}
			for _, val := range vals {
				switch {
				case val.name == "_":
					check.errorf(atPos(val.pos), NotAnExpr, "a value of variant %s cannot be named _", name.Name)
				case valueNames[val.name] && val.name != "":
					check.errorf(atPos(val.pos), DuplicateDecl, "duplicate value %s in variant %s", val.name, name.Name)
				}
				valueNames[val.name] = true
				v.values = append(v.values, val.name)
			}
			for i, val := range vals {
				fields = append(fields, NewField(val.pos, check.pkg, v.FieldName(i), val.typ, false))
				params = append(params, NewParam(val.pos, check.pkg, val.name, val.typ))
			}
			v.typ = NewSignatureType(nil, nil, nil,
				NewTuple(params...),
				NewTuple(newVar(ResultVar, name.Pos(), check.pkg, "", named)), false)
		} else {
			v.typ = named
		}
		if f.Tag != nil {
			v.jsonName = check.variantTag(f.Tag, jsonNames)
		}
		if other, ok := jsonNames[v.JSONName()]; ok {
			check.errorf(f, DuplicateDecl, "variants %s and %s have the same JSON name %q", other, v.name, v.JSONName())
		}
		jsonNames[v.JSONName()] = v.name
		check.recordDef(name, v)
		enum.variants = append(enum.variants, v)
	}
	named.enum = enum

	// Variant and method names must differ: T.Name would be ambiguous.
	check.later(func() {
		for _, v := range enum.variants {
			for _, m := range named.methods {
				if m.name == v.name {
					check.errorf(m, DuplicateFieldAndMethod, "method %s.%s has the same name as a variant of the enum", def.name, m.name)
				}
			}
		}
	}).describef(e, "enum %s", def.name)

	return NewStruct(fields, nil)
}

// enumMethods gives the enum type obj the methods generated when the code
// is lowered, unless it declares them: String, and for encoding,
// MarshalText and UnmarshalText (an enum without values encodes as its
// variant's name) or MarshalJSON and UnmarshalJSON (an enum with values
// encodes as {"type": name, "value": value}).
func (check *Checker) enumMethods(obj *TypeName) {
	n := asNamed(obj.typ)
	if n == nil || n.enum == nil {
		return
	}
	type method struct {
		name    string
		pointer bool
		params  []Type
		results []Type
	}
	bytes := NewSlice(Typ[Byte])
	methods := []method{{"String", false, nil, []Type{Typ[String]}}}
	if n.enum.HasValues() {
		methods = append(methods,
			method{"MarshalJSON", false, nil, []Type{bytes, universeError}},
			method{"UnmarshalJSON", true, []Type{bytes}, []Type{universeError}})
	} else {
		methods = append(methods,
			method{"MarshalText", false, nil, []Type{bytes, universeError}},
			method{"UnmarshalText", true, []Type{bytes}, []Type{universeError}})
	}
	for _, m := range methods {
		if hasMethod(n, m.name) {
			continue
		}
		n.enumGen = append(n.enumGen, m.name)
		if n.TypeParams().Len() > 0 {
			// Generic enums get the methods when lowered too, but the
			// checker does not know them: receivers of generic types
			// need type parameters of their own.
			continue
		}
		var rtype Type = n
		if m.pointer {
			rtype = NewPointer(n)
		}
		vars := func(types []Type, kind VarKind) *Tuple {
			var vs []*Var
			for _, t := range types {
				vs = append(vs, newVar(kind, obj.pos, check.pkg, "", t))
			}
			return NewTuple(vs...)
		}
		sig := NewSignatureType(NewParam(obj.pos, check.pkg, "", rtype), nil, nil, vars(m.params, ParamVar), vars(m.results, ResultVar), false)
		n.AddMethod(NewFunc(obj.pos, check.pkg, m.name, sig))
	}
}

func hasMethod(n *Named, name string) bool {
	for _, m := range n.methods {
		if m.name == name {
			return true
		}
	}
	return false
}

// enumSelector handles x.Sel where x is an enum type: a variant. It
// reports whether x was an enum type.
func (check *Checker) enumSelector(x *operand, e *ast.SelectorExpr) bool {
	if x.mode() != typexpr {
		return false
	}
	n := asNamed(x.typ())
	if n == nil || n.Origin().enum == nil {
		return false
	}
	en := n.Origin().enum
	v := en.Lookup(e.Sel.Name)
	if v == nil {
		if hasMethod(n.Origin(), e.Sel.Name) {
			return false // method expression
		}
		check.errorf(e.Sel, MissingFieldOrMethod, "%s has no variant %s", x.typ(), e.Sel.Name)
		x.invalidate()
		return true
	}
	check.recordUse(e.Sel, v)
	generic := n.TypeParams().Len() > 0 && n.TypeArgs().Len() == 0
	if !v.HasValue() {
		if generic {
			check.errorf(e, CannotInferTypeArgs, "cannot infer type arguments for %s.%s: write them out, as in %s[T].%s", n.obj.name, v.name, n.obj.name, v.name)
			x.invalidate()
			return true
		}
		x.mode_ = value
		x.typ_ = n
		x.expr = e
		return true
	}
	if generic {
		// A generic constructor: func[T ...](payload) E[T]
		tparams, _ := check.renameTParams(e.Pos(), n.TypeParams().list(), Typ[Invalid])
		targs := make([]Type, len(tparams))
		for i, tp := range tparams {
			targs[i] = tp
		}
		inst := check.instance(e.Pos(), n, targs, nil, check.context()).(*Named)
		// (renameTParams has bound the fresh type parameters already)
		sig := &Signature{
			tparams: &TypeParamList{tparams: tparams},
			params:  v.valueParams(inst, e.Sel.Pos(), check.pkg),
			results: NewTuple(newVar(ResultVar, e.Sel.Pos(), check.pkg, "", inst)),
		}
		x.mode_ = value
		x.typ_ = sig
		x.expr = e
		return true
	}
	sig := NewSignatureType(nil, nil, nil,
		v.valueParams(n, e.Sel.Pos(), check.pkg),
		NewTuple(newVar(ResultVar, e.Sel.Pos(), check.pkg, "", n)), false)
	x.mode_ = value
	x.typ_ = sig
	x.expr = e
	return true
}

// isReservedEnumField reports whether a field selection is of an enum's
// internal field.
func isReservedEnumField(t Type, name string) bool {
	if !strings.HasPrefix(name, enumFieldPrefix) {
		return false
	}
	if p, ok := t.Underlying().(*Pointer); ok {
		t = p.base
	}
	return EnumOf(t) != nil
}

// ----------------------------------------------------------------------------
// Patterns: T.Variant and T.Variant(name, ...)

// enumPattern checks a pattern for the enum type n. It returns the variant
// and the identifiers binding its values (nil if none; any may be _).
func (check *Checker) enumPattern(n Type, p ast.Expr) (*Variant, []*ast.Ident) {
	var sel *ast.SelectorExpr
	var call *ast.CallExpr
	var binds []*ast.Ident
	switch p := ast.Unparen(p).(type) {
	case *ast.SelectorExpr:
		sel = p
	case *ast.CallExpr:
		sel, _ = ast.Unparen(p.Fun).(*ast.SelectorExpr)
		if sel != nil {
			call = p
			if p.Ellipsis.IsValid() {
				check.error(atPos(p.Ellipsis), NotAnExpr, "a variant pattern binds names: T.Variant(name, ...)")
				return nil, nil
			}
			for _, arg := range p.Args {
				id, ok := arg.(*ast.Ident)
				if !ok {
					check.errorf(arg, NotAnExpr, "a variant pattern binds a name, not %s (compare values with ==)", ExprString(arg))
					return nil, nil
				}
				binds = append(binds, id)
			}
		}
	}
	if sel == nil {
		check.errorf(p, NotAnExpr, "%s is not a variant pattern: write %s.Variant or %s.Variant(name)", ExprString(p), typeName(n), typeName(n))
		return nil, nil
	}

	var x operand
	check.exprOrType(&x, sel.X, true)
	if !x.isValid() {
		return nil, nil
	}
	if x.mode() != typexpr {
		check.errorf(sel.X, NotAType, "%s is not a type: a pattern names the enum type, as in %s.%s", ExprString(sel.X), typeName(n), sel.Sel.Name)
		return nil, nil
	}
	nn := asNamed(n)
	pn := asNamed(x.typ())
	if pn == nil || nn == nil || (!Identical(pn, nn) && pn != nn.Origin()) {
		check.errorf(sel.X, MismatchedTypes, "pattern type %s does not match %s", x.typ(), n)
		return nil, nil
	}
	en := nn.Origin().enum
	v := en.Lookup(sel.Sel.Name)
	if v == nil {
		check.errorf(sel.Sel, MissingFieldOrMethod, "%s has no variant %s", typeName(n), sel.Sel.Name)
		return nil, nil
	}
	check.recordUse(sel.Sel, v)
	if call != nil && len(binds) != len(v.values) {
		switch {
		case !v.HasValue():
			var at positioner = call
			if len(binds) > 0 {
				at = binds[0]
			}
			check.errorf(at, WrongArgCount, "%s.%s has no value to bind", typeName(n), v.name)
		case len(v.values) == 1:
			check.errorf(call, WrongArgCount, "%s.%s has one value: bind it with %s.%s(name)", typeName(n), v.name, typeName(n), v.name)
		default:
			check.errorf(call, WrongArgCount, "%s.%s has %d values: bind each one (_ ignores one), or match any with %s.%s", typeName(n), v.name, len(v.values), typeName(n), v.name)
		}
		return v, nil
	}
	return v, binds
}

// bindsNames reports whether binds binds any name (other than _).
func bindsNames(binds []*ast.Ident) bool {
	for _, id := range binds {
		if id.Name != "_" {
			return true
		}
	}
	return false
}

func typeName(t Type) string {
	if n := asNamed(t); n != nil {
		return n.obj.name
	}
	return TypeString(t, nil)
}

// bindVariant declares the variables bound by a pattern in the current
// scope (with binds nil, it binds none).
func (check *Checker) bindVariant(enumType Type, v *Variant, binds []*ast.Ident, scopePos token.Pos) {
	if binds == nil {
		return
	}
	types := v.ValueTypes(enumType)
	for i, id := range binds {
		if id.Name == "_" {
			check.recordDef(id, nil)
			continue
		}
		obj := newVar(LocalVar, id.Pos(), check.pkg, id.Name, types[i])
		check.declare(check.scope, id, obj, scopePos)
	}
}

// enumSwitch checks a switch on a value x of enum type: its cases are
// patterns, and without a default case they must cover every variant.
func (check *Checker) enumSwitch(s *ast.SwitchStmt, x *operand, inner stmtContext) {
	en := EnumOf(x.typ())
	covered := make([]bool, en.Len())
	hasDefault := false
	check.multipleDefaults(s.Body.List)
	for i, c := range s.Body.List {
		clause, _ := c.(*ast.CaseClause)
		if clause == nil {
			continue
		}
		if clause.List == nil {
			hasDefault = true
		}
		check.openScope(clause, "case")
		for _, p := range clause.List {
			v, binds := check.enumPattern(x.typ(), p)
			if v == nil {
				continue
			}
			if covered[v.index] {
				check.errorf(p, DuplicateCase, "duplicate case %s in switch", ExprString(p))
			}
			covered[v.index] = true
			if len(clause.List) > 1 && bindsNames(binds) {
				for _, id := range binds {
					if id.Name != "_" {
						check.errorf(id, NotAnExpr, "cannot bind %s in a case with several patterns", id.Name)
						break
					}
				}
				binds = nil
			}
			check.bindVariant(x.typ(), v, binds, clause.Colon)
		}
		inner := inner
		if i+1 == len(s.Body.List) {
			inner |= finalSwitchCase
		}
		check.stmtList(inner, clause.Body)
		check.closeScope()
	}
	if !hasDefault {
		var missing []string
		for i, ok := range covered {
			if !ok {
				missing = append(missing, typeName(x.typ())+"."+en.variants[i].name)
			}
		}
		if len(missing) > 0 {
			check.errorf(atPos(s.Switch), InvalidExprSwitch, "switch on %s is missing %s (add the cases, or a default case)", typeName(x.typ()), strings.Join(missing, ", "))
		} else {
			if check.exhaustive == nil {
				check.exhaustive = map[*ast.SwitchStmt]bool{}
			}
			check.exhaustive[s] = true
		}
	}
}

// ----------------------------------------------------------------------------
// x is T.Variant(name)

// isExpr checks a variant test. Its binding, if any, is declared in the
// current scope if e may bind (see Checker.condBindings).
func (check *Checker) isExpr(x *operand, e *ast.IsExpr) {
	var y operand
	check.expr(nil, &y, e.X)
	if !y.isValid() {
		x.invalidate()
		return
	}
	if EnumOf(y.typ()) == nil {
		check.errorf(&y, NotAnExpr, "is needs a value of enum type, not %s", y.typ())
		x.invalidate()
		return
	}
	v, binds := check.enumPattern(y.typ(), e.Pattern)
	if v == nil {
		x.invalidate()
		return
	}
	if bindsNames(binds) && !check.bindable[e] {
		for _, id := range binds {
			if id.Name != "_" {
				check.errorf(id, NotAnExpr, "%s can only be bound in the condition of an if or ternary, in a chain of &&", id.Name)
				break
			}
		}
		binds = nil
	}
	check.bindVariant(y.typ(), v, binds, e.End())
	x.mode_ = value
	x.typ_ = Typ[UntypedBool]
	x.val = nil
	x.expr = e
}

// condBindings returns the is expressions in cond that may bind names:
// those in its top-level chain of &&.
func condBindings(cond ast.Expr) []*ast.IsExpr {
	var list []*ast.IsExpr
	var walk func(x ast.Expr)
	walk = func(x ast.Expr) {
		switch x := ast.Unparen(x).(type) {
		case *ast.BinaryExpr:
			if x.Op == token.LAND {
				walk(x.X)
				walk(x.Y)
			}
		case *ast.IsExpr:
			list = append(list, x)
		}
	}
	walk(cond)
	return list
}

// isTerminatingEnumSwitch is isTerminatingSwitch for an exhaustive switch
// on an enum, which needs no default case.
func (check *Checker) isTerminatingEnumSwitch(body *ast.BlockStmt, label string) bool {
	for _, s := range body.List {
		cc := s.(*ast.CaseClause)
		if !check.isTerminatingList(cc.Body, "") || hasBreakList(cc.Body, label, true) {
			return false
		}
	}
	return true
}

// openBindings marks the is expressions in cond's && chain as able to bind
// names, and if there are any, opens a scope for them ending at end (the
// end of the if body or the ternary's true branch). It reports whether it
// opened a scope.
func (check *Checker) openBindings(cond ast.Expr, node ast.Node, end token.Pos) bool {
	binds := condBindings(cond)
	if len(binds) == 0 {
		return false
	}
	if check.bindable == nil {
		check.bindable = map[*ast.IsExpr]bool{}
	}
	for _, b := range binds {
		check.bindable[b] = true
	}
	check.openScope(node, "is")
	check.scope.end = end
	return true
}

// variantTag returns the JSON name in a variant's tag (`json:"name"`).
func (check *Checker) variantTag(tag *ast.BasicLit, names map[string]string) string {
	s, err := strconv.Unquote(tag.Value)
	if err != nil {
		check.error(tag, InvalidSyntaxTree, "invalid tag")
		return ""
	}
	name, opts, _ := strings.Cut(reflect.StructTag(s).Get("json"), ",")
	switch {
	case opts != "":
		check.errorf(tag, NotAnExpr, "a variant's json tag can only rename it, not %q", ","+opts)
	case name == "-":
		check.error(tag, NotAnExpr, `json:"-" is not supported for variants`)
		return ""
	}
	return name
}
