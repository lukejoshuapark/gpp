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

// peek returns the token after the current one.
func (p *parser) peek() token.Token {
	s := p.scanner.Clone()
	for {
		_, tok, _ := s.Scan()
		if tok != token.COMMENT {
			return tok
		}
	}
}

// parseTypeSpecType parses the type in a type declaration, which may be a
// G++ enum: "enum" is only special here, directly followed by "{", where
// Go would not accept it.
func (p *parser) parseTypeSpecType() ast.Expr {
	if p.tok == token.IDENT && p.lit == "enum" && p.peek() == token.LBRACE {
		return p.parseEnumType()
	}
	return p.parseType()
}

// parseEnumType parses enum { Variant; Variant(Type); ... }.
func (p *parser) parseEnumType() *ast.EnumType {
	if p.trace {
		defer un(trace(p, "EnumType"))
	}
	pos := p.pos
	p.next() // enum
	lbrace := p.expect(token.LBRACE)
	var list []*ast.Field
	for p.tok == token.IDENT {
		doc := p.leadComment
		name := p.parseIdent()
		f := &ast.Field{Doc: doc, Names: []*ast.Ident{name}}
		if p.tok == token.LPAREN {
			p.next()
			f.Type = p.parseType()
			if p.tok == token.COMMA {
				p.error(p.pos, "an enum variant carries at most one value: use a struct type for several")
				for p.tok != token.RPAREN && p.tok != token.SEMICOLON && p.tok != token.RBRACE && p.tok != token.EOF {
					p.next()
				}
			}
			p.expect(token.RPAREN)
		}
		if p.tok == token.STRING {
			// a tag, as on struct fields: `json:"name"`
			f.Tag = &ast.BasicLit{ValuePos: p.pos, ValueEnd: p.end(), Kind: token.STRING, Value: p.lit}
			p.next()
		}
		if p.tok == token.SEMICOLON || p.tok == token.RBRACE {
			if p.tok == token.SEMICOLON {
				p.next()
			}
			f.Comment = p.lineComment
		} else {
			p.errorExpected(p.pos, "';', newline or '}' after enum variant")
			p.advance(exprEnd)
		}
		list = append(list, f)
	}
	rbrace := p.expect2(token.RBRACE)
	if len(list) == 0 {
		p.error(lbrace, "an enum needs at least one variant")
	}
	return &ast.EnumType{Enum: pos, Variants: &ast.FieldList{Opening: lbrace, List: list, Closing: rbrace}}
}

// atIs reports whether the current token is the G++ "is" operator: the
// identifier "is" followed by an identifier (the pattern's type), which
// can never follow an operand in Go.
func (p *parser) atIs() bool {
	return p.tok == token.IDENT && p.lit == "is" && p.peek() == token.IDENT
}

// parseIsExpr parses "is Pattern" after x.
func (p *parser) parseIsExpr(x ast.Expr) ast.Expr {
	pos := p.pos
	p.next() // is
	pattern := p.parsePrimaryExpr(nil)
	return &ast.IsExpr{X: x, Is: pos, Pattern: pattern}
}

// atErrorContext reports whether the token after a postfix ? starts an
// error context: a string literal, an identifier (other than the is
// operator) or func.
func (p *parser) atErrorContext() bool {
	switch p.tok {
	case token.STRING, token.FUNC:
		return true
	case token.IDENT:
		return !p.atIs()
	}
	return false
}
