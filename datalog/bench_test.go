package datalog

import (
	"bufio"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// Work baselines (#88): standard Datalog workloads and an agni-sized netlist, each answered by the
// planned SemiNaive, with the work it took checked against testdata/work.golden. Work() is
// deterministic, so a rewrite that makes a workload cost more fails here even when its answers are
// right, which is what the generated corpus can't see. Timings are in the matching benchmarks and
// are never checked.
//
// go test ./datalog -run TestWorkStaysWithinBaseline -update rewrites the baseline. A PR that moves
// it says why.

var updateWork = flag.Bool("update", false, "rewrite testdata/work.golden from this run")

const (
	workGolden = "testdata/work.golden"
	// workSlack is how far above its baseline a workload's work may go before the test fails, and
	// below before it asks for the baseline to be lowered.
	workSlack = 0.10
)

type workload struct {
	name  string
	src   func() ns.Source
	query string
	opts  []Option
}

const (
	closure  = `reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c); `
	sameGen  = `sg(?x, ?y) :- edge(?p, ?x), edge(?p, ?y); sg(?x, ?y) :- edge(?p, ?x), sg(?p, ?q), edge(?q, ?y); `
	pointsTo = `pt(?v, ?o) :- new(?v, ?o); pt(?v, ?o) :- assign(?v, ?w), pt(?w, ?o); ` +
		`pt(?v, ?o) :- load(?v, ?p), pt(?p, ?q), hpt(?q, ?o); hpt(?q, ?o) :- store(?p, ?w), pt(?p, ?q), pt(?w, ?o); `
	series = `series(?a, ?b) :- part(?r, "resistor"), pin(?r, ?a), pin(?r, ?b), ?a != ?b; ` +
		`along(?a, ?b) :- series(?a, ?b); along(?a, ?c) :- along(?a, ?b), series(?b, ?c); `
	testPoints = `tps(?n, count(distinct ?t)) :- pin(?t, ?n), part(?t, "test_point"); ` +
		`tpc(?n, ?c) :- tps(?n, ?c); tpc(?n, 0) :- pin(_, ?n), not tps(?n, _); `
)

var workloads = []workload{
	{"closure/chain", func() ns.Source { return chainOf(150) }, closure + `reach(?a, ?b) => ?a, ?b`, nil},
	{"closure/reversed", func() ns.Source { return reversedLine(150) }, closure + `reach(?a, ?b) => ?a, ?b`, nil},
	{"closure/reversed-from-start", func() ns.Source { return reversedLine(400) }, closure + `reach("v0", ?b) => ?b`, nil},
	{"closure/reversed-to-end", func() ns.Source { return reversedLine(400) }, closure + `reach(?a, "v399") => ?a`, nil},
	{"same-generation/tree", func() ns.Source { return binaryTree(7) }, sameGen + `sg(?x, ?y) => ?x, ?y`, nil},
	{"same-generation/tree-bound", func() ns.Source { return binaryTree(9) }, sameGen + `sg("t300", ?y) => ?y`, nil},
	{"points-to", func() ns.Source { return pointerProgram(1) }, pointsTo + `pt(?v, ?o) => ?v, ?o`, nil},
	{"points-to/one-variable", func() ns.Source { return pointerProgram(1) }, pointsTo + `pt("p7", ?o) => ?o`, nil},
	{"netlist/test-point-counts", func() ns.Source { return netlistOf(1, 2000, 600) }, testPoints + `tpc(?n, ?c) => ?n, ?c`, nil},
	{"netlist/uncovered-nets", func() ns.Source { return netlistOf(1, 2000, 600) }, testPoints + `tpc(?n, 0) => count(?n)`, nil},
	{"netlist/series-from-a-net", func() ns.Source { return netlistOf(1, 2000, 600) }, series + `along("n0", ?b) => ?b`, nil},
	{"netlist/series-reach-count", func() ns.Source { return netlistOf(1, 2000, 600) }, series + `along(?a, ?b) => ?a, count(?b)`, nil},
	{"netlist/probed-both", func() ns.Source { return probedBoard(400) }, probed + `probed_both(?r) => count(?r)`, nil},
	// CanonicalCites (#22) compares every rederivation and derives again from what it shortens.
	{"canonical/closure-chain", func() ns.Source { return chainOf(150) }, closure + `reach(?a, ?b) => ?a, ?b`, []Option{CanonicalCites()}},
	{"canonical/closure-reversed-from-start", func() ns.Source { return reversedLine(400) }, closure + `reach("v0", ?b) => ?b`, []Option{CanonicalCites()}},
	{"canonical/same-generation-tree", func() ns.Source { return binaryTree(7) }, sameGen + `sg(?x, ?y) => ?x, ?y`, []Option{CanonicalCites()}},
	{"canonical/points-to-one-variable", func() ns.Source { return pointerProgram(1) }, pointsTo + `pt("p7", ?o) => ?o`, []Option{CanonicalCites()}},
	// Across (#180) derives points-to once over 50 versions; BenchmarkVersions sets it against 50 Ats.
	{"versions/points-to-across-50", func() ns.Source { return historyOf(pointerProgram(1), 50, 0.04) }, pointsTo + `pt(?v, ?o) => ?v, ?o`, []Option{Across(ns.Span(0, 50))}},
}

// binaryTree is a complete binary tree of the given depth as parent->child edges, t0 the root and
// tN's children t(2N+1) and t(2N+2).
func binaryTree(depth int) *ns.MemSource {
	src := ns.NewMemSource().Declare("edge", "from", "to").Declare("node", "name")
	n := 1<<depth - 1
	for i := 0; i < n; i++ {
		src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("t%d", i))}})
		for _, c := range []int{2*i + 1, 2*i + 2} {
			if c < n {
				src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("t%d", i)), ns.S(fmt.Sprintf("t%d", c))}})
			}
		}
	}
	return src
}

// pointerProgram is the facts of a small program for an Andersen-style points-to analysis: 300
// pointer variables, 100 allocation sites, and random assignments, loads and stores among them.
func pointerProgram(seed int64) *ns.MemSource {
	rnd := rand.New(rand.NewSource(seed))
	src := ns.NewMemSource().Declare("new", "v", "o").Declare("assign", "to", "from").
		Declare("load", "to", "from").Declare("store", "to", "from")
	p := func() ns.Value { return ns.S(fmt.Sprintf("p%d", rnd.Intn(300))) }
	for i := 0; i < 100; i++ {
		src.Add("new", ns.Tuple{Vals: []ns.Value{p(), ns.S(fmt.Sprintf("o%d", i))}})
	}
	for i := 0; i < 300; i++ {
		src.Add("assign", ns.Tuple{Vals: []ns.Value{p(), p()}})
	}
	for _, rel := range []string{"load", "store"} {
		for i := 0; i < 60; i++ {
			src.Add(rel, ns.Tuple{Vals: []ns.Value{p(), p()}})
		}
	}
	return src
}

// netlistOf is a circuit about the size of a board agni audits: parts of each class with their pins
// on random nets (a resistor's two pins usually on different nets, so series paths form), and test
// points on some nets but not all.
func netlistOf(seed int64, parts, nets int) *ns.MemSource {
	rnd := rand.New(rand.NewSource(seed))
	src := ns.NewMemSource().Declare("part", "ref", "class").Declare("pin", "ref", "net")
	net := func() ns.Value { return ns.S(fmt.Sprintf("n%d", rnd.Intn(nets))) }
	classes := []struct {
		name string
		pins int
		pct  int
	}{{"resistor", 2, 40}, {"capacitor", 2, 30}, {"ic", 8, 10}, {"test_point", 1, 20}}
	for i := 0; i < parts; i++ {
		c, roll := classes[0], rnd.Intn(100)
		for _, k := range classes {
			if roll < k.pct {
				c = k
				break
			}
			roll -= k.pct
		}
		ref := ns.S(fmt.Sprintf("%s%d", c.name[:1], i))
		src.Add("part", ns.Tuple{Vals: []ns.Value{ref, ns.S(c.name)}})
		for j := 0; j < c.pins; j++ {
			src.Add("pin", ns.Tuple{Vals: []ns.Value{ref, net()}})
		}
	}
	return src
}

// measure answers a workload under ev and returns the work it took and its answer's size.
func (w workload) measure(t testing.TB, ev Evaluator) (int64, int) {
	t.Helper()
	b := baseFor(std(w.src()))
	q, err := Parse(w.query)
	if err != nil {
		t.Fatalf("%s: %v", w.name, err)
	}
	rows, err := ev.Eval(bg, q, b, w.opts...)
	if err != nil {
		t.Fatalf("%s: %v", w.name, err)
	}
	return b.Work(), len(rows)
}

type workBaseline struct {
	work int64
	rows int
}

func readWorkGolden(t *testing.T) map[string]workBaseline {
	t.Helper()
	f, err := os.Open(workGolden)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	defer f.Close()
	out := map[string]workBaseline{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("%s: bad line %q", workGolden, line)
		}
		work, err1 := strconv.ParseInt(fields[1], 10, 64)
		rows, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil {
			t.Fatalf("%s: bad line %q", workGolden, line)
		}
		out[fields[0]] = workBaseline{work, rows}
	}
	return out
}

func writeWorkGolden(t *testing.T, got map[string]workBaseline) {
	t.Helper()
	var b strings.Builder
	b.WriteString("# The work, and the answer's row count, of each workload in bench_test.go under SemiNaive{}.\n")
	b.WriteString("# Rewritten by: go test ./datalog -run TestWorkStaysWithinBaseline -update\n")
	names := make([]string, 0, len(got))
	for n := range got {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&b, "%s %d %d\n", n, got[n].work, got[n].rows)
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workGolden, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Each workload's work under the planned SemiNaive stays within workSlack of its baseline, and its
// answer keeps its size. Work that drops by more than the slack fails too, so the baseline follows
// an improvement and the next regression is measured from there.
func TestWorkStaysWithinBaseline(t *testing.T) {
	got := map[string]workBaseline{}
	for _, w := range workloads {
		work, rows := w.measure(t, SemiNaive{})
		got[w.name] = workBaseline{work, rows}
	}
	if *updateWork {
		writeWorkGolden(t, got)
		return
	}
	want := readWorkGolden(t)
	for _, w := range workloads {
		g, ok := want[w.name]
		if !ok {
			t.Errorf("%s has no baseline; run with -update", w.name)
			continue
		}
		cur := got[w.name]
		ratio := float64(cur.work) / float64(g.work)
		t.Logf("%-32s work %9d  baseline %9d  (%+.1f%%)  rows %d", w.name, cur.work, g.work, (ratio-1)*100, cur.rows)
		switch {
		case cur.rows != g.rows:
			t.Errorf("%s answers %d rows, baseline %d", w.name, cur.rows, g.rows)
		case ratio > 1+workSlack:
			t.Errorf("%s did %d work, %.0f%% over its baseline %d", w.name, cur.work, (ratio-1)*100, g.work)
		case ratio < 1-workSlack:
			t.Errorf("%s did %d work, %.0f%% under its baseline %d: lower it with -update", w.name, cur.work, (1-ratio)*100, g.work)
		}
		delete(want, w.name)
	}
	for name := range want {
		t.Errorf("%s has a baseline but no workload; run with -update", name)
	}
}

// The workloads have to be big enough to show a cost that grows with them. control: doubling the
// chain under a closure at least triples the planned evaluator's work, so the fixtures are past the
// size where fixed costs hide the fixpoint's.
func TestWorkloadsAreBigEnoughToShowGrowth(t *testing.T) {
	q := closure + `reach(?a, ?b) => ?a, ?b`
	small, _ := workload{"", func() ns.Source { return chainOf(75) }, q, nil}.measure(t, SemiNaive{})
	big, _ := workload{"", func() ns.Source { return chainOf(150) }, q, nil}.measure(t, SemiNaive{})
	if ratio := float64(big) / float64(small); ratio < 3 {
		t.Errorf("doubling the chain grew the closure's work %.1fx, want at least 3x", ratio)
	}
}

func BenchmarkWorkloads(b *testing.B) {
	for _, w := range workloads {
		q, err := Parse(w.query)
		if err != nil {
			b.Fatal(err)
		}
		v := std(w.src())
		b.Run(w.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if _, err := (SemiNaive{}).Eval(bg, q, baseFor(v), w.opts...); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
