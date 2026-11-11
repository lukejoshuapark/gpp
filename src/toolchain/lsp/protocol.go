package lsp

// The subset of the Language Server Protocol G++ uses. See
// https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/

// Position is zero-based; Character counts UTF-16 code units.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

// TextDocumentContentChangeEvent is a full-document change: the server
// advertises full synchronization.
type TextDocumentContentChangeEvent struct {
	Text string `json:"text"`
}

type DidChangeTextDocumentParams struct {
	TextDocument   VersionedTextDocumentIdentifier  `json:"textDocument"`
	ContentChanges []TextDocumentContentChangeEvent `json:"contentChanges"`
}

type DidCloseTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

type DocumentSymbolParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

const (
	SeverityError = 1
)

type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}

type PublishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type Hover struct {
	Contents MarkupContent `json:"contents"`
	Range    *Range        `json:"range,omitempty"`
}

// SymbolKind values.
const (
	SymbolNamespace  = 3
	SymbolMethod     = 6
	SymbolField      = 8
	SymbolEnum       = 10
	SymbolInterface  = 11
	SymbolEnumMember = 22
	SymbolFunction   = 12
	SymbolVariable   = 13
	SymbolConstant   = 14
	SymbolStruct     = 23
)

type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   ServerInfo         `json:"serverInfo"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type CompletionOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
}

type CompletionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// CompletionItemKind values.
const (
	CompletionMethod        = 2
	CompletionFunction      = 3
	CompletionConstructor   = 4
	CompletionField         = 5
	CompletionVariable      = 6
	CompletionInterface     = 8
	CompletionModule        = 9
	CompletionEnum          = 13
	CompletionKeyword       = 14
	CompletionEnumMember    = 20
	CompletionConstant      = 21
	CompletionStruct        = 22
	CompletionTypeParameter = 25
)

type CompletionItem struct {
	Label            string         `json:"label"`
	Kind             int            `json:"kind,omitempty"`
	Detail           string         `json:"detail,omitempty"`
	Documentation    *MarkupContent `json:"documentation,omitempty"`
	SortText         string         `json:"sortText,omitempty"`
	InsertText       string         `json:"insertText,omitempty"`
	FilterText       string         `json:"filterText,omitempty"`
	InsertTextFormat int            `json:"insertTextFormat,omitempty"`

	LabelDetails        *CompletionItemLabelDetails `json:"labelDetails,omitempty"`
	AdditionalTextEdits []TextEdit                  `json:"additionalTextEdits,omitempty"`
}

type CompletionItemLabelDetails struct {
	Description string `json:"description,omitempty"`
}

type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

type WorkspaceEdit struct {
	Changes map[string][]TextEdit `json:"changes"`
}

type CodeActionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
	Context      CodeActionContext      `json:"context"`
}

type CodeActionContext struct {
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type CodeAction struct {
	Title       string         `json:"title"`
	Kind        string         `json:"kind,omitempty"`
	Diagnostics []Diagnostic   `json:"diagnostics,omitempty"`
	IsPreferred bool           `json:"isPreferred,omitempty"`
	Edit        *WorkspaceEdit `json:"edit,omitempty"`
}

type SignatureHelpOptions struct {
	TriggerCharacters   []string `json:"triggerCharacters,omitempty"`
	RetriggerCharacters []string `json:"retriggerCharacters,omitempty"`
}

type SignatureHelp struct {
	Signatures      []SignatureInformation `json:"signatures"`
	ActiveSignature int                    `json:"activeSignature"`
	ActiveParameter int                    `json:"activeParameter"`
}

type SignatureInformation struct {
	Label         string                 `json:"label"`
	Documentation *MarkupContent         `json:"documentation,omitempty"`
	Parameters    []ParameterInformation `json:"parameters,omitempty"`
}

// ParameterInformation labels a parameter by its UTF-16 offsets in the
// signature's label.
type ParameterInformation struct {
	Label [2]int `json:"label"`
}

type CompletionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []CompletionItem `json:"items"`
}

type ServerCapabilities struct {
	TextDocumentSync       TextDocumentSyncOptions `json:"textDocumentSync"`
	HoverProvider          bool                    `json:"hoverProvider"`
	CompletionProvider     *CompletionOptions      `json:"completionProvider,omitempty"`
	SignatureHelpProvider  *SignatureHelpOptions   `json:"signatureHelpProvider,omitempty"`
	CodeActionProvider     bool                    `json:"codeActionProvider"`
	DefinitionProvider     bool                    `json:"definitionProvider"`
	DocumentSymbolProvider bool                    `json:"documentSymbolProvider"`
	FormattingProvider     bool                    `json:"documentFormattingProvider"`
}

type DocumentFormattingParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

const (
	SeverityWarning = 2
	SymbolType      = 5
	SymbolClass     = 5
	SymbolKey       = 20
	SymbolTypeParam = 26

	CompletionText      = 1
	CompletionClass     = 7
	CompletionValue     = 12
	CompletionSnippet   = 15
	CompletionReference = 18
	CompletionOperator  = 24

	InsertTextFormatSnippet = 2
)

type TextDocumentSyncOptions struct {
	OpenClose bool `json:"openClose"`
	Change    int  `json:"change"` // 1 = full document
}

func (d *CompletionItemLabelDetails) description() string {
	if d == nil {
		return ""
	}
	return d.Description
}
