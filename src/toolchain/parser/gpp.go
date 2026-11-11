package parser

import (
	"go/token"

	"gpp/ast"
	"gpp/lexer"
)

// G++ additions to the parser.

// atLambda reports whether the '(' at the current position starts a lambda
// parameter list, i.e. the matching ')' is followed by '=>'. It looks ahead
// on a copy of the scanner, giving up as soon as it sees a token that
// cannot appear in a parameter list.
func (p *parser) atLambda() bool {
	if p.tok != token.LPAREN {
		return false
	}
	s := p.scanner.Clone()
	depth := 1
	for {
		_, tok, _ := s.Scan()
		switch tok {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
			if depth == 0 {
				if tok != token.RPAREN {
					return false
				}
				for {
					_, tok, _ = s.Scan()
					if tok != token.COMMENT {
						return tok == lexer.FAT_ARROW
					}
				}
			}
		case token.IDENT, token.COMMA, token.MUL, token.PERIOD, token.ELLIPSIS,
			token.FUNC, token.MAP, token.CHAN, token.STRUCT, token.INTERFACE,
			token.ARROW, token.SEMICOLON, token.INT, token.COMMENT, token.STRING:
			// may appear in a parameter list (STRING: struct tags)
		default:
			return false
		}
	}
}

// parseLambda parses a parenthesized lambda: (params) => body.
func (p *parser) parseLambda() ast.Expr {
	if p.trace {
		defer un(trace(p, "Lambda"))
	}
	lparen := p.expect(token.LPAREN)
	var fields []*ast.Field
	var pending []*ast.Ident // names waiting for a type: (x, y int)
	typed := false
	p.exprLev++
	for p.tok != token.RPAREN && p.tok != token.EOF {
		name := p.parseIdent()
		if p.tok == token.COMMA || p.tok == token.RPAREN {
			pending = append(pending, name)
		} else {
			var typ ast.Expr
			if p.tok == token.ELLIPSIS {
				pos := p.pos
				p.next()
				typ = &ast.Ellipsis{Ellipsis: pos, Elt: p.parseType()}
			} else {
				typ = p.parseType()
			}
			fields = append(fields, &ast.Field{Names: append(pending, name), Type: typ})
			pending = nil
			typed = true
		}
		if !p.atComma("lambda parameter list", token.RPAREN) {
			break
		}
		p.next()
	}
	p.exprLev--
	if len(pending) > 0 {
		if typed {
			p.error(pending[0].Pos(), "missing type for lambda parameter "+pending[0].Name+": either all or none of the parameters need types")
		}
		for _, name := range pending {
			fields = append(fields, &ast.Field{Names: []*ast.Ident{name}})
		}
	}
	rparen := p.expectClosing(token.RPAREN, "lambda parameter list")
	return p.parseLambdaBody(&ast.FieldList{Opening: lparen, List: fields, Closing: rparen})
}

// parseLambdaBody parses "=> body" after the parameters of a lambda.
func (p *parser) parseLambdaBody(params *ast.FieldList) ast.Expr {
	arrow := p.expect(lexer.FAT_ARROW)
	lam := &ast.LambdaExpr{Params: params, Arrow: arrow}
	if p.tok == token.LBRACE {
		p.exprLev++
		lam.Block = p.parseBody()
		p.exprLev--
	} else {
		lam.Body = p.parseExpr()
	}
	return lam
}

// parseArrowBody parses the "=> expr" body of a function declaration,
// returning the equivalent block: { return expr, ... }, or { expr } if
// the function has no results.
func (p *parser) parseArrowBody(results *ast.FieldList) *ast.BlockStmt {
	arrow := p.expect(lexer.FAT_ARROW)
	p.exprLev++
	list := p.parseList(true)
	p.exprLev--
	var s ast.Stmt
	if results != nil && len(results.List) > 0 {
		s = &ast.ReturnStmt{Return: list[0].Pos(), Results: list}
	} else {
		if len(list) > 1 {
			p.error(list[1].Pos(), "a function without results has a single expression after =>")
		}
		s = &ast.ExprStmt{X: list[0]}
	}
	return &ast.BlockStmt{Lbrace: arrow, List: []ast.Stmt{s}}
}
