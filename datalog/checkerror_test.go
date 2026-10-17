package datalog

import (
	"errors"
	"reflect"
	"testing"
)

// checkErrorOf is the CheckError Validate and every evaluator give for text, failing unless they all
// give one and agree on it field for field.
func checkErrorOf(t *testing.T, text string) *CheckError {
	t.Helper()
	q := mustParse(t, text)
	v := std(graph())
	var first *CheckError
	for name, err := range map[string]error{
		"Validate":  Validate(q, v),
		"Naive":     errorOf(Naive{}, q, baseFor(v)),
		"written":   errorOf(SemiNaive{WrittenOrder: true}, q, baseFor(v)),
		"SemiNaive": errorOf(SemiNaive{}, q, baseFor(v)),
	} {
		var ce *CheckError
		if !errors.As(err, &ce) {
			t.Fatalf("%s: %q gave %T %v, want a *CheckError", name, text, err, err)
		}
		if first == nil {
			first = ce
		} else if !reflect.DeepEqual(ce, first) {
			t.Fatalf("%s: %q gave %+v, another gave %+v", name, text, ce, first)
		}
	}
	return first
}

// Every check error keeps the message it always had, so hosts matching on it see no change, and says
// as data what kind of mistake it is, what it's about, where in the text that is, and how to fix it.
func TestACheckErrorSaysWhatToFixAndWhere(t *testing.T) {
	for _, c := range []struct {
		name, text, msg string
		kind            ErrorKind
		rel, v, fix     string
		line, col       int // 0: Locate finds no place
	}{
		{"arity", `edge(?a) => ?a`,
			`query: relation "edge" takes 2 args, got 1`, KindArity, "edge", "", "edge(from, to)", 1, 1},
		{"arity in a later clause", "r(?a) :- node(?a);\nr(?a), edge(?a, ?b, ?c) => ?b",
			`query: relation "edge" takes 2 args, got 3`, KindArity, "edge", "", "edge(from, to)", 2, 8},
		{"predicate arity", `node(?a), str.prefix(?a) => ?a`,
			`query: str.prefix takes 2 args, got 1`, KindArity, "str.prefix", "", "str.prefix(string: string, prefix: string)", 1, 11},
		{"unknown", `egde(?a, ?b) => ?a`,
			`query: unknown relation "egde"; did you mean "edge"?`, KindUnknown, "egde", "", `did you mean "edge"?`, 1, 1},
		{"unknown with nothing close", `maintainer(?a, ?b) => ?a`,
			`query: unknown relation "maintainer"`, KindUnknown, "maintainer", "",
			"nothing is named maintainer; members taking 2 arguments include edge(from, to), weight(node, w), num.neg(a: number, b: number), num.unit(number: number, unit: string)", 1, 1},
		{"unknown at an arity nothing takes", `maintainer(?a, ?b, ?c, ?d, ?e, ?f, ?g) => ?a`,
			`query: unknown relation "maintainer"`, KindUnknown, "maintainer", "",
			"nothing is named maintainer, and no member takes 7 arguments; use one the vocabulary lists", 1, 1},
		{"unknown at one argument", `maintainer(?a) => ?a`,
			`query: unknown relation "maintainer"`, KindUnknown, "maintainer", "", "nothing is named maintainer; members taking 1 argument include node(name), absent(value)", 1, 1},
		{"a qualified head", "str.mine(?a) :- node(?a);\nmine(?a) => ?a",
			`query: rule head "str.mine" is a qualified path; a query defines only its own bare relations, and a shared one is registered with AddModule`, KindDefinition, "", "", "name the rule mine, without a module, and call it mine", 1, 1},
		{"the program's rule called through a module", "mine(?a) :- node(?a);\nstr.mine(?a) => ?a",
			`query: unknown relation "str.mine"; "str" holds contains, distance, glob, match, prefix, suffix`, KindUnknown, "str.mine", "", "mine is the program's own rule; call it mine, without a module", 2, 1},
		{"the program's rule called through no module", "out(?a, ?b) :- edge(?a, ?b);\nr(?b) :- foo.out(\"x\", ?b);\nr(?b) => ?b",
			`query: rule "r" reads unknown module "foo" in "foo.out"`, KindUnknown, "foo.out", "", "out is the program's own rule; call it out, without a module", 2, 10},
		{"the program's rule misspelled", "mine(?a) :- node(?a);\nmines(?a) => ?a",
			`query: unknown relation "mines"`, KindUnknown, "mines", "", "did you mean mine, the program's own rule?", 2, 1},
		{"the program's rule misspelled at another arity", "mine(?a) :- node(?a);\nmines(?a, ?b) => ?a",
			`query: unknown relation "mines"`, KindUnknown, "mines", "",
			"nothing is named mines; members taking 2 arguments include edge(from, to), weight(node, w), num.neg(a: number, b: number), num.unit(number: number, unit: string)", 2, 1},
		{"a member under a module", `graph.edge(?a, ?b) => ?b`,
			`query: unknown module "graph" in "graph.edge"`, KindUnknown, "graph.edge", "", "edge has no module; call edge(from, to)", 1, 1},
		{"unknown in a rule", "r(?a) :- nod(?a);\nr(?a) => ?a",
			`query: rule "r" reads unknown relation "nod"; did you mean "node"?`, KindUnknown, "nod", "", `did you mean "node"?`, 1, 10},
		{"unbound head variable", "r(?a) :- node(?a);\ns(?a, ?b) :- r(?a);\ns(?a, ?b) => ?a",
			`query: rule "s" head variable ?b is not bound by a positive body relation`, KindUnbound, "s", "b",
			"add a relation to s's body that binds ?b, or drop ?b from its head", 2, 1},
		{"unbound comparison operand", `node(?a), ?b < 3 => ?a`,
			`query: comparison operand is unbound (a variable must appear in a relation before it is compared)`, KindUnbound, "", "b",
			"add a relation that binds ?b to the same body", 1, 11},
		{"negation with no anchor", `node(?a), not edge(?b, ?c) => ?a`,
			"query: negated relation \"edge\" shares no variable with the rest of the query (?b appears only inside the `not`, so the negation has nothing to range over and matches either every row or none)",
			KindUnbound, "edge", "b", "bind ?b with a positive relation outside the not, or write _ for it if any value will do", 1, 15},
		{"projected and unbound", `node(?a), not edge(?a, ?b) => ?b`,
			`query: projected ?b is not bound by a positive relation (a variable used only under negation is existential and cannot be selected)`,
			KindUnbound, "", "b", "bind ?b with a positive relation in the goal, or drop it from the answer", 1, 24},
		{"recursion through negation", "p(?a) :- node(?a), not q(?a);\nq(?a) :- node(?a), not p(?a);\np(?a) => ?a",
			`query: rules are not stratifiable (recursion through negation: p, q)`, KindStratify, "", "", "", 0, 0},
		{"order by a column not selected", `node(?a), edge(?a, ?b) => ?a order by ?b`,
			`query: order by ?b, which is not an answer column (sort on a column the projection selects)`, KindAnswer, "", "b",
			"select ?b after =>, or order by a column that is", 1, 20},
	} {
		t.Run(c.name, func(t *testing.T) {
			ce := checkErrorOf(t, c.text)
			if ce.Error() != c.msg {
				t.Errorf("message moved:\n got %s\nwant %s", ce.Error(), c.msg)
			}
			if ce.Kind != c.kind || ce.Relation != c.rel || ce.Var != c.v || ce.Fix != c.fix {
				t.Errorf("got kind %q relation %q var %q fix %q\nwant kind %q relation %q var %q fix %q", ce.Kind, ce.Relation, ce.Var, ce.Fix, c.kind, c.rel, c.v, c.fix)
			}
			pos, ok := Locate(c.text, ce)
			if c.line == 0 {
				if ok {
					t.Errorf("Locate found %+v, want no place", pos)
				}
				return
			}
			if !ok || pos.Line != c.line || pos.Column != c.col {
				t.Errorf("Locate = %+v %v, want line %d column %d", pos, ok, c.line, c.col)
			}
		})
	}
}

func TestLocateFindsTheCallTheErrorIsAbout(t *testing.T) {
	text := "# r reads ?b too\nr(?b) :- node(?b), net.pins(?b, ?c);\ns(?a, ?b) :- node(?a);\nr(?x), net.pins(?x) => ?x"
	for _, c := range []struct {
		name      string
		err       *CheckError
		line, col int
	}{
		{"a dotted name, by its argument count", &CheckError{Relation: "net.pins", Args: 1}, 4, 8},
		{"control: the first call when the count matches none", &CheckError{Relation: "net.pins", Args: 5}, 2, 20},
		{"a variable in the rule named", &CheckError{Rule: "s", Var: "b", Args: -1}, 3, 7},
		{"control: the first ?b without a rule", &CheckError{Var: "b", Args: -1}, 2, 3},
		{"a name only a dotted name ends in", &CheckError{Relation: "pins", Args: -1}, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			pos, ok := Locate(text, c.err)
			if c.line == 0 {
				if ok {
					t.Fatalf("Locate found %+v, want no place", pos)
				}
				return
			}
			if !ok || pos.Line != c.line || pos.Column != c.col {
				t.Fatalf("Locate = %+v %v, want line %d column %d", pos, ok, c.line, c.col)
			}
		})
	}
	_, err := Parse("edge(?a, ?b) =>\n  ?a,")
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("control: Parse gave %v, want a ParseError", err)
	}
	if pos, ok := Locate("", err); !ok || pos != (Position{pe.Offset, pe.Line, pe.Column}) {
		t.Fatalf("Locate(ParseError) = %+v %v, want its own position", pos, ok)
	}
}
