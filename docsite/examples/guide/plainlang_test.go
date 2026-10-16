package guide_test

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/nl"
)

// Card lists the vocabulary a line per member, for a model's prompt. The
// same vocabulary always gives the same text, so it caches as a prefix.
func Example_card() {
	v, err := vocabulary(source())
	if err != nil {
		panic(err)
	}
	card, err := nl.Card(v)
	if err != nil {
		panic(err)
	}
	fmt.Print(card)
	// Output:
	// # Every relation and predicate a program can call, by module: name(arg: type, ...) # what it holds.
	// # ~type is inferred from the relation's rules, "in" marks an argument the caller must bind, [, arg] is optional.
	// # A predicate says what it needs bound before it runs: "filter" needs every argument.
	// absent(value) filter # reports whether the field carried no value at all, which is different from an empty string and from zero; `not absent(?x)` reads "this field is stated"
	// imports(from, to)
	// pkg(name, license)
	//
	// module deps
	// deps.depends_on(a, b) # Everything a package depends on, directly or through others.
	//
	// module num
	// num.add(a: number, b: number, c: number) needs a+b or a+c or b+c # c = a + b, run from any two of the three; units follow the vocabulary's table
	// num.div(a: number, b: number, c: number) needs a+b or a+c or b+c # c = a / b, run from any two of the three; units follow the vocabulary's table
	// num.mul(a: number, b: number, c: number) needs a+b or a+c or b+c # c = a * b, run from any two of the three; units follow the vocabulary's table
	// num.neg(a: number, b: number) needs a or b # b = -a, run from either; the unit carries over
	// num.sub(a: number, b: number, c: number) needs a+b or a+c or b+c # c = a - b, run from any two of the three; units follow the vocabulary's table
	// num.unit(number: number, unit: string) needs number # binds the number's unit ("V", "A"), or absent for a number without one; the number must be bound
	// num.value(number: number, value: number) needs number # binds the number without its unit, so 5 V and 5 A both bind 5; the number must be bound
	//
	// module str
	// str.contains(string: string, substring: string) filter # reports whether a string contains a substring
	// str.distance(string: string, other: string, distance: number) needs string+other # binds the edit (Levenshtein) distance between two strings, in characters; both strings must be bound
	// str.glob(string: string, pattern: string) filter # reports whether the whole string matches a SQLite-style glob (`*` any run, `?` one character, `[a-z]` or `[^a-z]` one of a class, `[[]` a literal `[`)
	// str.match(string: string, regex: string) filter # reports whether the string matches an unanchored regular expression
	// str.prefix(string: string, prefix: string) filter # reports whether a string starts with a prefix
	// str.suffix(string: string, suffix: string) filter # reports whether a string ends with a suffix
}

// Explain turns a program's error into what a model fixes it from: the
// message, the place, and the fix. The CheckError says the same as data.
func Example_explainAnError() {
	v, err := vocabulary(source())
	if err != nil {
		panic(err)
	}
	for _, text := range []string{
		"depends_on(?a, ?b) => ?b",
		"deps.depends_on(?a, ?b),\n  pkg(?b) => ?b",
	} {
		err := datalog.Validate(datalog.MustParse(text), v)
		fmt.Println(nl.Explain(text, err))
		var ce *datalog.CheckError
		if errors.As(err, &ce) {
			fmt.Printf("kind %s, about %s\n\n", ce.Kind, ce.Relation)
		}
	}
	// Output:
	// query: unknown relation "depends_on"; did you mean "deps.depends_on"?
	// line 1, column 1:
	//   depends_on(?a, ?b) => ?b
	//   ^
	// fix: did you mean "deps.depends_on"?
	// kind unknown, about depends_on
	//
	// query: relation "pkg" takes 2 args, got 1
	// line 2, column 3:
	//     pkg(?b) => ?b
	//     ^
	// fix: pkg(name, license)
	// kind arity, about pkg
}

// scriptedModel replies with its drafts in turn. A host's Model would ask a
// real one, a local model through nlmodels or a hosted one.
type scriptedModel struct {
	drafts []string
	n      int
}

func (m *scriptedModel) Name() string { return "scripted" }

func (m *scriptedModel) Complete(_ context.Context, p nl.Prompt) (nl.Completion, error) {
	if m.n > 0 {
		fmt.Printf("told: %s\n\n", p.Turns[len(p.Turns)-1].Text)
	}
	m.n++
	return nl.Completion{Text: m.drafts[m.n-1]}, nil
}

// Translate asks a model for a program, checks it, and hands each error
// back until one checks. With a Base it runs the program too, so an empty
// answer can go to a fallback model.
func Example_translate() {
	v, err := vocabulary(source())
	if err != nil {
		panic(err)
	}
	m := &scriptedModel{drafts: []string{
		`depends_on("app", ?d) => ?d`,
		"```\ndeps.depends_on(\"app\", ?d) => ?d\n```",
	}}
	b := datalog.MustBase(v, source())
	p, tr, err := nl.Translate(context.Background(), "what does app depend on?", v, nil, m, nl.Options{Base: b})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, t := range tr.Tries {
		fmt.Printf("try by %s: %q\n", t.Model, t.Kind)
	}
	fmt.Println(p.Text)
	for _, r := range p.Rows {
		fmt.Print(r.Bind["d"].S, " ")
	}
	fmt.Println()
	// Output:
	// told: query: unknown relation "depends_on"; did you mean "deps.depends_on"?
	// line 1, column 1:
	//   depends_on("app", ?d) => ?d
	//   ^
	// fix: did you mean "deps.depends_on"?
	//
	// Write the whole program again, fixed.
	//
	// try by scripted: "unknown"
	// try by scripted: ""
	// deps.depends_on("app", ?d) => ?d
	// api log util
}

// Grammar is the program syntax as GBNF, for a decoder to keep a model's
// output to text that parses. Its first rules say what a program is.
func Example_grammar() {
	for _, line := range strings.SplitN(nl.Grammar(), "\n", 4)[:3] {
		fmt.Println(line)
	}
	// Output:
	// root ::= (rule sep)* goal
	// sep ::= "; " | ";\n"
	// rule ::= head " :- " literals
}
