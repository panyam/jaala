package datalog

import (
	"math"
	"reflect"
	"testing"

	"github.com/panyam/jaala/ns"
)

// avg counts every binding, as sum does: GND's pins are numbered 1, 1, 1 and 2 (C1 has both on it),
// so its average is 1.25, and over distinct numbers 1.5.
func TestAvgCountsEveryBinding(t *testing.T) {
	got := cols(eval(t, netlist(), `pin(_, ?net, ?n) => ?net, avg(?n), avg(distinct ?n), sum(?n), count(?n)`), "net", "avg(n)", "avg(distinct n)", "sum(n)", "count(n)")
	want := []string{"GND 1.25 1.5 5 4", "SCL 3 3 3 1", "SDA 2 2 4 2", "VCC 1.5 1.5 3 2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("per net %v, want %v", got, want)
	}
}

func TestAvgInARuleAndABodyAggregate(t *testing.T) {
	for _, c := range []struct {
		name, query string
		want        []string
	}{
		{"head", `mean(?net, avg(?n)) :- pin(_, ?net, ?n); mean(?net, ?a), ?net = "GND" => ?net, ?a`, []string{"GND 1.25"}},
		{"body", `pin(_, ?net, 3), ?a = avg(?n) : { pin(_, ?net, ?n) } => ?net, ?a`, []string{"SCL 3"}},
		{"body over a net", `pin("C1", ?net, 1), ?a = avg(?n) : { pin(_, ?net, ?n) } => ?net, ?a`, []string{"GND 1.25"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := cols(eval(t, netlist(), c.query), "net", "a"); !reflect.DeepEqual(got, c.want) {
				t.Errorf("%s = %v, want %v", c.query, got, c.want)
			}
		})
	}
}

// Like min and max, avg over no numbers is absent, saying why, and over numbers in different units
// absent too; over one unit it keeps it.
func TestAvgWithNoNumbersOrMixedUnitsIsAbsent(t *testing.T) {
	rows := eval(t, params(), `param(?p, _, ?v) => ?p, avg(?v)`)
	want := []string{"U1 -(mixed units: A, V, ohm)", "U2 2.5[V]", "U3 1.625[V]", "U4 -(mixed units: A, V)"}
	if got := withUnits(rows, "p", "avg(v)"); !reflect.DeepEqual(got, want) {
		t.Errorf("per part = %v, want %v", got, want)
	}
	for _, c := range []struct{ query, want string }{
		{`param(?p, ?s, _), ?p = "U2" => avg(?s)`, "-(no numbers)"},
		{`param(?p, _, ?v), ?p = "U9" => avg(?v)`, "-(no rows)"},
		{`lo(avg(?v)) :- param("U9", _, ?v); lo(?a) => ?a`, "-"},
	} {
		rows := eval(t, params(), c.query)
		if len(rows) != 1 {
			t.Fatalf("%s: %d rows, want 1", c.query, len(rows))
		}
		for col := range rows[0].Bind {
			if got := withUnits(rows, string(col)); got[0] != c.want {
				t.Errorf("%s = %v, want %s", c.query, got, c.want)
			}
		}
	}
}

func TestAvgOfANaNIsNaN(t *testing.T) {
	src := ns.NewMemSource().DeclareSchema("w", ns.Schema{Arity: 2, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}})
	src.Add("w", ns.Tuple{Vals: []ns.Value{ns.S("a"), ns.N(1)}})
	src.Add("w", ns.Tuple{Vals: []ns.Value{ns.S("b"), ns.N(math.NaN())}})
	src.Add("w", ns.Tuple{Vals: []ns.Value{ns.S("c"), ns.N(3)}})
	q := mustParse(t, `w(?k, ?x) => avg(?x)`)
	for _, e := range []Evaluator{Naive{}, SemiNaive{}} {
		rows, err := e.Eval(bg, q, baseFor(std(src)))
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Bind["avg(x)"].Num == nil || !math.IsNaN(*rows[0].Bind["avg(x)"].Num) {
			t.Errorf("%T: avg = %v, want NaN", e, rows)
		}
	}
	if got := col(eval(t, src, `w(?k, ?x), ?k != "b" => avg(?x)`), "avg(x)"); got != "2" {
		t.Errorf("control: avg without the NaN = %s, want 2", got)
	}
}

// A store summarizing the goal hands avg each value with its count, so it averages what memory does.
func TestAvgOverASummary(t *testing.T) {
	m := netlist()
	q := mustParse(t, `pin(_, ?net, ?n) => ?net, avg(?n)`)
	want, err := Naive{}.Eval(bg, q, baseFor(std(m)))
	if err != nil {
		t.Fatal(err)
	}
	src := pushingAt(m)
	got, err := SemiNaive{}.Eval(bg, q, MustBase(std(m), src))
	if err != nil {
		t.Fatal(err)
	}
	if len(src.summarized) != 1 || len(src.summarized[0].Values) != 1 {
		t.Fatalf("control: summaries %+v, want one asking for ?n's values", src.summarized)
	}
	if g, w := cols(got, "net", "avg(n)"), cols(want, "net", "avg(n)"); !reflect.DeepEqual(g, w) || w[0] != "GND 1.25" {
		t.Errorf("summarized %v, in memory %v; want the same, GND 1.25", g, w)
	}
}
