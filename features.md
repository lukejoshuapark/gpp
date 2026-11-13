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

### Adding context

Anything written straight after the `?` adds context to the error. What
happens depends on the context's type:

```go
n := strconv.Atoi(s)? "parsing port"            // parsing port: strconv.Atoi: ...
n := strconv.Atoi(s)? "parsing port of " + name // build messages with +
n := strconv.Atoi(s)? "%w (while parsing)"      // %w places the cause yourself
n := strconv.Atoi(s)? ErrBadPort                // errors.Is matches both
n := strconv.Atoi(s)? err => &ParseError{Line: line, Err: err}
```

| Context | Returns |
|---|---|
| a string | `fmt.Errorf("<message>: %w", err)` |
| a constant string containing `%w` | `fmt.Errorf(<format>, err)`; it must have no other verbs |
| an `error` | `fmt.Errorf("%w: %w", <error>, err)` |
| a `func(error) error` (a lambda gets its type from this) | `<func>(err)` |

- The context must start with a string literal, a name or `func`. In Go,
  nothing else can follow an operand, so this never changes the meaning of
  other code. Anything else after `?` still applies to the result:
  `f()? > 10`, `f()?.Name`, `{k()?: v}`.
- The context is only evaluated when there is an error.
- It extends to the end of the expression (up to a `,`, `)`, `}`, `:` or the
  end of the statement), and must start on the same line as the `?`.

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

## Enums

Enums list a fixed set of variants. Each variant can carry values, listed
like a function's parameters, with or without names:

```go
type Color enum { Red; Green; Blue }

type Shape enum {
	Empty
	Circle(float64)
	Rect(w, h float64)
	Line(Point, Point, string)
}

type Option[T any] enum {
	None
	Some(T)
}
```

- **Construction:** `Color.Red`, `Shape.Circle(2)`, `Shape.Rect(3, 4)`,
  `Option.Some(3)`. A variant with values is a function, so `Shape.Rect(f())`
  works when `f` returns two `float64`s, and `Shape.Rect` can be passed as a
  `func(float64, float64) Shape`. A generic enum's type arguments are
  inferred from the values; for a variant without values, write them out:
  `Option[int].None`. The type name is always required.
- **`switch` on an enum value:** its cases are patterns that can bind the
  value. Without a `default` case the switch must cover every variant, and
  then it counts as a terminating statement:

  ```go
  func (s Shape) Area() float64 {
  	switch s {
  	case Shape.Circle(r):
  		return math.Pi * r * r
  	case Shape.Rect(w, h):
  		return w * h
  	case Shape.Line(_, _, _), Shape.Empty:
  		return 0
  	}
  }
  ```

  A pattern binds every value of its variant, by position, whatever names
  the declaration gave them. `_` ignores a value, and `Shape.Rect` without
  parentheses matches any rect. A case can list several patterns
  (`case Color.Red, Color.Blue:`), but then it can't bind values.
- **`is`:** tests for a variant. In the `&&` chain of an `if` or ternary
  condition it can bind the value, which is then in scope for the rest of
  the condition and the `if` body (or the ternary's first branch):

  ```go
  if s is Shape.Circle(r) && r > 5 { ... }
  if s is Shape.Rect(w, h) && w == h { ... }
  label := s is Shape.Circle(r) ? fmt.Sprint(r) : "-"
  empty := s is Shape.Empty
  ```

- **Values:** enums are plain values, with no allocation. The zero value is the
  first variant, so put a sensible default first. Enums support `==` (and can
  be map keys) when their values do. They print as `Circle(2)` or
  `Rect(3, 4)` through a generated `String` method, unless you declare your
  own.
- **Ordinary named types:** enums can have methods (other than ones named like
  variants) and satisfy interfaces. `type Hue Color` makes a new enum type with
  the same variants.
- **Code outside the enum:** composite literals of an enum type are not
  allowed, and neither is reading its fields, which are internal.

### Enums and JSON

Enums encode to JSON (and text) by their variant names:

- **An enum with no values** encodes as a string. It works as a map key, and
  with anything that uses `encoding.TextMarshaler` (`flag.TextVar`, most YAML
  and TOML libraries):

  ```go
  Color.Green                 // "Green"
  map[Color]int{Color.Red: 1} // {"Red":1}
  ```

- **An enum where any variant has values** encodes every variant as an
  object, with the variant's name under `"type"` and its values, if it has
  any, under `"value"`:

  ```go
  Shape.Empty                 // {"type":"Empty"}
  Shape.Circle(1.5)           // {"type":"Circle","value":1.5}
  Shape.Rect(3, 4)            // {"type":"Rect","value":{"w":3,"h":4}}
  Shape.Line(a, b, "dashed")  // {"type":"Line","value":[{...},{...},"dashed"]}
  ```

  A single value is encoded as itself, named or not. Several named values
  become an object keyed by their names; several unnamed values become an
  array.

- **Renaming a variant:** give it a tag, as on struct fields. The tag can
  only rename; `String` keeps the Go name.

  ```go
  type Shape enum {
  	Empty           `json:"empty"`
  	Circle(float64) `json:"circle"`
  }
  ```

- **Decoding:** unknown names, a missing `"type"` or `"value"`, a missing
  named value, or an array of the wrong length are errors that say what was
  expected. `null` leaves the value unchanged.
  `omitzero` omits the first variant (`omitempty` never applies to enums).
- **Mechanism:** the encoding comes from generated `MarshalText` and
  `UnmarshalText` methods (enums without values) or `MarshalJSON` and
  `UnmarshalJSON` methods (enums with values). Each is generated only if the
  enum doesn't declare it. To encode differently, for example an `Option` as
  `null` or the bare value, declare both `MarshalJSON` and `UnmarshalJSON`.

