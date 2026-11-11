// Tokenizes G++ snippets with the same TextMate engine VS Code uses and
// checks the scopes the grammar assigns. Run with `npm test`.
import assert from "node:assert/strict";
import fs from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";
import vsctm from "vscode-textmate";
import oniguruma from "vscode-oniguruma";

const require = createRequire(import.meta.url);
const here = path.dirname(fileURLToPath(import.meta.url));
const grammarPath = path.join(here, "..", "syntaxes", "gpp.tmLanguage.json");

await oniguruma.loadWASM(fs.readFileSync(require.resolve("vscode-oniguruma/release/onig.wasm")).buffer);
const registry = new vsctm.Registry({
  onigLib: Promise.resolve({
    createOnigScanner: (patterns) => new oniguruma.OnigScanner(patterns),
    createOnigString: (s) => new oniguruma.OnigString(s),
  }),
  loadGrammar: async (scope) =>
    scope === "source.gpp" ? vsctm.parseRawGrammar(fs.readFileSync(grammarPath, "utf8"), grammarPath) : null,
});
const grammar = await registry.loadGrammar("source.gpp");

// tokenize returns [text, innermost scope] pairs for every token.
function tokenize(src) {
  let stack = vsctm.INITIAL;
  const out = [];
  for (const line of src.split("\n")) {
    const r = grammar.tokenizeLine(line, stack);
    for (const t of r.tokens) {
      out.push([line.slice(t.startIndex, t.endIndex), t.scopes[t.scopes.length - 1]]);
    }
    stack = r.ruleStack;
  }
  return out;
}

// expect checks that each [text, scope] appears, in order, in src's tokens.
function expect(src, ...want) {
  const toks = tokenize(src);
  let i = 0;
  for (const [text, scope] of want) {
    while (i < toks.length && toks[i][0] !== text) i++;
    assert.ok(i < toks.length, `${JSON.stringify(src)}: token ${JSON.stringify(text)} not found in ${JSON.stringify(toks)}`);
    assert.equal(toks[i][1], scope, `${JSON.stringify(src)}: scope of ${JSON.stringify(text)}`);
    i++;
  }
}

// Go
expect("package main", ["package", "keyword.other.package.gpp"], ["main", "entity.name.namespace.gpp"]);
expect('import "fmt"', ["import", "keyword.other.import.gpp"], ['"', "punctuation.definition.string.begin.gpp"]);
expect("func add(a, b int) int {",
  ["func", "storage.type.function.gpp"], ["add", "entity.name.function.gpp"], ["int", "support.type.primitive.gpp"]);
expect("func (p *Point) Dist() float64 {",
  ["func", "storage.type.function.gpp"], ["Point", "variable.other.gpp"], ["Dist", "entity.name.function.gpp"],
  ["float64", "support.type.primitive.gpp"]);
expect("func Map[T, U any](xs []T) []U {", ["Map", "entity.name.function.gpp"], ["any", "support.type.primitive.gpp"]);
expect("type Point struct { X, Y int }",
  ["type", "storage.type.gpp"], ["Point", "entity.name.type.gpp"], ["struct", "storage.type.gpp"]);
expect("total := 0x_FF + 1.5e3 + 42 + 0b101",
  [":=", "keyword.operator.assignment.define.gpp"], ["0x_FF", "constant.numeric.hex.gpp"],
  ["1.5e3", "constant.numeric.float.gpp"], ["42", "constant.numeric.integer.gpp"], ["0b101", "constant.numeric.binary.gpp"]);
expect('fmt.Printf("n=%d\\n", x)',
  ["fmt", "variable.other.gpp"], ["Printf", "entity.name.function.call.gpp"], ["%d", "constant.other.placeholder.gpp"],
  ["\\n", "constant.character.escape.gpp"]);
expect("c := 'a'; s := `raw\\n`", ["'a'", "constant.character.gpp"], ["raw\\n", "string.quoted.raw.gpp"]);
expect("if ok { return true } else { x += 1 }",
  ["if", "keyword.control.gpp"], ["return", "keyword.control.gpp"], ["true", "constant.language.boolean.gpp"],
  ["else", "keyword.control.gpp"], ["+=", "keyword.operator.assignment.compound.gpp"]);
expect("for i, x := range xs { go f(<-ch); defer close(ch) }",
  ["for", "keyword.control.gpp"], ["range", "keyword.control.gpp"], ["go", "keyword.control.gpp"],
  ["<-", "keyword.operator.channel.gpp"], ["close", "support.function.builtin.gpp"]);
expect("/* spans\nlines */ func // trailing",
  ["/*", "punctuation.definition.comment.gpp"], ["lines ", "comment.block.gpp"], ["func", "storage.type.function.gpp"],
  ["//", "punctuation.definition.comment.gpp"]);
expect("var m map[string][]int = nil", ["var", "storage.type.gpp"], ["map", "storage.type.gpp"], ["nil", "constant.language.nil.gpp"]);
expect("n := len(xs) + int(f)", ["len", "support.function.builtin.gpp"], ["int", "support.type.primitive.gpp"]);

// G++
expect("n := strconv.Atoi(s)?", ["Atoi", "entity.name.function.call.gpp"], ["?", "keyword.operator.try.gpp"]);
expect("x := n > 0 ? n : -n", ["?", "keyword.operator.ternary.gpp"], [":", "punctuation.separator.colon.gpp"]);
expect("v := f()? > 0 ? a : b", ["?", "keyword.operator.try.gpp"], ["?", "keyword.operator.ternary.gpp"]);
expect("double := x => x * 2", ["x", "variable.parameter.gpp"], ["=>", "keyword.operator.arrow.gpp"]);
expect("add := (a, b int) => a + b", ["int", "support.type.primitive.gpp"], ["=>", "keyword.operator.arrow.gpp"]);
expect("sort.Slice(xs, (i, j) => xs[i] < xs[j])", ["Slice", "entity.name.function.call.gpp"], ["=>", "keyword.operator.arrow.gpp"]);

expect("func (c Circle) Area() float64 => math.Pi * c.Radius",
  ["Area", "entity.name.function.gpp"], ["float64", "support.type.primitive.gpp"], ["=>", "keyword.operator.arrow.gpp"]);
expect("func (p P) Get() Point => p.pt", ["Point", "variable.other.gpp"], ["=>", "keyword.operator.arrow.gpp"]);
expect("b := Button{OnClick: n => n}", ["n", "variable.parameter.gpp"], ["=>", "keyword.operator.arrow.gpp"]);
expect("return x => x + 1", ["x", "variable.parameter.gpp"]);

console.log("grammar tests passed");
