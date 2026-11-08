package datalog

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
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
			"nothing is named maintainer; members taking 2 arguments include edge(from, to), weight(node, w), list.length(list: list, length: number), num.neg(a: number, b: number)", 1, 1},
		{"unknown at an arity nothing takes", `maintainer(?a, ?b, ?c, ?d, ?e, ?f, ?g) => ?a`,
			`query: unknown relation "maintainer"`, KindUnknown, "maintainer", "",
			"nothing is named maintainer, and no member takes 7 arguments; use one the vocabulary lists", 1, 1},
		{"unknown at one argument", `maintainer(?a) => ?a`,
			`query: unknown relation "maintainer"`, KindUnknown, "maintainer", "", "nothing is named maintainer; members taking 1 argument include node(name), absent(value)", 1, 1},
		{"a qualified head", "str.mine(?a) :- node(?a);\nmine(?a) => ?a",
			`query: rule head "str.mine" is a qualified path; a query defines only its own bare relations, and a shared one is registered with AddModule`, KindDefinition, "", "", "name the rule mine, without a module, and call it mine", 1, 1},
		{"the program's rule called through a module", "ownrule(?a) :- node(?a);\nstr.ownrule(?a) => ?a",
			`query: unknown relation "str.ownrule"; "str" holds capitalize, concat, contains, distance, find, glob, lower, match, prefix, replace, split, suffix, upper`, KindUnknown, "str.ownrule", "", "ownrule is the program's own rule; call it ownrule, without a module", 2, 1},
		{"the program's rule called through no module", "out(?a, ?b) :- edge(?a, ?b);\nr(?b) :- foo.out(\"x\", ?b);\nr(?b) => ?b",
			`query: rule "r" reads unknown module "foo" in "foo.out"`, KindUnknown, "foo.out", "", "out is the program's own rule; call it out, without a module", 2, 10},
		{"the program's rule misspelled", "mine(?a) :- node(?a);\nmines(?a) => ?a",
			`query: unknown relation "mines"`, KindUnknown, "mines", "", "did you mean mine, the program's own rule?", 2, 1},
		{"the program's rule misspelled at another arity", "mine(?a) :- node(?a);\nmines(?a, ?b) => ?a",
			`query: unknown relation "mines"`, KindUnknown, "mines", "",
			"nothing is named mines; members taking 2 arguments include edge(from, to), weight(node, w), list.length(list: list, length: number), num.neg(a: number, b: number)", 2, 1},
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
		{"projected and nowhere bound", `node(?a) => ?z`,
			`query: projected ?z is not bound by the goal (nothing in it binds ?z)`,
			KindUnbound, "", "z", "bind ?z with a positive relation in the goal, or drop it from the answer", 1, 13},
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

// A projection over a rule whose head drops the variable used to get the negation message, whose fix
// sends the author to bind the variable in the goal — the wrong repair for a head that has no place
// for the variable (issue 513). The message names the rule, and the fix is the head.
func TestAProjectionOverARuleWhoseHeadDropsAVarNamesTheRule(t *testing.T) {
	text := "mine() :- node(?a);\nmine() => ?a, count(?a)"
	ce := checkErrorOf(t, text)
	want := `query: rule "mine" binds ?a in its body but its head drops it, so the answer cannot project it`
	if ce.Error() != want {
		t.Errorf("message:\n got %s\nwant %s", ce.Error(), want)
	}
	if ce.Kind != KindUnbound || ce.Rule != "mine" || ce.Var != "a" {
		t.Errorf("got kind %q rule %q var %q\nwant kind %q rule \"mine\" var \"a\"", ce.Kind, ce.Rule, ce.Var, KindUnbound)
	}
	if ce.Fix != "write the head as mine(?a) and add ?a to the goal's call, so the rule's head carries ?a out" {
		t.Errorf("fix %q, want the head carried out of the rule", ce.Fix)
	}
	if pos, ok := Locate(text, ce); !ok || pos.Line != 1 || pos.Column != 16 {
		t.Errorf("Locate = %+v %v, want the ?a the rule's body binds, line 1 column 16", pos, ok)
	}
}

// A head with its own arguments keeps them in the fix: writing mine(?y) in place of mine(?x) would
// drop ?x, so the fix spells mine(?x, ?y) and moves the goal's call to the new arity with it.
func TestAHeadDropFixKeepsTheHeadsOwnArgs(t *testing.T) {
	text := "mine(?x) :- node(?x), edge(?x, ?y);\nmine(?x) => ?y"
	ce := checkErrorOf(t, text)
	want := `query: rule "mine" binds ?y in its body but its head drops it, so the answer cannot project it`
	if ce.Error() != want {
		t.Errorf("message:\n got %s\nwant %s", ce.Error(), want)
	}
	if ce.Kind != KindUnbound || ce.Rule != "mine" || ce.Var != "y" {
		t.Errorf("got kind %q rule %q var %q\nwant kind %q rule \"mine\" var \"y\"", ce.Kind, ce.Rule, ce.Var, KindUnbound)
	}
	if ce.Fix != "write the head as mine(?x, ?y) and add ?y to the goal's call, so the rule's head carries ?y out" {
		t.Errorf("fix %q, want the head's own args kept", ce.Fix)
	}
}

// The fix spells the head through the package's printer, so it keeps what the surface syntax keeps:
// an aggregate's distinct, a constant's unit (folded, as the engine holds it), and a string as
// written rather than escaped.
func TestAHeadDropFixUsesThePrinter(t *testing.T) {
	text := `mine(?n, count(distinct ?x), 2km, "a\"b") :- node(?n), node(?x), weight(?n, ?w);` + "\n" +
		`mine(?c, ?d, ?e, ?f) => ?w`
	ce := checkErrorOf(t, text)
	want := `query: rule "mine" binds ?w in its body but its head drops it, so the answer cannot project it`
	if ce.Error() != want {
		t.Errorf("message:\n got %s\nwant %s", ce.Error(), want)
	}
	wantFix := `write the head as mine(?n, count(distinct ?x), 2000m, "a\"b", ?w) and add ?w to the goal's call, so the rule's head carries ?w out`
	if ce.Fix != wantFix {
		t.Errorf("fix:\n got %s\nwant %s", ce.Fix, wantFix)
	}
}

// A relation with two clauses needs the new argument in every head, and the fix says so.
func TestAHeadDropFixNamesEveryClause(t *testing.T) {
	text := "mine(?x) :- node(?x), edge(?x, ?y);\nmine(?x) :- node(?x), weight(?x, ?y);\nmine(?x) => ?y"
	ce := checkErrorOf(t, text)
	want := `write the head as mine(?x, ?y) and add ?y to the goal's call, so the rule's head carries ?y out; mine has 2 clauses, so add ?y to every head`
	if ce.Fix != want {
		t.Errorf("fix:\n got %s\nwant %s", ce.Fix, want)
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
	locatesValue(t)
	_, err := Parse("edge(?a, ?b) =>\n  ?a,")
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("control: Parse gave %v, want a ParseError", err)
	}
	if pos, ok := Locate("", err); !ok || pos != (Position{pe.Offset, pe.Line, pe.Column}) {
		t.Fatalf("Locate(ParseError) = %+v %v, want its own position", pos, ok)
	}
}

// A value an argument refuses is located at that value, in the call holding it, not at the first
// call to its relation. control: the first call holds a value the argument takes.
func locatesValue(t *testing.T) {
	t.Helper()
	src := ns.NewMemSource().DeclareSchema("pkg", ns.Schema{Arity: 3, Labels: []string{"name", "loc", "tier"},
		Types: []ns.ArgType{{}, {Type: ns.TypeNumber}, {Domain: []string{"core", "edge"}}}})
	v := std(src)
	for _, c := range []struct {
		name, text, at string
		arg            int
	}{
		{"a value outside a Domain", `pkg(?a, _, "core"), pkg(?b, _, "middle") => ?a, ?b`, `"middle"`, 2},
		{"text in a number argument", `pkg(?a, 3, _), pkg(?b, "big", _) => ?a, ?b`, `"big"`, 1},
		{"a number compared with text, at the comparison", `pkg(?a, ?n, _), ?n > "big" => ?a`, `?n > "big"`, -1},
		{"text in arithmetic, at the text", `pkg(?a, ?n, _), 0 = ?n * "" => ?a`, `""`, 1},
		{"an expression compared with text, at the text", `pkg(?a, ?n, _), "" = -(?n) => ?a`, `""`, -1},
	} {
		err := Validate(mustParse(t, c.text), v)
		var ce *CheckError
		if !errors.As(err, &ce) {
			t.Fatalf("%s: %v, want a CheckError", c.name, err)
		}
		if ce.Arg != c.arg {
			t.Errorf("%s: Arg = %d, want %d", c.name, ce.Arg, c.arg)
		}
		pos, ok := Locate(c.text, err)
		if want := strings.Index(c.text, c.at); !ok || pos.Offset != want {
			t.Errorf("%s: Locate = %+v %v, want offset %d (%s)", c.name, pos, ok, want, c.at)
		}
	}
}
