package lsp

import (
	"fmt"
	"go/token"
	"slices"
	"strings"

	"github.com/lukejoshuapark/gpp/src/toolchain/ast"
	"github.com/lukejoshuapark/gpp/src/toolchain/driver"
	"github.com/lukejoshuapark/gpp/src/toolchain/types"
)

// Find references and rename. References are searched for in every
// package of the main module (type-checked through the cache), not just
// the open ones.

type ReferenceParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      struct {
		IncludeDeclaration bool `json:"includeDeclaration"`
	} `json:"context"`
}

type RenameParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	NewName      string                 `json:"newName"`
}

type RenameOptions struct {
	PrepareProvider bool `json:"prepareProvider"`
}

type PrepareRenameResult struct {
	Range       Range  `json:"range"`
	Placeholder string `json:"placeholder"`
}

// objKey identifies an object across loads (which may type-check a
// package more than once): by declaration position and name.
type objKey struct {
	file   string
	offset int
	name   string
}

func keyOf(fset *token.FileSet, obj types.Object) objKey {
	switch o := obj.(type) {
	case *types.Func:
		obj = o.Origin()
	case *types.Var:
		obj = o.Origin()
	}
	p := fset.PositionFor(obj.Pos(), false)
	return objKey{p.Filename, p.Offset, obj.Name()}
}

// A reference is an identifier denoting the object.
type reference struct {
	path      string
	start     token.Pos
	end       token.Pos
	isDecl    bool
	pkg       *driver.Package
	identName string
}

// references returns the identifiers denoting obj in the main module (or
// just the loaded packages, outside a module).
func (s *server) references(snap *snapshot, fi *fileInfo, obj types.Object) []reference {
	fset := fi.prog.Fset
	target := keyOf(fset, obj)
	progs := snap.progs
	if root := moduleRoot(fi.pkg.Dir); root != "" {
		if prog, err := driver.Load(&driver.Config{Dir: root, Overlay: snap.texts, Tests: true, Cache: s.cache}, "./..."); err == nil {
			progs = []*driver.Program{prog}
		}
	}
	var refs []reference
	seen := map[token.Position]bool{}
	for _, prog := range progs {
		for _, p := range prog.Order {
			if p.Info == nil || p.DepOnly {
				continue
			}
			add := func(id *ast.Ident, o types.Object, decl bool) {
				if o == nil || o.Pkg() == nil && obj.Pkg() != nil || keyOf(prog.Fset, o) != target {
					return
				}
				pos := prog.Fset.PositionFor(id.Pos(), false)
				if seen[pos] {
					return
				}
				seen[pos] = true
				refs = append(refs, reference{path: pos.Filename, start: id.Pos(), end: id.End(), isDecl: decl, pkg: p, identName: id.Name})
			}
			for id, o := range p.Info.Defs {
				add(id, o, true)
			}
			for id, o := range p.Info.Uses {
				add(id, o, false)
			}
		}
	}
	slices.SortFunc(refs, func(a, b reference) int {
		if a.path != b.path {
			return strings.Compare(a.path, b.path)
		}
		return int(a.start) - int(b.start)
	})
	return refs
}

func (s *server) findReferences(p ReferenceParams) []Location {
	out := []Location{}
	snap, fi, _, _, obj := s.objectAt(p.TextDocument.URI, p.Position)
	if fi == nil || obj == nil {
		return out
	}
	for _, r := range s.references(snap, fi, obj) {
		if r.isDecl && !p.Context.IncludeDeclaration {
			continue
		}
		out = append(out, Location{URI: pathToURI(r.path), Range: posRange(fi.prog.Fset, snap.text(r.path), r.start, r.end)})
	}
	return out
}

// renameable reports why obj cannot be renamed, if it cannot.
func renameable(fi *fileInfo, obj types.Object) error {
	switch o := obj.(type) {
	case nil:
		return fmt.Errorf("no name here to rename")
	case *types.PkgName:
		return fmt.Errorf("renaming imports is not supported")
	case *types.Label:
		return fmt.Errorf("renaming labels is not supported")
	case *types.Func:
		if o.Name() == "main" && o.Pkg() != nil && o.Pkg().Name() == "main" || o.Name() == "init" {
			return fmt.Errorf("%s cannot be renamed", o.Name())
		}
	}
	if obj.Pkg() == nil {
		return fmt.Errorf("%s is predeclared", obj.Name())
	}
	root := moduleRoot(fi.pkg.Dir)
	decl := fi.prog.Fset.PositionFor(obj.Pos(), false).Filename
	if root == "" || !within(decl, root) {
		return fmt.Errorf("%s is declared outside this module", obj.Name())
	}
	return nil
}

func (s *server) prepareRename(p TextDocumentPositionParams) (*PrepareRenameResult, error) {
	_, fi, text, id, obj := s.objectAt(p.TextDocument.URI, p.Position)
	if fi == nil || id == nil {
		return nil, fmt.Errorf("no name here to rename")
	}
	if err := renameable(fi, obj); err != nil {
		return nil, err
	}
	return &PrepareRenameResult{Range: posRange(fi.prog.Fset, text, id.Pos(), id.End()), Placeholder: id.Name}, nil
}

func (s *server) rename(p RenameParams) (*WorkspaceEdit, error) {
	snap, fi, _, _, obj := s.objectAt(p.TextDocument.URI, p.Position)
	if fi == nil {
		return nil, fmt.Errorf("no name here to rename")
	}
	if err := renameable(fi, obj); err != nil {
		return nil, err
	}
	name := p.NewName
	if !token.IsIdentifier(name) {
		return nil, fmt.Errorf("%q is not a valid name", name)
	}
	if name == obj.Name() {
		return &WorkspaceEdit{Changes: map[string][]TextEdit{}}, nil
	}
	if err := renameConflict(obj, name); err != nil {
		return nil, err
	}
	refs := s.references(snap, fi, obj)
	for _, r := range refs {
		// an unexported name cannot be used from other packages
		if !token.IsExported(name) && r.pkg.Types != nil && obj.Pkg() != nil && r.pkg.Types.Path() != obj.Pkg().Path() {
			return nil, fmt.Errorf("%s is used in package %s, so it must stay exported", obj.Name(), r.pkg.Types.Path())
		}
		// the reference must not be captured by a closer declaration
		if r.pkg.Types != nil && !isMember(obj) && !r.isDecl {
			if scope := r.pkg.Types.Scope().Innermost(r.start); scope != nil {
				if _, other := scope.LookupParent(name, r.start); other != nil && other != obj && closer(other, obj) {
					return nil, fmt.Errorf("renaming would make %s at %s refer to %s instead", obj.Name(), fi.prog.Fset.Position(r.start), types.ObjectString(other, nil))
				}
			}
		}
	}
	// the renamed object must not capture uses of the new name
	if !isMember(obj) {
		for _, prog := range snap.progs {
			for _, pkg := range prog.Order {
				if pkg.Types == nil || pkg.Types.Path() != obj.Pkg().Path() {
					continue
				}
				for id, o := range pkg.Info.Uses {
					if id.Name == name && o != obj && captures(obj, o, id.Pos()) {
						return nil, fmt.Errorf("renaming would make %s at %s refer to the renamed %s", name, prog.Fset.Position(id.Pos()), obj.Name())
					}
				}
			}
		}
	}
	edits := &WorkspaceEdit{Changes: map[string][]TextEdit{}}
	for _, r := range refs {
		uri := pathToURI(r.path)
		edits.Changes[uri] = append(edits.Changes[uri], TextEdit{Range: posRange(fi.prog.Fset, snap.text(r.path), r.start, r.end), NewText: name})
	}
	return edits, nil
}

// isMember reports whether obj is a field, method or enum variant (named
// through a value or type, not looked up in scopes).
func isMember(obj types.Object) bool {
	switch o := obj.(type) {
	case *types.Var:
		return o.IsField()
	case *types.Func:
		return o.Signature().Recv() != nil
	case *types.Variant:
		return true
	}
	return false
}

// renameConflict reports whether name is already declared where obj is.
func renameConflict(obj types.Object, name string) error {
	switch o := obj.(type) {
	case *types.Variant:
		if sig, ok := o.Type().(*types.Signature); ok {
			if en := types.EnumOf(sig.Results().At(0).Type()); en != nil && en.Lookup(name) != nil {
				return fmt.Errorf("the enum already has a variant %s", name)
			}
		} else if en := types.EnumOf(o.Type()); en != nil && en.Lookup(name) != nil {
			return fmt.Errorf("the enum already has a variant %s", name)
		}
		return nil
	case *types.Func:
		if recv := o.Signature().Recv(); recv != nil {
			if other, _, _ := types.LookupFieldOrMethod(recv.Type(), true, o.Pkg(), name); other != nil {
				return fmt.Errorf("%s already has a field or method %s", types.TypeString(recv.Type(), nil), name)
			}
			return nil
		}
	case *types.Var:
		if o.IsField() {
			// the struct's other fields (for named struct types)
			scope := o.Pkg().Scope()
			for _, n := range scope.Names() {
				if st, ok := scope.Lookup(n).Type().Underlying().(*types.Struct); ok {
					for i := range st.NumFields() {
						if st.Field(i) == o {
							if other, _, _ := types.LookupFieldOrMethod(scope.Lookup(n).Type(), true, o.Pkg(), name); other != nil {
								return fmt.Errorf("%s already has a field or method %s", n, name)
							}
						}
					}
				}
			}
			return nil
		}
	}
	if parent := obj.Parent(); parent != nil {
		if other := parent.Lookup(name); other != nil {
			return fmt.Errorf("%s is already declared in this scope", name)
		}
		if parent == obj.Pkg().Scope() {
			// file scopes hold imports
			for f := range parent.Children() {
				if f.Lookup(name) != nil {
					return fmt.Errorf("%s is already the name of an import", name)
				}
			}
		}
	}
	return nil
}

// closer reports whether other is declared in a scope inside obj's (so it
// would hide obj).
func closer(other, obj types.Object) bool {
	if obj.Parent() == nil || other.Parent() == nil {
		return false
	}
	for sc := other.Parent(); sc != nil; sc = sc.Parent() {
		if sc == obj.Parent() {
			return other.Parent() != obj.Parent()
		}
	}
	return false
}

// captures reports whether obj, renamed, would hide other at pos: pos is
// in obj's scope (after its declaration, for locals) and other is declared
// outside it.
func captures(obj, other types.Object, pos token.Pos) bool {
	scope := obj.Parent()
	if scope == nil || other.Parent() == nil {
		return false
	}
	if scope != obj.Pkg().Scope() {
		if !scope.Contains(pos) || pos < obj.Pos() {
			return false
		}
	}
	// other is declared in an enclosing scope of obj's (incl. universe)
	for sc := scope.Parent(); sc != nil; sc = sc.Parent() {
		if sc == other.Parent() {
			return true
		}
	}
	// imports live in file scopes, children of the package scope
	if scope == obj.Pkg().Scope() {
		if _, ok := other.(*types.PkgName); ok {
			return true
		}
	}
	return false
}
