# G++

G++ is a thin superset of Go. Every Go program is a G++ program; G++ adds a
few conveniences on top, listed in [features.md](features.md):

```go
func parsePort(s string) (int, error) {
	n := strconv.Atoi(s)?                       // error propagation
	return n > 0 && n < 65536 ? n : 8080, nil    // ternary
}

sort.Slice(ports, (i, j) => ports[i] < ports[j]) // lambdas
```

G++ source files end in `.gpp` and live in ordinary Go modules, next to
`.go` files if you like: a package may mix both. The `gpp` command
translates `.gpp` files to Go and hands the result to the go command.

## Install

Requires Go 1.27 (G++'s front end is forked from it) and, for the editor,
Node.js and VS Code.

```
./install.sh        # installs gpp, then builds and installs the VS Code extension
```

or just the toolchain: `cd src/toolchain && go install ./cmd/gpp`.

## Using gpp

`gpp` is a drop-in for the go command:

```
gpp run .                  # build and run (also: gpp run main.gpp)
gpp build ./...            # build; flags as for go build
gpp test ./...             # tests in _test.gpp (and _test.go) files
gpp install, gpp vet       # likewise
gpp check ./...            # type-check only, and report errors
gpp fmt [-l] [paths]       # format .gpp files (gofmt style); "gpp fmt -" for stdin
gpp gen [packages]         # write the Go translation next to each .gpp file (x.gpp.go)
gpp lsp                    # language server, on stdin/stdout
gpp mod init example.com/m # anything else is passed to go
```

Projects are plain Go modules (`go.mod`, `go.sum`, `gpp get`, `gpp mod tidy`).
Dependencies may contain `.gpp` files too: gpp translates them as well.
To publish a G++ package for plain Go users, commit the output of `gpp gen`.

## How it works

`src/toolchain` is the Go module `gpp`. Its compiler pipeline is Go's own
front end, forked from Go 1.27 and extended:

| package   | role | forked from |
|-----------|------|-------------|
| `lexer`   | tokens, including G++'s `?` (postfix and ternary) and `=>` | `go/scanner` |
| `ast`     | syntax trees, plus `TernaryExpr`, `TryExpr`, `LambdaExpr`, `EnumType`, `IsExpr` | `go/ast` |
| `parser`  | parsing | `go/parser` |
| `types`   | type checking, including lambda inference (`types/gpp.go`) and enums (`types/gpp_enum.go`) | `go/types` |
| `printer`, `format` | formatting (`gpp fmt`) | `go/printer`, `go/format` |
| `lower`   | rewrites G++ constructs into Go and prints Go with `//line` directives | — |
| `driver`  | loads packages (via `go list`), type-checks, lowers, builds | — |
| `lsp`     | the language server | — |
| `cmd/gpp` | the command | — |

Forking the front end, rather than writing a new one, is what keeps G++ a
strict superset. Go code parses and type-checks exactly as it does in Go,
and `gpp check std` type-checks the whole standard library. Changes to the
forked packages are marked `G++`, and new code goes in `gpp.go` files where
possible, which keeps it easy to rebase onto newer Go releases. The Go
license is in `src/toolchain/LICENSE-GO`.

A build works like this:

1. The driver gives `go list` a stub `.go` file (package clause and imports)
   for each `.gpp` file through an overlay, so the go command sees G++
   packages. It then parses and type-checks every package from source.
   Dependencies are checked without function bodies.
2. `lower` translates each `.gpp` file to Go, using the type information:
   - ternaries become immediately-called function literals;
   - `?` becomes `v, err := ...; if err != nil { return ..., err }` ahead of
     the statement;
   - lambdas become function literals with their inferred types.
   The output carries `//line` directives, so compiler errors and panic
   stack traces point at the `.gpp` source.
3. The translations are written to the cache (`$GPPCACHE`, or `gpp` in the
   user cache directory). The go command runs with `-overlay`, which puts
   them in place of the `.gpp` files. Nothing is written next to your
   sources.

The language server uses the same driver, with the editor's unsaved text as
an overlay. It keeps type-checked packages between edits, so only the
edited package and its dependents are checked again.

## Editor support

`src/vscode` is the VS Code extension: a TextMate grammar, plus a client for
`gpp lsp` that provides:

- semantic highlighting: names coloured by what they denote (types,
  functions, parameters, enum variants, ...);
- errors as you type;
- completion: members, names in scope, and packages not imported yet, which
  are added to the imports when accepted;
- hover with documentation (also on `?` and `=>`, showing types);
- go to definition, including into the standard library;
- signature help;
- formatting on save;
- quick fixes for missing imports;
- the outline.

See `src/vscode/README.md`.

## Tests

```
cd src/toolchain && go test ./...   # compiler, driver and language server
cd src/vscode && npm test           # grammar
```

The golden tests:

- Each `src/toolchain/testdata/run/<name>/` program must print its `stdout`
  file.
- Each `src/toolchain/testdata/errors/<name>/` package must produce exactly
  its `errors` file.
- Run `go test ./driver -update` to rewrite both after an intended change.

The language server tests drive it over the protocol, as an editor would.

New language features should land with editor support. Update the grammar in
`src/vscode/syntaxes/gpp.tmLanguage.json` and its tests, and record the
feature in `features.md`.

## Known limitations

- gopls does not read `.gpp` files. In a package that mixes `.go` and
  `.gpp` files, gopls reports names declared in `.gpp` files as undefined
  in the `.go` files. `gpp check` and the build are unaffected.
- `.gpp` files cannot use cgo (`import "C"`). Go files in the same package
  can.
- `?` cannot be used in `switch` case expressions or in a `for` loop's post
  statement.
- A lambda or ternary whose type mentions an unexported type from another
  package cannot be translated. Give the expression an explicit type.
- Enums are structs to the go command: Go code (and `encoding/json`) sees
  their internal `GPP_` fields. Generic enums get a `String` method when
  translated, but G++ code can't call it or use it to satisfy an interface,
  because the type checker doesn't know about it.
