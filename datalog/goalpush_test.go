package datalog

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// linesStores is the plan's example: store A holds a1 and a2 at 7 lines and a3 at 9, store B b1 at 7
// and b2 at 3, each fact citing its id, in one federation.
func linesStores() ([]*pushing, *ns.Federation, *ns.MemSource) {
	whole := ns.NewMemSource().Declare("attr", "id", "key", "value")
	mem := func(facts ...[2]string) *ns.MemSource {
		m := ns.NewMemSource().Declare("attr", "id", "key", "value")
		for _, f := range facts {
			t := ns.Tuple{Vals: []ns.Value{ns.S(f[0]), ns.S("lines"), ns.S(f[1])}, Cites: []string{f[0]}}
			m.Add("attr", t)
			whole.Add("attr", t)
		}
		return m
	}
	a := pushingAt(mem([2]string{"a1", "7"}, [2]string{"a2", "7"}, [2]string{"a3", "9"}))
	b := pushingAt(mem([2]string{"b1", "7"}, [2]string{"b2", "3"}))
	f, err := ns.NewFederation(ns.Member{Source: a}, ns.Member{Source: b})
	if err != nil {
		panic(err)
	}
	return []*pushing{a, b}, f, whole
}

func yielded(stores []*pushing) []int {
	out := make([]int, len(stores))
	for i, s := range stores {
		out[i] = s.yielded
	}
	return out
}

// A count over two stores is a row from each, added; and a page of two is each store's first two,
// merged. Both answer, cite and explain as the matches read one by one do. control: stores that
// neither page nor summarize ship every match.
func TestACountAndAPageAreARowsPerStore(t *testing.T) {
	cases := []struct {
		text string
		rows []int // per store
	}{
		{`attr(?id, "lines", "7") => count(?id)`, []int{1, 1}},
		{`attr(?id, "lines", ?n) => ?id, ?n order by ?id limit 2`, []int{2, 2}},
		{`attr(?id, "lines", ?n) => ?id, ?n order by ?n desc, ?id limit 2 offset 1`, []int{3, 2}},
	}
	for _, c := range cases {
		q := mustParse(t, c.text)
		stores, f, whole := linesStores()
		want, err := both(q, baseFor(std(whole)))
		if err != nil || len(want) == 0 {
			t.Fatalf("%s in memory: %v %v", c.text, binds(want), err)
		}
		got, err := SemiNaive{}.Eval(bg, q, MustBase(std(whole), f))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: pushed %v %v, in memory %v", c.text, got, err, want)
		}
		if y := yielded(stores); !reflect.DeepEqual(y, c.rows) {
			t.Errorf("%s: the stores sent %v rows, want %v", c.text, y, c.rows)
		}
		for _, opts := range [][]Option{{Witnesses()}, {Witnesses(), CanonicalCites()}} {
			_, f, _ := linesStores()
			w, _ := Naive{}.Eval(bg, q, baseFor(std(whole)), opts...)
			g, err := SemiNaive{}.Eval(bg, q, MustBase(std(whole), f), opts...)
			if err != nil || !reflect.DeepEqual(w, g) {
				t.Errorf("%s with %d options: pushed %v %v, in memory %v", c.text, len(opts), g, err, w)
			}
		}
		stores, f, _ = linesStores()
		for _, s := range stores {
			s.noGoal = true
		}
		if _, err := (SemiNaive{}).Eval(bg, q, MustBase(std(whole), f)); err != nil || !reflect.DeepEqual(yielded(stores), []int{0, 0}) || stores[0].summaries+stores[0].pages != 0 {
			t.Errorf("control: %s from stores that don't page or summarize: yielded %v, %d summaries and %d pages: %v", c.text, yielded(stores), stores[0].summaries, stores[0].pages, err)
		}
	}
}

// partValues is a part's values: 5 V, 5 A, 3 and 2 (no unit, 2 stored as text), the text "n/a", and
// 2 V, on parts p1 to
// p6, in a typed number column. (No NaN: reflect.DeepEqual can't compare one with itself.)
func partValues() *ns.MemSource {
	m := ns.NewMemSource().DeclareSchema("val", ns.Schema{Arity: 3, Labels: []string{"part", "kind", "v"}, Types: []ns.ArgType{{}, {}, {Type: ns.TypeNumber}}})
	add := func(part, kind string, v ns.Value) {
		m.Add("val", ns.Tuple{Vals: []ns.Value{ns.S(part), ns.S(kind), v}, Cites: []string{part}})
	}
	add("p1", "volt", ns.NU(5, "V"))
	add("p2", "volt", ns.NU(5, "A"))
	add("p3", "plain", ns.N(3))
	add("p4", "plain", ns.S("2")) // a number stored as text, read as the number
	add("p5", "plain", ns.S("n/a"))
	add("p6", "odd", ns.NU(2, "V"))
	return m
}

// A summarized aggregate keeps jaala's rules: mixed units are absent with a reason, a repeated value
// sums and lists as often as it occurs, distinct reads each value once, text is skipped, a comparison
// is checked on each row, and a having filters groups. A sloppy store splitting every row answers the
// same.
func TestASummaryKeepsTheAggregateRules(t *testing.T) {
	questions := []string{
		`val(?p, "volt", ?v) => sum(?v), min(?v), max(?v)`,
		`val(?p, ?k, ?v) => ?k, sum(?v), count(?p), count(distinct ?v), list(?v), list(distinct ?v)`,
		`val(?p, "plain", ?v), ?v > 2 => count(?p), sum(?v)`,
		`val(?p, "plain", ?v), ?v > 2 => count(?p)`,
		`val(?p, ?k, ?v) => ?k, count(?p) having count(?p) > 1`,
		`val(?p, "plain", ?v), ?v > 2 => min(?v)`,
		`val(?p, "plain", ?v) => sum(?v), min(?v)`,
	}
	for _, text := range questions {
		q := mustParse(t, text)
		want, err := both(q, baseFor(std(partValues())))
		if err != nil || len(want) == 0 {
			t.Fatalf("%s in memory: %v %v", text, want, err)
		}
		for _, sloppy := range []bool{false, true} {
			src := pushingAt(partValues())
			src.sloppy = sloppy
			got, err := SemiNaive{}.Eval(bg, q, MustBase(std(partValues()), src))
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("%s (sloppy %v): summarized %v %v, in memory %v", text, sloppy, got, err, want)
			}
			if src.summaries != 1 {
				t.Errorf("%s (sloppy %v): %d summaries, want 1", text, sloppy, src.summaries)
			}
		}
	}
	rows, _ := both(mustParse(t, questions[0]), baseFor(std(partValues())))
	if len(rows) != 1 || !rows[0].Bind["sum(v)"].Absent || !strings.Contains(rows[0].Why["sum(v)"], "mixed units") {
		t.Errorf("control: volts and amps sum to %v (%v), want absent over mixed units", rows, rows[0].Why)
	}
}

// A page's order is jaala's: a mixed column of numbers and text pages numbers by value first.
func TestAPageOrdersAsTheAnswerDoes(t *testing.T) {
	q := mustParse(t, `val(?p, ?k, ?v) => ?p, ?v order by ?v desc, ?p limit 3`)
	want, err := both(q, baseFor(std(partValues())))
	if err != nil || len(want) != 3 {
		t.Fatalf("in memory: %v %v", binds(want), err)
	}
	src := pushingAt(partValues())
	got, err := SemiNaive{}.Eval(bg, q, MustBase(std(partValues()), src))
	if err != nil || !reflect.DeepEqual(got, want) || src.pages != 1 || src.yielded > 3 {
		t.Errorf("paged %v %v from %d pages of %d rows, in memory %v", binds(got), err, src.pages, src.yielded, binds(want))
	}
}

// A goal with a comparison, a negation, a derived relation or an after cursor, or one whose total the
// host asks for, isn't paged: a store
// skipping the comparison would fill its page with rows failing it, and a page starts at the first
// row, not after the cursor.
func TestAGoalItCantPageIsSolved(t *testing.T) {
	for _, text := range []string{
		`val(?p, ?k, ?v), ?v > 2 => ?p order by ?p limit 1`,
		`val(?p, ?k, ?v), not val(?p, "volt", _) => ?p order by ?p limit 1`,
		`r(?p) :- val(?p, _, _); r(?p) :- val(?p, "volt", _); r(?p) => ?p order by ?p limit 1`,
	} {
		q := mustParse(t, text)
		want, err := both(q, baseFor(std(partValues())))
		if err != nil {
			t.Fatal(err)
		}
		src := pushingAt(partValues())
		got, err := SemiNaive{}.Eval(bg, q, MustBase(std(partValues()), src))
		if err != nil || !reflect.DeepEqual(binds(got), binds(want)) || src.pages != 0 {
			t.Errorf("%s: %v %v in %d pages, want %v unpaged", text, binds(got), err, src.pages, binds(want))
		}
	}
	q := mustParse(t, `val(?p, ?k, ?v) => ?p order by ?p limit 1`)
	after := After(map[Var]ns.Value{"p": ns.S("p2")})
	want, err := both(q, baseFor(std(partValues())), after)
	if err != nil || len(want) != 1 || want[0].Bind["p"].S != "p3" {
		t.Fatalf("control: after p2, in memory, %v %v", binds(want), err)
	}
	src := pushingAt(partValues())
	if got, err := (SemiNaive{}).Eval(bg, q, MustBase(std(partValues()), src), after); err != nil || !reflect.DeepEqual(binds(got), binds(want)) || src.pages != 0 {
		t.Errorf("after p2: %v %v in %d pages, want %v unpaged", binds(got), err, src.pages, binds(want))
	}
	var total int
	src = pushingAt(partValues())
	if _, err := (SemiNaive{}).Eval(bg, q, MustBase(std(partValues()), src), Total(&total)); err != nil || total != 6 || src.pages != 0 {
		t.Errorf("asking the total, it was %d in %d pages, want 6 unpaged: %v", total, src.pages, err)
	}
	src = pushingAt(partValues())
	if _, err := (SemiNaive{}).Eval(bg, q, MustBase(std(partValues()), src)); err != nil || src.pages != 1 {
		t.Errorf("control: without them the goal is paged, %d pages: %v", src.pages, err)
	}
}

// A failed summary fails the query, wrapped with the goal's literals, and isn't kept; its rows are
// charged to the budget as they arrive.
func TestASummaryFailsAndStopsAsAPush(t *testing.T) {
	q := mustParse(t, `val(?p, ?k, ?v) => ?p, count(?k)`)
	src := pushingAt(partValues())
	broken := errors.New("locked")
	src.pushFail = broken
	b := MustBase(std(partValues()), src)
	if _, err := (SemiNaive{}).Eval(bg, q, b); !errors.Is(err, broken) || !strings.Contains(err.Error(), "query: pushing val(?p, ?k, ?v): ") {
		t.Fatalf("got %v, want the store's error wrapped with the goal", err)
	}
	src.pushFail = nil
	if got, err := (SemiNaive{}).Eval(bg, q, b); err != nil || len(got) != 6 || src.summaries != 2 {
		t.Errorf("after the failure, %d rows %v in %d summaries, want six from a second summary", len(got), err, src.summaries)
	}
	many := ns.NewMemSource().Declare("val", "part", "kind", "v")
	for i := range 500 {
		many.Add("val", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprint("p", i)), ns.S("k"), ns.S("1")}})
	}
	big := pushingAt(many)
	_, err := SemiNaive{}.Eval(bg, q, MustBase(std(many), big), Budget(50))
	if over := (*BudgetExceeded)(nil); !errors.As(err, &over) || big.yielded > 51 {
		t.Errorf("under a budget of 50, the summary yielded %d rows: %v", big.yielded, err)
	}
}

// Explain counts a summary as a pushed call.
func TestExplainReportsASummary(t *testing.T) {
	_, f, whole := linesStores()
	var r Report
	if _, err := (SemiNaive{}).Eval(bg, mustParse(t, `attr(?id, "lines", "7") => count(?id)`), MustBase(std(whole), f), Explain(&r)); err != nil {
		t.Fatal(err)
	}
	if r.Pushes != 1 || r.Lookups != 0 || len(r.Sources) != 1 || r.Sources[0].Fetched != 2 {
		t.Errorf("reported %d pushes and %d lookups, sources %+v, want one push fetching a row from each store:\n%s", r.Pushes, r.Lookups, r.Sources, r.String())
	}
	if lit := r.Goal.Literals[0]; !slices.Contains(lit.Access, "pushed") || lit.Work == 0 {
		t.Errorf("the goal's literal was read %v with work %d, want the summary counted against it:\n%s", lit.Access, lit.Work, r.String())
	}
}

// addTimes sums x n times as n adds would, the product's rounding error included: three thirds less
// one is the thirds' own error, which the rounded product 1.0 alone would lose.
func TestAddTimesSumsAsRepeatedAdds(t *testing.T) {
	third := 1.0 / 3
	var times, adds exactSum
	times.addTimes(third, 3)
	times.add(-1)
	for range 3 {
		adds.add(third)
	}
	adds.add(-1)
	if times.value() != adds.value() || adds.value() == 0 {
		t.Errorf("addTimes gave %v, three adds %v, want the same nonzero error", times.value(), adds.value())
	}
}

// A min and max a store reduces is a row per group and unit rather than one per value (#252), and
// answers as in memory, mixed units included. control: a store that can't is asked for the values.
func TestExtremesAreARowPerGroup(t *testing.T) {
	q := mustParse(t, `val(?p, ?k, ?v) => ?k, min(?v), max(?v)`)
	want, err := both(q, baseFor(std(partValues())))
	if err != nil || len(want) != 3 {
		t.Fatalf("in memory: %v %v", want, err)
	}
	for _, noExtremes := range []bool{false, true} {
		src := pushingAt(partValues())
		src.noExtremes = noExtremes
		got, err := SemiNaive{}.Eval(bg, q, MustBase(std(partValues()), src))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("noExtremes %v: %v %v, in memory %v", noExtremes, got, err, want)
		}
		if len(src.summarized) == 0 {
			t.Errorf("noExtremes %v: the store was never asked for a summary", noExtremes)
			continue
		}
		s := src.summarized[len(src.summarized)-1]
		switch {
		case !noExtremes && (len(s.Extremes) != 1 || len(s.Values) != 0 || src.yielded != 3):
			t.Errorf("asked %+v and got %d rows, want ?v's extremes and a row per kind", s, src.yielded)
		case noExtremes && (len(s.Extremes) != 0 || len(s.Values) != 1 || src.yielded <= 3):
			t.Errorf("control: refusing extremes, asked %+v and got %d rows, want ?v's values, a row per value", s, src.yielded)
		}
	}
}

// A NaN among a min's numbers makes it NaN, reduced in the store as in memory.
func TestExtremesKeepNaN(t *testing.T) {
	m := ns.NewMemSource().Declare("w", "n", "v")
	m.Add("w", ns.Tuple{Vals: []ns.Value{ns.S("a"), ns.N(2)}})
	m.Add("w", ns.Tuple{Vals: []ns.Value{ns.S("b"), ns.N(math.NaN())}})
	q := mustParse(t, `w(?n, ?v) => min(?v), max(?v)`)
	want, _ := Naive{}.Eval(bg, q, baseFor(std(m)))
	src := pushingAt(m)
	got, err := SemiNaive{}.Eval(bg, q, MustBase(std(m), src))
	if err != nil || len(got) != 1 || len(src.summarized) != 1 || len(src.summarized[0].Extremes) != 1 {
		t.Fatalf("%v %v, summaries %+v", got, err, src.summarized)
	}
	again, err := SemiNaive{}.Eval(bg, q, MustBase(std(m), src)) // a new Base: the store reduces again
	if err != nil || len(src.summarized) != 2 {
		t.Fatalf("asked again: %v, %d summaries", err, len(src.summarized))
	}
	b := MustBase(std(m), src)
	if _, err := (SemiNaive{}).Eval(bg, q, b); err != nil {
		t.Fatal(err)
	}
	kept, err := SemiNaive{}.Eval(bg, q, b) // the same Base: read back from what it kept
	if err != nil || len(src.summarized) != 3 {
		t.Fatalf("asked on the same Base: %v, %d summaries, want the third kept", err, len(src.summarized))
	}
	for _, rows := range [][]Row{got, again, kept} {
		for _, col := range []Var{"min(v)", "max(v)"} {
			if w, g := want[0].Bind[col], rows[0].Bind[col]; !math.IsNaN(*w.Num) || g.Num == nil || !math.IsNaN(*g.Num) {
				t.Errorf("%s: in memory %v, reduced %v, want NaN both", col, w, g)
			}
		}
	}
}
