# G++ features

What G++ adds to Go. Everything else is Go: every valid Go program is a
valid G++ program, and G++ code can call Go code (and the other way round)
directly.

## Ternary expressions

```go
abs := n < 0 ? -n : n
label := score > 90 ? "great" : score > 50 ? "ok" : "poor"
```

- Only the chosen branch is evaluated.
- The branches must have a common type: either they have the same type, or
  one is assignable to the other (`err != nil ? err : nil`). Untyped
  constants take their type from the other branch or the context
  (`var f float64 = ok ? 1 : 2.5`). To mix two concrete types in an
  interface, convert one: `ok ? Shape(Square{}) : Circle{}`.
- A ternary `?` must have a space before it. Across lines, break after the
  `?` and the `:`, since a line ending in an expression ends the statement:

  ```go
  msg := ok ?
  	"yes" :
  	"no"
  ```

## Error propagation: `?`

A `?` right after a call (no space) returns early if the call's last result,
an error, is not nil. The other results become the expression's value:

```go
func parsePoint(s string) (Point, error) {
	xs, ys, _ := strings.Cut(s, ",")
	return Point{strconv.Atoi(xs)?, strconv.Atoi(ys)?}, nil
}
```

- `f()?` works when `f` returns `(T, error)` (value `T`), `(T1, T2, error)`
  (assign it: `a, b := f()?`) or just `error` (a statement).
- `err?` works on an error value too.
- It is allowed in functions whose last result is `error`. The function
  returns zero values for its other results, plus the error.
- The error result may be a concrete type, such as `*MyErr`. `?` checks it
  for nil before converting it to `error`, so a nil `*MyErr` is never
  returned as a non-nil error.
- Calls keep Go's left-to-right order: in `f(a(), b()?)`, `a` still runs
  before `b`.
- In a lambda or function literal, `?` returns from that function.

## Lambdas

Short function literals whose parameter types come from the context:

```go
sort.Slice(people, (i, j) => people[i].Age < people[j].Age)
upper := strings.Map(r => unicode.ToUpper(r), s)
names := Map(users, u => u.Name)          // generic: U inferred from the body
add := (a, b int) => a + b                 // types written out
onClick := () => {
	clicks++
}
```

- The forms are `x => expr`, `(x, y) => expr`, `() => expr` and `(x int, y string) => expr`.
  The body is either an expression (its value is the result) or a block.
- Parentheses are required unless there is exactly one untyped parameter.
- Either every parameter has a type, or none do. Go's grouping works:
  `(a, b int)`.
- Untyped parameters take their types from the function type the context
  expects: a call argument, a typed variable, a struct field, a return value
  or a composite literal element.
- For generic callees, type arguments are inferred from the other arguments
  first. An expression body's type can then fill in the rest, as with
  `Map(users, u => u.Name)`.
- A lambda is an ordinary Go func value, so it can be passed to and from Go
  code.

## Expression-bodied functions and methods

A function or method whose body is a single `return` (or a single call) can
be written with `=>`:

```go
func (c Circle) Area() float64 => math.Pi * c.Radius * c.Radius
func square(n int) int => n * n
func parse(s string) (Port, error) => Port(strconv.Atoi(s)?), nil
func greet(name string) => fmt.Println("hello,", name)
```

- `func f() T => x` means `func f() T { return x }`.
- For several results, list the values: `=> v, nil`.
- A function without results takes a single expression (normally a call),
  which runs as a statement.
