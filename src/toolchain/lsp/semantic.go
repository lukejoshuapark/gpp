package lsp

import (
	"slices"

	"gpp/ast"
	"gpp/types"
)

// Semantic tokens: the server classifies identifiers by what they denote
// (types, functions, parameters, enum variants, ...), which a TextMate
// grammar cannot know. The editor colours them on top of the grammar.

var semanticTokenTypes = []string{
	"namespace", "type", "typeParameter", "parameter", "variable",
	"property", "enumMember", "function", "method",
}

var semanticTokenModifiers = []string{"declaration", "readonly", "defaultLibrary"}

const (
	tokNamespace = iota
	tokType
	tokTypeParameter
	tokParameter
	tokVariable
	tokProperty
	tokEnumMember
	tokFunction
	tokMethod
)

const (
	modDeclaration = 1 << iota
	modReadonly
	modDefaultLibrary
)

type SemanticTokensLegend struct {
	TokenTypes     []string `json:"tokenTypes"`
	TokenModifiers []string `json:"tokenModifiers"`
}

type SemanticTokensOptions struct {
	Legend SemanticTokensLegend `json:"legend"`
	Full   bool                 `json:"full"`
}

type SemanticTokensParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type SemanticTokens struct {
	Data []uint32 `json:"data"`
}

// classify returns the token type and modifiers for an identifier denoting
// obj, or ok == false to leave it to the grammar.
func classify(obj types.Object, def bool) (typ, mods int, ok bool) {
	if def {
		mods |= modDeclaration
	}
	universe := obj.Pkg() == nil
	if universe {
		mods |= modDefaultLibrary
	}
	switch obj := obj.(type) {
	case *types.PkgName:
		return tokNamespace, mods, true
	case *types.TypeName:
		if _, ok := obj.Type().(*types.TypeParam); ok {
			return tokTypeParameter, mods, true
		}
		return tokType, mods, true
	case *types.Var:
		switch {
		case obj.IsField():
			return tokProperty, mods, true
		case obj.Kind() == types.ParamVar || obj.Kind() == types.ResultVar || obj.Kind() == types.RecvVar:
			return tokParameter, mods, true
		}
		return tokVariable, mods, true
	case *types.Const:
		if universe {
			return 0, 0, false // true, false, iota: the grammar's constants
		}
		return tokVariable, mods | modReadonly, true
	case *types.Func:
		if obj.Signature().Recv() != nil {
			return tokMethod, mods, true
		}
		return tokFunction, mods, true
	case *types.Builtin:
		return tokFunction, mods, true
	case *types.Variant:
		return tokEnumMember, mods, true
	}
	return 0, 0, false // nil, labels
}

func (s *server) semanticTokens(p SemanticTokensParams) *SemanticTokens {
	out := &SemanticTokens{Data: []uint32{}}
	_, fi, text := s.file(p.TextDocument.URI)
	if fi == nil {
		return out
	}
	info := fi.pkg.Info
	type token struct {
		pos       Position
		length    int
		typ, mods int
	}
	var toks []token
	ast.Inspect(fi.file, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		obj, def := info.Defs[id], true
		if obj == nil {
			obj, def = info.Uses[id], false
		}
		if obj == nil {
			return false
		}
		typ, mods, ok := classify(obj, def)
		if !ok {
			return false
		}
		r := posRange(fi.prog.Fset, text, id.Pos(), id.End())
		if r.Start.Line != r.End.Line || r.End.Character <= r.Start.Character {
			return false
		}
		toks = append(toks, token{r.Start, r.End.Character - r.Start.Character, typ, mods})
		return false
	})
	slices.SortFunc(toks, func(a, b token) int {
		if a.pos.Line != b.pos.Line {
			return a.pos.Line - b.pos.Line
		}
		return a.pos.Character - b.pos.Character
	})
	var line, char int
	for i, t := range toks {
		if i > 0 && t.pos == toks[i-1].pos {
			continue // the same identifier reached twice
		}
		dl := t.pos.Line - line
		dc := t.pos.Character
		if dl == 0 {
			dc -= char
		}
		out.Data = append(out.Data, uint32(dl), uint32(dc), uint32(t.length), uint32(t.typ), uint32(t.mods))
		line, char = t.pos.Line, t.pos.Character
	}
	return out
}
