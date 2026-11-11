---
name: gpp
description: Writing and editing G++ code (.gpp files), a thin superset of Go. Use when a project contains .gpp files or the user mentions G++/gpp.
---

# G++

G++ is Go plus a few features. `.gpp` files sit in ordinary Go modules, next
to `.go` files if needed, and all valid Go is valid G++. Write idiomatic Go,
and use the features below wherever they make the code shorter or clearer.
Prefer them to the long-hand Go equivalents in `.gpp` files.

## Tooling

Use `gpp` wherever you would use `go`: `gpp build`, `gpp run`, `gpp test`,
`gpp vet` and `gpp get` work the same way. Unknown commands pass through to
`go`.

- `gpp check ./...`: type-check and report errors. Run it after edits.
- `gpp fmt`: format `.gpp` files.
- **`gpp mod tidy`, never `go mod tidy`.** `go mod tidy` can't see imports in
  `.gpp` files and drops requirements.
- Tests go in `_test.gpp` files.

## Features

**Error propagation:** use `?` instead of `if err != nil { return ..., err }`.

```go
n := strconv.Atoi(s)?                       // returns zero values + err on failure
cfg := load(path)? "loading config"          // wraps: "loading config: <err>"
v := parse(s)? "parsing " + name             // build messages with +
f := open(p)? ErrNotFound                    // errors.Is matches ErrNotFound and the cause
x := decode(b)? err => &DecodeError{Err: err} // custom error from a func(error) error
```

- `?` must touch the expression. The context after it must start with a
  string, a name or `func`.
- `?` works only in functions whose last result is `error`.
- Prefer adding context to a bare `?` when it helps the reader of the error.

**Ternary:** `abs := n < 0 ? -n : n`. Use it for simple value choices. There
must be a space before the `?`.

**Lambdas:** parameter types come from the context.

```go
sort.Slice(xs, (i, j) => xs[i] < xs[j])
names := Map(users, u => u.Name)
add := (a, b int) => a + b                  // no context: write the types
http.HandleFunc("/", (w, r) => { ... })      // block body
```

**One-line functions and methods:** use these for single-expression bodies.

```go
func (c Circle) Area() float64 => math.Pi * c.R * c.R
func parse(s string) (int, error) => strconv.Atoi(s)? "parsing", nil
```

**Enums:** a fixed set of variants, each carrying at most one value (use a
struct for more). Use them instead of `iota` constants, sealed interfaces or
tag-plus-fields structs.

```go
type Color enum { Red; Green; Blue }

type Shape enum {
	Empty
	Circle(float64)
	Rect(Size)
}

s := Shape.Circle(2)                 // always qualified by the type
o := Option.Some(3)                  // generic: inferred; Option[int].None otherwise

switch s {                           // exhaustive unless there's a default
case Shape.Circle(r):
	return math.Pi * r * r
case Shape.Rect(sz):
	return sz.W * sz.H
case Shape.Empty:
	return 0
}

if s is Shape.Circle(r) && r > 5 { ... } // binds r for the && chain and the body
```

- Enums are plain values: comparable, usable as map keys, and the zero value
  is the first variant, so put a sensible default first.
- They print as `Circle(2)`.
- In JSON, an enum without values encodes as the variant name. One with
  values encodes as `{"type":"Circle","value":2}`. Rename variants with
  tags: `` Circle(float64) `json:"circle"` ``.
- Never write enum composite literals or touch their `GPP_` fields.

## Don'ts

- Don't run `go mod tidy`, and don't use plain `go build` on `.gpp` code.
- Don't expect Go code to import G++ packages: only G++ can import them.
- Don't edit generated `*.gpp.go` files. They're output from `gpp gen`, for
  inspection only.
