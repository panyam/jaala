package datalog

import (
	"reflect"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// spans is Declaire's shape: home(node, repo, uri, start, end), each node's lines in a file, and
// hit(uri, line), a text-search hit. f spans a.go 1-50 with g inside it at 10-20; h spans b.go 1-30.
func spans() *ns.MemSource {
	src := ns.NewMemSource().
		DeclareSchema("home", ns.Schema{Arity: 5, Labels: []string{"node", "repo", "uri", "start", "end"}, Types: []ns.ArgType{{}, {}, {}, {Type: ns.TypeNumber}, {Type: ns.TypeNumber}}}).
		DeclareSchema("hit", ns.Schema{Arity: 2, Labels: []string{"uri", "line"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}})
	for _, h := range []struct {
		n, uri string
		s, e   float64
	}{{"f", "a.go", 1, 50}, {"g", "a.go", 10, 20}, {"h", "b.go", 1, 30}} {
		src.Add("home", ns.Tuple{Vals: []ns.Value{ns.S(h.n), ns.S("r"), ns.S(h.uri), ns.N(h.s), ns.N(h.e)}, Cites: []string{"home:" + h.n}})
	}
	src.Add("hit", ns.Tuple{Vals: []ns.Value{ns.S("a.go"), ns.N(15)}, Cites: []string{"hit:a15"}}).
		Add("hit", ns.Tuple{Vals: []ns.Value{ns.S("b.go"), ns.N(40)}, Cites: []string{"hit:b40"}})
	return src
}

const locAt = `at(?n, ?repo, ?uri, ?line) :- home(?n, ?repo, ?uri, ?s, ?e), ?s <= ?line, ?line <= ?e;`

func TestARuleTakesAnArgumentFromItsCaller(t *testing.T) {
	setLine := Bind(map[Var][]ns.Value{"l": {ns.N(15), ns.N(25)}})
	for _, c := range []struct {
		name, q string
		opts    []Option
		cols    []string
		want    []string
	}{
		{"a constant", locAt + `at(?n, ?r, "a.go", 15) => ?n`, nil, []string{"n"}, []string{"f", "g"}},
		{"a variable another relation binds", locAt + `hit(?u, ?l), at(?n, ?r, ?u, ?l) => ?u, ?l, ?n`, nil, []string{"u", "l", "n"}, []string{"a.go 15 f", "a.go 15 g"}},
		{"written before what binds it", locAt + `at(?n, ?r, ?u, ?l), hit(?u, ?l) => ?u, ?l, ?n`, nil, []string{"u", "l", "n"}, []string{"a.go 15 f", "a.go 15 g"}},
		{"from a rule", locAt + `enclosing(?u, ?l, ?n) :- hit(?u, ?l), at(?n, _, ?u, ?l); enclosing(?u, ?l, ?n) => ?n`, nil, []string{"n"}, []string{"f", "g"}},
		{"passed on to another", locAt + `within(?n, ?line) :- at(?n, _, "a.go", ?line); within(?n, 25) => ?n`, nil, []string{"n"}, []string{"f"}},
		{"bound by the host", locAt + `at(?n, ?r, "a.go", ?l) => ?n`, []Option{Bind(map[Var][]ns.Value{"l": {ns.N(15)}})}, []string{"n"}, []string{"f", "g"}},
		{"bound by the host to several", locAt + `at(?n, ?r, "a.go", ?l) => ?l, ?n`, []Option{setLine}, []string{"l", "n"}, []string{"15 f", "15 g", "25 f"}},
		{"counted distinct", locAt + `at(?n, ?r, "a.go", 15) => count(distinct ?n)`, nil, []string{"count(distinct n)"}, []string{"2"}},
		{"never called", locAt + `hit(?u, ?l) => ?u`, nil, []string{"u"}, []string{"a.go", "b.go"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, extra := range [][]Option{nil, {Witnesses()}} {
				rows, err := both(mustParse(t, c.q), baseFor(std(spans())), append(c.opts, extra...)...)
				if err != nil {
					t.Fatal(err)
				}
				if got := cols(rows, c.cols...); !reflect.DeepEqual(got, c.want) {
					t.Errorf("%d extra options: %v, want %v", len(extra), got, c.want)
				}
			}
		})
	}
}

func TestACallMustBindTheArgumentsItsRuleTakes(t *testing.T) {
	for _, c := range []struct{ q, want string }{
		{locAt + `at(?n, ?r, "a.go", ?l) => ?n`, `at's ?line is an input argument, and the call in the query doesn't bind it`},
		{locAt + `at(?n, ?r, "a.go", _) => ?n`, `at's ?line is an input argument, and the call in the query doesn't bind it`},
		{locAt + `e(?n) :- at(?n, _, "a.go", ?l); e(?n) => ?n`, `at's ?line is an input argument, and the call in rule "e" doesn't bind it`},
		{locAt + `home(?n, _, _, _, _), not at(?n, _, "a.go", 15) => ?n`, `at takes an argument from its caller, so the query can't negate it`},
		{locAt + `at(?n, ?r, "a.go", 15) => count(?n)`, `the query counts its bindings, and at, which takes an argument from its caller, binds variables its head drops`},
		{locAt + `at(?n, ?r, "a.go", 15) => sum(?n)`, `the query counts its bindings, and at, which takes an argument from its caller, binds variables its head drops`},
		{locAt + `at(?n, ?r, "a.go", 15) => avg(?n)`, `the query counts its bindings, and at, which takes an argument from its caller, binds variables its head drops`},
		// Shapes it doesn't inline keep today's message.
		{`r(?x, ?l) :- r(?x, ?m), hit(?x, ?m), ?m < ?l; r(?x, 3) => ?x`, `rule "r" head variable ?l is not bound by a positive body relation`}, // one rule, recursive
		{`a(?n, ?l) :- home(?n, _, _, ?s, _), ?s <= ?l; a(?n, ?l) :- hit(?n, ?l); a(?n, 3) => ?n`, `rule "a" head variable ?l is not bound by a positive body relation`},
		{`u(?n, ?m) :- home(?n, _, _, _, _); u(?n, 3) => ?n`, `rule "u" head variable ?m is not bound by a positive body relation`},
	} {
		q, err := Parse(c.q)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := both(q, baseFor(std(spans()))); err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), "query: ") {
			t.Errorf("%s: err = %v, want it to say %q", c.q, err, c.want)
		}
		if err := Validate(q, std(spans())); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Validate(%s) = %v, want it to say %q", c.q, err, c.want)
		}
	}
	// A variable the host will bind is bound, for Validate as for the Eval.
	if err := ValidateBound(mustParse(t, locAt+`at(?n, ?r, "a.go", ?l) => ?n`), std(spans()), "l"); err != nil {
		t.Errorf("ValidateBound with ?l: %v, want it accepted", err)
	}
}

func TestAModuleMemberTakesAnArgumentFromItsCaller(t *testing.T) {
	v := std(spans())
	if err := v.AddModule("loc", LanguageName, locAt, ""); err != nil {
		t.Fatal(err)
	}
	if err := v.Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
	e, err := v.Lookup("loc.at")
	if err != nil {
		t.Fatal(err)
	}
	var inputs []string
	for _, a := range e.Args {
		if a.Input {
			inputs = append(inputs, a.Name)
		}
	}
	if !reflect.DeepEqual(inputs, []string{"line"}) {
		t.Errorf("loc.at's inputs = %v, want [line]", inputs)
	}
	rows, err := both(mustParse(t, `hit(?u, ?l), loc.at(?n, _, ?u, ?l) => ?n`), baseFor(v))
	if err != nil || col(rows, "n") != "f,g" {
		t.Errorf("hits' enclosing nodes = %v, %v, want f and g", binds(rows), err)
	}
	bad := std(spans())
	if err := bad.AddModule("loc", LanguageName, locAt+`e(?n) :- at(?n, _, "a.go", ?l);`, ""); err != nil {
		t.Fatal(err)
	}
	if err := bad.Check(); err == nil || !strings.Contains(err.Error(), `at's ?line is an input argument, and the call in rule "loc.e" doesn't bind it`) {
		t.Errorf("Check of a module calling at unbound: %v, want it refused", err)
	}
}
