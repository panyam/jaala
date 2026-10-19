package stdlib_test

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

// listsSetup is part(ref, kind) and rail(ref, volts) beside the standard library: two resistors and a
// capacitor, the capacitor rated in volts, R2 on two rails.
func listsSetup(t *testing.T) (*ns.Vocabulary, *ns.MemSource) {
	t.Helper()
	src := ns.NewMemSource().Declare("part", "ref", "kind").
		DeclareSchema("rail", ns.Schema{Arity: 2, Labels: []string{"ref", "volts"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber, Unit: "V"}}})
	for _, p := range [][2]string{{"R1", "resistor"}, {"R2", "resistor"}, {"C1", "capacitor"}} {
		src.Add("part", ns.Tuple{Vals: []ns.Value{ns.S(p[0]), ns.S(p[1])}})
	}
	for _, r := range []struct {
		ref string
		v   float64
	}{{"R1", 3.3}, {"R2", 5}, {"R2", 3.3}, {"C1", 12}} {
		src.Add("rail", ns.Tuple{Vals: []ns.Value{ns.S(r.ref), ns.NU(r.v, "V")}})
	}
	v := ns.MustVocabulary(src)
	if err := v.AddLanguage(datalog.Language); err != nil {
		t.Fatal(err)
	}
	if err := stdlib.Register(v); err != nil {
		t.Fatal(err)
	}
	return v, src
}

func cells(rows []datalog.Row, vars ...datalog.Var) []string {
	var out []string
	for _, r := range rows {
		var vals []string
		for _, x := range vars {
			vals = append(vals, r.Bind[x].S)
		}
		out = append(out, strings.Join(vals, " "))
	}
	sort.Strings(out)
	return out
}

// A list collected in one stage is filtered on its length in the next and unwound back into the rows
// it was built from (#283's done-when): the kinds with more than one part, each part again.
func TestAListIsCollectedFilteredAndUnwound(t *testing.T) {
	v, src := listsSetup(t)
	q := `kinds(?k, list(?r)) :- part(?r, ?k);
	      kinds(?k, ?l), list.length(?l, ?n), ?n > 1, list.member(?l, ?i, ?r) => ?k, ?i, ?r`
	if got, want := cells(eval(t, v, src, q), "k", "i", "r"), []string{"resistor 0 R1", "resistor 1 R2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// control: unwinding every kind's list gives back every part
	all := cells(eval(t, v, src, `kinds(?k, list(?r)) :- part(?r, ?k); kinds(?k, ?l), list.member(?l, _, ?r) => ?r, ?k`), "r", "k")
	parts := cells(eval(t, v, src, `part(?r, ?k) => ?r, ?k`), "r", "k")
	if !reflect.DeepEqual(all, parts) {
		t.Errorf("unwound %v, want %v", all, parts)
	}
}

// A number in a unit goes into a list and comes back out as that number, so it compares and joins as
// one: the rails at 3.3 V, and the parts on a rail above 4 V.
func TestAListKeepsANumbersUnit(t *testing.T) {
	v, src := listsSetup(t)
	q := `rails(list(distinct ?v)) :- rail(_, ?v); rails(?l), list.member(?l, _, ?v), rail(?r, ?v), ?v > 4V => ?r, ?v`
	if got, want := cells(eval(t, v, src, q), "r"), []string{"C1", "R2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	rows := eval(t, v, src, `rail(_, ?v) => list(distinct ?v)`)
	if got := rows[0].Bind["list(distinct v)"].S; got != `[{"num":3.3,"unit":"V"},{"num":5,"unit":"V"},{"num":12,"unit":"V"}]` {
		t.Errorf("list %s", got)
	}
}

// list.at reads one index, a negative one from the end; list.member finds a value's index; anything
// past either end, and a value that isn't a list, gives no row.
func TestListFunctionsReadOneValue(t *testing.T) {
	v, src := listsSetup(t)
	l := fmt.Sprintf("%q", ns.ListText([]ns.Value{ns.S("a"), ns.S("b"), ns.N(3)}))
	for _, c := range []struct {
		q    string
		vars []datalog.Var
		want []string
	}{
		{`list.at(` + l + `, 0, ?x) => ?x`, []datalog.Var{"x"}, []string{"a"}},
		{`list.at(` + l + `, -1, ?x) => ?x`, []datalog.Var{"x"}, []string{"3"}},
		{`list.at(` + l + `, 3, ?x) => ?x`, []datalog.Var{"x"}, nil},
		{`list.at(` + l + `, -4, ?x) => ?x`, []datalog.Var{"x"}, nil},
		{`list.member(` + l + `, ?i, "b") => ?i`, []datalog.Var{"i"}, []string{"1"}},
		{`list.member(` + l + `, 2, ?x) => ?x`, []datalog.Var{"x"}, []string{"3"}},
		{`list.length(` + l + `, ?n) => ?n`, []datalog.Var{"n"}, []string{"3"}},
		{`list.length("[]", ?n) => ?n`, []datalog.Var{"n"}, []string{"0"}},
		{`list.length("a b", ?n) => ?n`, []datalog.Var{"n"}, nil},
		{`list.member("[1,", ?i, ?x) => ?x`, []datalog.Var{"x"}, nil},
	} {
		if got := cells(eval(t, v, src, c.q), c.vars...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %v, want %v", c.q, got, c.want)
		}
	}
}

// A path's nodes are a list, so list.* reads them as path.* does.
func TestAPathsNodesAreAList(t *testing.T) {
	v, src, _ := pathsSetup(t)
	q := `graph.paths("edge", "a", "c", "shortest", 1, 0, ?p), path.nodes(?p, ?l), list.length(?l, ?n), list.at(?l, -2, ?x) => ?n, ?x`
	if got := cells(eval(t, v, src, q), "n", "x"); !reflect.DeepEqual(got, []string{"3 b"}) {
		t.Errorf("got %v", got)
	}
}
