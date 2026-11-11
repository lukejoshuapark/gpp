package lexer

import "go/token"

// Tokens G++ adds to Go. They live outside go/token's range so that every
// Go token keeps its go/token value.
const (
	gppTokenBase token.Token = 1000 + iota

	// QUESTION is the ternary operator: a '?' preceded by whitespace.
	QUESTION
	// TRY is the postfix error propagation operator: a '?' that touches
	// the expression before it, as in f()?.
	TRY
	// FAT_ARROW is the lambda arrow: x => x * 2.
	FAT_ARROW
)

// TokenString is token.Token.String, aware of the G++ tokens.
func TokenString(tok token.Token) string {
	switch tok {
	case QUESTION, TRY:
		return "?"
	case FAT_ARROW:
		return "=>"
	}
	return tok.String()
}

// Clone returns a copy of s for looking ahead: scanning the copy does not
// advance s, and reports no errors.
func (s *Scanner) Clone() *Scanner {
	c := *s
	c.err = nil
	return &c
}
