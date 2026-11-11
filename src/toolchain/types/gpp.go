// Copyright 2026 The G++ Authors. Portions copyright The Go Authors.
// Use of this source code is governed by a BSD-style license.

// This file implements type checking of the expressions G++ adds to Go:
// ternary expressions, postfix ? (error propagation) and lambdas.

package types

import (
	"go/token"

	"gpp/ast"
	. "gpp/internal/typeerrors"
)

// lambdaType is the provisional type of a lambda call argument whose
// parameter types are not known yet. It never escapes Checker.arguments.
type lambdaType struct {
	e *ast.LambdaExpr
}

func (t *lambdaType) Underlying() Type { return t }
func (t *lambdaType) String() string   { return "lambda" }

// ----------------------------------------------------------------------------
// Ternary: cond ? x : y

func (check *Checker) ternary(T *target, x *operand, e *ast.TernaryExpr, hint Type) {
	// Names bound by "is" in the condition are in scope in the condition
	// and the true branch.
	binds := check.openBindings(e.Cond, e, e.X.End())
	var c operand
	check.expr(nil, &c, e.Cond)
	if c.isValid() && !allBoolean(c.typ()) {
		check.errorf(&c, InvalidCond, "non-boolean condition in ternary expression")
	}

	var a, b operand
	check.branch(T, &a, e.X, hint)
	if binds {
		check.closeScope()
	}
	check.branch(T, &b, e.Y, hint)
	if !a.isValid() || !b.isValid() {
		x.invalidate()
		return
	}

	var typ Type
	switch {
	case isUntyped(a.typ()) && isUntyped(b.typ()):
		if a.isNil() && b.isNil() {
			check.error(e, UntypedNilUse, "use of untyped nil in ternary expression")
			x.invalidate()
			return
		}
		typ = maxType(a.typ(), b.typ())
		if typ == nil {
			check.errorf(e, MismatchedTypes, invalidOp+"mismatched types %s and %s in ternary expression", a.typ(), b.typ())
			x.invalidate()
			return
		}
	case isUntyped(a.typ()):
		check.convertUntyped(&a, b.typ())
		typ = b.typ()
	case isUntyped(b.typ()):
		check.convertUntyped(&b, a.typ())
		typ = a.typ()
	case Identical(a.typ(), b.typ()):
		typ = a.typ()
	default:
		if ok, _ := a.assignableTo(check, b.typ(), nil); ok {
			typ = b.typ()
		} else if ok, _ := b.assignableTo(check, a.typ(), nil); ok {
			typ = a.typ()
		} else {
			check.errorf(e, MismatchedTypes, invalidOp+"mismatched types %s and %s in ternary expression", a.typ(), b.typ())
			x.invalidate()
			return
		}
	}
	if !a.isValid() || !b.isValid() {
		x.invalidate()
		return
	}

	x.mode_ = value
	x.typ_ = typ
	x.val = nil
	x.expr = e
}

// branch checks one branch of a ternary expression.
func (check *Checker) branch(T *target, x *operand, e ast.Expr, hint Type) {
	check.rawExpr(T, x, e, hint, false)
	check.exclude(x, 1<<novalue|1<<builtin|1<<typexpr)
	check.singleValue(x)
}

// ----------------------------------------------------------------------------
// Error propagation: x?

func (check *Checker) try(x *operand, e *ast.TryExpr) {
	check.rawExpr(nil, x, e.X, nil, false)
	if !x.isValid() {
		return
	}
	defer func() { x.expr = e }()

	var results []Type
	switch {
	case x.mode() == novalue:
		check.errorf(x, TooManyValues, "%s (no value) cannot be used with ?", e.X)
		x.invalidate()
		return
	case x.mode() == typexpr || x.mode() == builtin:
		check.errorf(x, NotAnExpr, "%s is not a value", e.X)
		x.invalidate()
		return
	}
	if t, ok := x.typ().(*Tuple); ok {
		for _, v := range t.vars {
			results = append(results, v.typ)
		}
	} else {
		results = []Type{x.typ()}
	}

	last := results[len(results)-1]
	if !Identical(last, universeError) && !check.implementsError(last) {
		if len(results) == 1 {
			check.errorf(x, InvalidCall, "cannot use ? on %s: %s is not an error", e.X, last)
		} else {
			check.errorf(x, InvalidCall, "cannot use ? on %s: its last result (%s) is not an error", e.X, last)
		}
		x.invalidate()
		return
	}
	if !hasNil(last) {
		check.errorf(x, InvalidCall, "cannot use ? on %s: %s cannot be nil", e.X, last)
		x.invalidate()
		return
	}

	sig := check.sig
	if sig == nil {
		check.error(atPos(e.Question), InvalidCall, "? can only be used inside a function")
		x.invalidate()
		return
	}
	if n := sig.results.Len(); n == 0 || !Identical(sig.results.vars[n-1].typ, universeError) {
		check.error(atPos(e.Question), InvalidCall, "? can only be used in a function whose last result is an error")
		x.invalidate()
		return
	}

	rest := results[:len(results)-1]
	switch len(rest) {
	case 0:
		x.mode_ = novalue
		x.typ_ = nil
	case 1:
		x.mode_ = value
		x.typ_ = rest[0]
	default:
		vars := make([]*Var, len(rest))
		for i, t := range rest {
			vars[i] = NewVar(e.Pos(), check.pkg, "", t)
		}
		x.mode_ = value
		x.typ_ = NewTuple(vars...)
	}
	x.val = nil
	check.hasCallOrRecv = true
}

// implementsError reports whether values of type t can be used as errors.
func (check *Checker) implementsError(t Type) bool {
	return check.implements(t, universeError, false, nil)
}

// ----------------------------------------------------------------------------
// Lambdas: x => e, (x, y) => e, (x int) => { ... }

// lambdaParamCount returns the number of parameters of e.
func lambdaParamCount(e *ast.LambdaExpr) int {
	n := 0
	for _, f := range e.Params.List {
		n += len(f.Names)
	}
	return n
}

// lambda type-checks the lambda e. T, if not nil, is the function type the
// context expects. Type parameters in T.free are not known yet; T's
// parameters must not contain them, but its results may, in which case the
// result type is taken from the lambda's (expression) body.
func (check *Checker) lambda(T *target, x *operand, e *ast.LambdaExpr) {
	var tsig *Signature
	var free []*TypeParam
	if T != nil {
		tsig = T.sig
		free = T.free
	}

	n := lambdaParamCount(e)
	countMismatch := false
	if tsig != nil && tsig.params.Len() != n {
		check.errorf(e, WrongArgCount, "lambda has %d parameter%s, but %s needs %d", n, plural(n), tsig, tsig.params.Len())
		tsig = nil
		countMismatch = true
	}

	scope := NewScope(check.scope, e.Pos(), endPos(e), "lambda")
	scope.isFunc = true
	check.recordScope(e, scope)

	var params []*Var
	variadic := tsig != nil && tsig.variadic
	valid := true
	i := 0
	for _, f := range e.Params.List {
		var annotated Type
		if f.Type != nil {
			ftype := f.Type
			if dd, _ := ftype.(*ast.Ellipsis); dd != nil {
				annotated = &Slice{elem: check.varType(dd.Elt)}
				check.recordTypeAndValue(ftype, typexpr, annotated, nil)
				variadic = true
			} else {
				annotated = check.varType(ftype)
			}
		}
		for _, name := range f.Names {
			var typ Type
			switch {
			case annotated != nil:
				typ = annotated
				if tsig != nil && isValid(typ) {
					if want := tsig.params.vars[i].typ; !Identical(typ, want) {
						check.errorf(f.Type, IncompatibleAssign, "lambda parameter %s has type %s, but %s needs %s", name.Name, typ, tsig, want)
						typ = want // avoid follow-on errors
					}
				}
			case tsig != nil:
				typ = tsig.params.vars[i].typ
			case countMismatch:
				typ = Typ[Invalid]
				valid = false
			default:
				check.errorf(name, CannotInferTypeArgs, "cannot infer type of lambda parameter %s: no function type is expected here (add a type: (%s T) => ...)", name.Name, name.Name)
				typ = Typ[Invalid]
				valid = false
			}
			par := NewParam(name.Pos(), check.pkg, name.Name, typ)
			check.declare(scope, name, par, e.Arrow)
			params = append(params, par)
			i++
		}
	}

	sig := &Signature{scope: scope, params: NewTuple(params...), variadic: variadic}

	resultKnown := tsig != nil && (tsig.results.Len() == 0 || !isParameterized(free, tsig.results))
	if resultKnown {
		vars := make([]*Var, tsig.results.Len())
		for i := range vars {
			vars[i] = newVar(ResultVar, e.Arrow, check.pkg, "", tsig.results.At(i).typ)
		}
		sig.results = NewTuple(vars...)
	}

	if e.Block != nil {
		switch {
		case resultKnown:
		case tsig == nil:
			// A block lambda without context returns nothing.
		default:
			check.errorf(e, CannotInferTypeArgs, "cannot infer the result type of this lambda: use an expression body, or give the type arguments explicitly")
			valid = false
		}
		if valid && !check.conf.IgnoreFuncBodies {
			decl, iota := check.decl, check.iota
			check.later(func() {
				check.funcBody(decl, "<lambda>", sig, e.Block, iota)
			}).describef(e, "lambda")
		}
	} else {
		check.lambdaExprBody(sig, e, resultKnown)
	}

	if !valid {
		x.invalidate()
		x.typ_ = Typ[Invalid]
		return
	}
	x.mode_ = value
	x.typ_ = sig
}

// lambdaExprBody checks the expression body of a lambda right away, in the
// lambda's environment. If the result type is not known, it is set from
// the body's type.
func (check *Checker) lambdaExprBody(sig *Signature, e *ast.LambdaExpr, resultKnown bool) {
	defer func(env environment) {
		check.environment = env
	}(check.environment)
	check.environment = environment{
		decl:    check.decl,
		scope:   sig.scope,
		version: check.version,
		iota:    check.iota,
		sig:     sig,
	}

	if resultKnown {
		if sig.results.Len() == 0 {
			// The body's value, if any, is discarded.
			var y operand
			check.rawExpr(nil, &y, e.Body, nil, false)
			check.exclude(&y, 1<<builtin|1<<typexpr)
			return
		}
		ret := &ast.ReturnStmt{Return: e.Body.Pos(), Results: []ast.Expr{e.Body}}
		check.initVars(sig.results.vars, ret.Results, ret)
		return
	}

	var y operand
	check.rawExpr(nil, &y, e.Body, nil, false)
	check.exclude(&y, 1<<builtin|1<<typexpr)
	var vars []*Var
	switch {
	case !y.isValid():
		vars = []*Var{newVar(ResultVar, e.Arrow, check.pkg, "", Typ[Invalid])}
	case y.mode() == novalue:
	default:
		if t, ok := y.typ().(*Tuple); ok {
			for _, v := range t.vars {
				vars = append(vars, newVar(ResultVar, e.Arrow, check.pkg, "", v.typ))
			}
		} else {
			check.assignment(&y, nil, "lambda result")
			typ := y.typ()
			if !y.isValid() {
				typ = Typ[Invalid]
			}
			vars = []*Var{newVar(ResultVar, e.Arrow, check.pkg, "", typ)}
		}
	}
	sig.results = NewTuple(vars...)
}

// lambdaArgs types the lambda arguments of a call, which have the
// provisional lambdaType, using the (possibly generic) parameter types.
// tparams and targs are the type parameters to infer and the known type
// arguments, as in Checker.arguments. It reports whether all lambda
// arguments could be typed.
func (check *Checker) lambdaArgs(call *ast.CallExpr, tparams []*TypeParam, targs []Type, params *Tuple, args []*operand) bool {
	var pending []int
	for i, a := range args {
		if _, ok := a.typ().(*lambdaType); ok {
			pending = append(pending, i)
		}
	}
	if len(pending) == 0 {
		return true
	}

	desc := check.sprintf("argument to %s", call.Fun)
	typeArg := func(i int, ptyp Type, free []*TypeParam) {
		a := args[i]
		e := a.expr.(*ast.LambdaExpr)
		T := newTarget(ptyp, desc)
		if T == nil {
			check.errorf(e, IncompatibleAssign, "cannot use lambda as %s value in %s", ptyp, desc)
			a.invalidate()
			a.typ_ = Typ[Invalid]
			return
		}
		T.free = free
		check.rawExpr(T, a, e, nil, false)
	}

	if len(tparams) == 0 {
		for _, i := range pending {
			typeArg(i, params.vars[i].typ, nil)
		}
		return true
	}

	for len(pending) > 0 {
		// Infer what we can from the arguments typed so far.
		var ps []*Var
		var as []*operand
		for i, a := range args {
			if _, ok := a.typ().(*lambdaType); !ok {
				ps = append(ps, params.vars[i])
				as = append(as, a)
			}
		}
		known := check.inferPartial(call, tparams, targs, NewTuple(ps...), as)
		if known == nil {
			return false // inference fails anyway; reported by the caller
		}
		smap := makeSubstMap(tparams, known)

		progress := false
		var still []int
		for _, i := range pending {
			ptyp := check.subst(call.Pos(), params.vars[i].typ, smap, nil, check.context())
			if T := newTarget(ptyp, desc); T != nil && T.sig.params.Len() > 0 && isParameterized(tparams, T.sig.params) {
				still = append(still, i) // parameter types not known yet
				continue
			}
			typeArg(i, ptyp, tparams)
			progress = true
		}
		pending = still
		if !progress {
			break
		}
	}

	for _, i := range pending {
		a := args[i]
		check.errorf(a.expr, CannotInferTypeArgs, "cannot infer the parameter types of this lambda in call to %s", call.Fun)
		a.invalidate()
		a.typ_ = Typ[Invalid]
	}
	return len(pending) == 0
}

// inferPartial is like infer, but returns the type arguments it can infer
// (with nil for the others) instead of failing when some are unknown.
// The result is nil if inference fails outright.
func (check *Checker) inferPartial(posn positioner, tparams []*TypeParam, targs []Type, params *Tuple, args []*operand) []Type {
	err := check.newError(CannotInferTypeArgs)
	check.partialInference = true
	defer func() { check.partialInference = false }()
	return check.infer(posn, tparams, targs, params, args, false, err)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// lambdaFieldHint returns the type of the struct field key if value is a
// lambda, so the lambda can take its parameter types from the field.
func (check *Checker) lambdaFieldHint(s *Struct, key *ast.Ident, value ast.Expr) Type {
	if _, ok := value.(*ast.LambdaExpr); !ok || key == nil {
		return nil
	}
	if obj, _, _ := lookupFieldOrMethod(s, false, check.pkg, key.Name, false); obj != nil {
		if fld, _ := obj.(*Var); fld != nil {
			return fld.typ
		}
	}
	return nil
}

// Span returns the source range of the error (G++ addition, for editors).
func (err Error) Span() (start, end token.Pos) {
	start, end = err.go116start, err.go116end
	if !start.IsValid() {
		start = err.Pos
	}
	if !end.IsValid() || end < start {
		end = start
	}
	return
}
