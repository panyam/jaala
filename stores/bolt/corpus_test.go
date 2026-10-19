package bolt

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/internal/corpus"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

// corpusGraph lays out the corpus's relations: node(name) under Node, weight(node, w) under Weight
// with w a node property, and edge(from, to) as EDGE relationships.
var corpusGraph = cypher.Graph{
	Nodes: []cypher.Node{
		{Label: "Node", Relation: "node", Key: 0},
		{Label: "Weight", Relation: "weight", Key: 0, Props: map[string]int{"w": 1}},
	},
	Edges: []cypher.Edge{{Type: "EDGE", Relation: "edge", From: 0, To: 1}},
}

// vocabulary is the corpus's: stdlib, and a Source's units folded as the Base reads them, as
// datalog's corpus reads the same seeds (#303).
func vocabulary(t testing.TB, src ns.Source) *ns.Vocabulary {
	t.Helper()
	v, err := ns.NewVocabulary(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.AddLanguage(datalog.Language); err != nil {
		t.Fatal(err)
	}
	if err := stdlib.Register(v); err != nil {
		t.Fatal(err)
	}
	v.FoldSourceUnits()
	return v
}

func base(t testing.TB, src ns.Source) *datalog.Base {
	t.Helper()
	b, err := datalog.NewBase(vocabulary(t, src), src)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// rows writes an answer as text, each row's columns by name with their values' text and unit, in
// the order the evaluator answered.
func rows(rs []datalog.Row) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		var cols []string
		for v, val := range r.Bind {
			cols = append(cols, fmt.Sprintf("%s=%q%s%v", v, val.S, val.BaseUnit, val.Absent))
		}
		slices.Sort(cols)
		out[i] = strings.Join(cols, " ")
	}
	return out
}

func bindOpts(c corpus.Case) []datalog.Option {
	var opts []datalog.Option
	if bind := c.Prog.Bind(); len(bind) > 0 {
		values := map[datalog.Var][]ns.Value{}
		for v, names := range bind {
			values[datalog.Var(v)] = []ns.Value{}
			for _, n := range names {
				values[datalog.Var(v)] = append(values[datalog.Var(v)], ns.S(n))
			}
		}
		opts = append(opts, datalog.Bind(values))
	}
	if c.Witnessed {
		opts = append(opts, datalog.Witnesses())
	}
	return opts
}

// corpusBudget bounds Naive's work over the seed's MemSource, as datalog's corpus does.
const corpusBudget = 200_000

type outcome int

const (
	agreed outcome = iota
	skipped
	disagreed
)

// agreeOverBolt loads c's graph into the server and asks c's program of it, through Naive and the
// planned SemiNaive, and of c's MemSource through Naive: every answer must be the MemSource's, row
// for row and in order, and an error the same error. A limit without an order by answers whichever
// rows the goal finds first, so such a program is skipped here; datalog's corpus checks it over its
// own stores.
func agreeOverBolt(t testing.TB, s *Store, c corpus.Case, st *counting) (outcome, string) {
	text := c.Prog.String()
	q, err := datalog.Parse(text)
	if err != nil {
		return skipped, ""
	}
	if q.Limit > 0 && len(q.OrderBy) == 0 {
		return skipped, ""
	}
	mem := c.Facts.Source()
	want, werr := (datalog.Naive{}).Eval(bg, q, base(t, mem), append(bindOpts(c), datalog.Budget(corpusBudget))...)
	var over *datalog.BudgetExceeded
	if errors.As(werr, &over) {
		return skipped, ""
	}
	empty(t, s.driver)
	if err := s.Apply(bg, load(mem)); err != nil {
		return disagreed, "loading the graph: " + err.Error()
	}
	for _, ev := range []struct {
		name string
		e    datalog.Evaluator
	}{{"naive", datalog.Naive{}}, {"planned", datalog.SemiNaive{}}} {
		got, err := ev.e.Eval(bg, q, base(t, st), bindOpts(c)...)
		if fmt.Sprint(err) != fmt.Sprint(werr) {
			return disagreed, fmt.Sprintf("%s over bolt errs %v, over memory %v", ev.name, err, werr)
		}
		if !slices.Equal(rows(got), rows(want)) {
			return disagreed, fmt.Sprintf("%s over bolt answers\n  %q\n over memory\n  %q", ev.name, rows(got), rows(want))
		}
	}
	// Under CanonicalCites every evaluator keeps each tuple's shortest derivation (#22), so the
	// citations, and under Witnesses each leaf's fact id, must match too: a fact's id and citations
	// are kept by the store, and only these show them.
	wantC, werr := (datalog.Naive{}).Eval(bg, q, base(t, mem), append(bindOpts(c), datalog.CanonicalCites())...)
	for _, ev := range []struct {
		name string
		e    datalog.Evaluator
	}{{"naive", datalog.Naive{}}, {"planned", datalog.SemiNaive{}}} {
		got, err := ev.e.Eval(bg, q, base(t, st), append(bindOpts(c), datalog.CanonicalCites())...)
		if fmt.Sprint(err) != fmt.Sprint(werr) {
			return disagreed, fmt.Sprintf("%s over bolt, canonically, errs %v, over memory %v", ev.name, err, werr)
		}
		if !slices.Equal(cited(got), cited(wantC)) {
			return disagreed, fmt.Sprintf("%s over bolt, canonically, answers\n  %q\n over memory\n  %q", ev.name, cited(got), cited(wantC))
		}
	}
	return agreed, ""
}

// cited writes an answer with each row's citations and witnesses.
func cited(rs []datalog.Row) []string {
	out := rows(rs)
	for i, r := range rs {
		var w strings.Builder
		for _, n := range r.Witness {
			witness(&w, n)
		}
		out[i] += fmt.Sprintf(" cites=%v witness=%s", r.Cites, w.String())
	}
	return out
}

func witness(b *strings.Builder, w *datalog.Witness) {
	fmt.Fprintf(b, "(%s", w.Relation)
	for _, v := range w.Values {
		fmt.Fprintf(b, " %q", v.S)
	}
	fmt.Fprintf(b, " fact=%q cites=%v neg=%v", w.Fact, w.Cites, w.Negated)
	for _, c := range w.Children {
		witness(b, c)
	}
	b.WriteString(")")
}

// Every corpus seed's program answers over a graph server as over its own MemSource, plain and
// under Witnesses, through Naive and the planned SemiNaive. JAALA_BOLT_SEEDS sets how many seeds
// run (CI's bolt job runs 300), and JAALA_GEN_SEED=n replays one. A disagreement is shrunk before
// it is reported. The control is that the planned run asks the server by lookup and by batch, so
// the answers aren't all from whole reads.
func TestTheCorpusAgreesOverBolt(t *testing.T) {
	first, seeds := int64(1), envInt("JAALA_BOLT_SEEDS", 40)
	if s := envInt("JAALA_GEN_SEED", 0); s > 0 {
		first, seeds = int64(s), 1
	}
	for _, sv := range servers(t) {
		t.Run(sv.name, func(t *testing.T) {
			compared, skips, failed := 0, 0, 0
			lookups, batches, multi := 0, 0, 0
			for seed := first; seed < first+int64(seeds); seed++ {
				rnd := rand.New(rand.NewSource(seed))
				facts := corpus.Graph(rnd)
				prog := corpus.New(rnd, facts).Program()
				for _, witnessed := range []bool{false, true} {
					c := corpus.Case{Prog: prog, Facts: facts, Witnessed: witnessed}
					s := open(t, sv.driver, corpusGraph, schemasOf(facts.Source()))
					st := &counting{Store: s}
					out, diff := agreeOverBolt(t, s, c, st)
					lookups, batches, multi = lookups+st.lookups, batches+st.batches, max(multi, st.batchedValues-st.batches)
					switch out {
					case skipped:
						skips++
					case agreed:
						compared++
					case disagreed:
						failed++
						small, d := corpus.Shrink(c, diff, func(s2 corpus.Case) (bool, string) {
							o, d := agreeOverBolt(t, s, s2, &counting{Store: s})
							return o == disagreed, d
						})
						t.Errorf("seed %d (JAALA_GEN_SEED=%d replays it): %s\n%s", seed, seed, d, small)
						if failed >= 3 {
							t.Fatal("stopping after 3")
						}
					}
				}
			}
			t.Logf("%d seeds: %d cases compared, %d skipped (a limit without an order by, or over Naive's budget); %d lookups, %d batches",
				seeds, compared, skips, lookups, batches)
			if seeds >= 20 {
				if lookups+batches == 0 {
					t.Error("control: no program asked the server by lookup or batch")
				}
				if multi == 0 {
					t.Error("control: no batch carried more than one value")
				}
				if compared < seeds {
					t.Errorf("control: only %d of %d cases compared", compared, 2*seeds)
				}
			}
		})
	}
}

func envInt(name string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return def
}

// edit is a session's change set over a seed's graph: a few edges removed, the first node's weight
// replaced, and a new node joined to it.
func edit(src ns.Source) ns.Changes {
	var c ns.Changes
	for i, e := range src.Tuples("edge") {
		if i%3 == 0 {
			c.Remove("edge", e)
		}
	}
	if ws := src.Tuples("weight"); len(ws) > 0 {
		c.Remove("weight", ws[0]).Add("weight", ns.Tuple{Vals: []ns.Value{ws[0].Vals[0], ns.N(99)}, Cites: []string{"session:w"}})
		c.Add("node", ns.Tuple{Vals: []ns.Value{ns.S("added")}, Cites: []string{"session:n"}})
		c.Add("edge", ns.Tuple{Vals: []ns.Value{ws[0].Vals[0], ns.S("added")}, ID: "e-added", Cites: []string{"session:e"}})
	}
	return c
}

// A session over the server answers as the same session over memory, and once its change set has
// gone over the wire as JSON (#417) and been applied, the server alone answers as the session did.
// The control is that the edit changes some answer, so the comparison can tell an applied set from
// one that was dropped.
func TestACommittedSessionAnswersAsItsOverlayDid(t *testing.T) {
	seeds := envInt("JAALA_BOLT_SESSION_SEEDS", 30)
	for _, sv := range servers(t) {
		t.Run(sv.name, func(t *testing.T) {
			changed := 0
			for seed := int64(1); seed <= int64(seeds); seed++ {
				rnd := rand.New(rand.NewSource(seed))
				facts := corpus.Graph(rnd)
				prog := corpus.New(rnd, facts).Program()
				q, err := datalog.Parse(prog.String())
				if err != nil || q.Limit > 0 && len(q.OrderBy) == 0 {
					continue
				}
				c := corpus.Case{Prog: prog, Facts: facts}
				mem := facts.Source()
				ch := edit(mem)
				ask := func(src ns.Source) ([]string, error) {
					rs, err := (datalog.SemiNaive{}).Eval(bg, q, base(t, src), append(bindOpts(c), datalog.CanonicalCites(), datalog.Budget(corpusBudget))...)
					return cited(rs), err
				}
				before, err := ask(mem)
				if err != nil {
					continue
				}
				want, err := ask(ns.Overlay(mem, ch))
				if err != nil {
					continue
				}
				if !slices.Equal(before, want) {
					changed++
				}
				s := open(t, sv.driver, corpusGraph, schemasOf(mem))
				if err := s.Apply(bg, load(mem)); err != nil {
					t.Fatal(err)
				}
				if got, err := ask(ns.Overlay(s, ch)); err != nil || !slices.Equal(got, want) {
					t.Fatalf("seed %d: the session over bolt answers %q (%v), over memory %q\n%s", seed, got, err, want, prog)
				}
				wire, err := json.Marshal(ch)
				if err != nil {
					t.Fatal(err)
				}
				var sent ns.Changes
				if err := json.Unmarshal(wire, &sent); err != nil {
					t.Fatal(err)
				}
				if err := s.Apply(bg, sent); err != nil {
					t.Fatalf("seed %d: applying the session: %v", seed, err)
				}
				if got, err := ask(s); err != nil || !slices.Equal(got, want) {
					t.Fatalf("seed %d: after the commit bolt answers %q (%v), the session %q\n%s", seed, got, err, want, prog)
				}
			}
			if changed == 0 {
				t.Errorf("control: no seed's edit changed its answer over %d seeds", seeds)
			}
			t.Logf("%d seeds, %d answers the edit changed", seeds, changed)
		})
	}
}
