# G++ for VS Code

Support for `.gpp` files: highlighting, errors as you type, completion
(members, names in scope, and packages that are not imported yet, which are
imported when you accept them), hover with documentation, go to definition,
find references, rename, signature help, formatting and organizing imports
(both on save by default), quick fixes for missing imports, debugging, and
the outline view.

## Debugging

Debugging needs the Go extension (`golang.go`) and Delve (`dlv`). Press F5
in a `.gpp` file to debug its package, or add a launch configuration:

```json
{
  "type": "gpp",
  "request": "launch",
  "name": "Debug G++ program",
  "program": "${workspaceFolder}",
  "args": [],
  "buildFlags": []
}
```

The extension builds the program with `gpp build` (optimizations off) and
debugs the binary with the Go extension. Breakpoints, stepping and stack
traces use the `.gpp` sources. Enum values show their internal fields
(`GPP_tag`, ...).

Highlighting and bracket/comment handling are declarative (`syntaxes/`,
`language-configuration.json`). Everything else comes from the G++ language
server, `gpp lsp`, which lives with the compiler in `src/toolchain/lsp`; the
extension only starts it (`src/extension.ts`).

## Setup

The extension runs `gpp lsp`, so the `gpp` binary must be on your `PATH` (or
set `gpp.serverPath`):

```
cd src/toolchain && go install ./cmd/gpp
```

## Developing

```
npm install
npm test          # grammar tests, using VS Code's TextMate engine
npm run typecheck
```

Open this folder in VS Code and press F5 to start a window with the
extension loaded. After changing the language server, `go install` it again
and run **G++: Restart Language Server**.

## Installing locally

```
npm run package                      # builds gpp-<version>.vsix
code --install-extension gpp-0.2.0.vsix
```
