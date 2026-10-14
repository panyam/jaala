package datalog

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// pushing is a looking store that also evaluates conjunctions itself (ns.PushSource, #237), by a
// nested-loop join of its own. sloppy ignores the constants and comparisons, returning rows that
// don't satisfy the conjunction, as a store may; noCompare refuses a conjunction with comparisons, and
// refuse any it reports true for. It records each conjunction pushed and counts the rows it yielded.
type pushing struct {
	*looking
	sloppy, noCompare bool
	refuse            func(ns.Conjunction) bool
	pushFail          error
	pushed            []ns.Conjunction
	yielded           int
	noWalk            bool
	walkFail          error
	walked            []ns.Walk
	noGoal            bool // refuses a page or a summary
	noExtremes        bool // refuses a summary with Extremes
	pages, summaries  int
	summarized        []ns.Summary
	pagedAfter        [][]ns.Value
}

func pushingAt(src ns.Source) *pushing { return &pushing{looking: lookingAt(src)} }

func (p *pushing) Pushes(c ns.Conjunction) bool {
	if p.noCompare && len(c.Compare) > 0 || c.Page != nil && p.noGoal {
		return false
	}
	return p.refuse == nil || !p.refuse(c)
}

func (p *pushing) Push(_ context.Context, c ns.Conjunction) iter.Seq2[[]ns.Tuple, error] {
	return func(yield func([]ns.Tuple, error) bool) {
		p.mu.Lock()
		p.pushed = append(p.pushed, c)
		fail := p.pushFail
		p.mu.Unlock()
		if fail != nil {
			yield(nil, fail)
			return
		}
		schema := func(rel string) ns.Schema { s, _ := p.Source.Schema(rel); return s }
		all := func(rel string, _ map[int]ns.Value) []ns.Tuple { return p.Source.Tuples(rel) }
		rows := evalConjunction(c, schema, all, p.sloppy)
		if c.Page != nil {
			p.mu.Lock()
			p.pages++
			p.pagedAfter = append(p.pagedAfter, c.Page.After)
			p.mu.Unlock()
			if !p.sloppy {
				rows = pageOf(c, schema, rows)
			}
		}
		for _, row := range rows {
			p.mu.Lock()
			p.yielded++
			p.mu.Unlock()
			if !yield(row, nil) {
				return
			}
		}
	}
}

// Walks implements ns.WalkSource: a pushing store walks any relation, unless noWalk.
func (p *pushing) Walks(string, int, int, []int) bool { return !p.noWalk }

// Walk follows w over the store's facts, read as their types; a sloppy store also sends a fact no
// walk reaches.
func (p *pushing) Walk(_ context.Context, w ns.Walk) iter.Seq2[ns.Tuple, error] {
	return func(yield func(ns.Tuple, error) bool) {
		p.mu.Lock()
		p.walked = append(p.walked, w)
		fail := p.walkFail
		p.mu.Unlock()
		if fail != nil {
			yield(ns.Tuple{}, fail)
			return
		}
		sch, _ := p.Source.Schema(w.Relation)
		all := p.Source.Tuples(w.Relation)
		read := normalizeTuples(all, sch.Types)
		facts := walkFacts(w, func(bound map[int]ns.Value) []ns.Tuple {
			var out []ns.Tuple
			for k, t := range read {
				ok := true
				for i, v := range bound {
					ok = ok && i < len(t.Vals) && valueEq(t.Vals[i], normalizeTuples([]ns.Tuple{{Vals: []ns.Value{v}}}, sch.Types[min(i, len(sch.Types)):min(i+1, len(sch.Types))])[0].Vals[0])
				}
				if ok {
					out = append(out, all[k])
				}
			}
			return out
		})
		if p.sloppy && len(all) > 0 {
			facts = append(facts, all[len(all)-1])
		}
		for _, t := range facts {
			p.mu.Lock()
			p.yielded++
			p.mu.Unlock()
			if !yield(t, nil) {
				return
			}
		}
	}
}

// walkFacts is a store's own answer to w: from each start value, and each To value of a fact it
// returns, the facts lookup offers with that value at w.From and w.Bound's constants, each value
// walked once by its text.
func walkFacts(w ns.Walk, lookup func(bound map[int]ns.Value) []ns.Tuple) []ns.Tuple {
	var out []ns.Tuple
	seen := map[string]bool{}
	queue := append([]ns.Value(nil), w.Start...)
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		if k := valueKey(v); seen[k] {
			continue
		} else {
			seen[k] = true
		}
		bound := map[int]ns.Value{w.From: v}
		for p, c := range w.Bound {
			bound[p] = c
		}
		for _, t := range lookup(bound) {
			out = append(out, t)
			if w.To < len(t.Vals) {
				queue = append(queue, t.Vals[w.To])
			}
		}
	}
	return out
}

func (p *pushing) walks() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.walked)
}

func (p *pushing) pushes() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pushed)
}

// Summarizes implements ns.SummarySource, unless noGoal.
func (p *pushing) Summarizes(_ ns.Conjunction, s ns.Summary) bool {
	return !p.noGoal && (!p.noExtremes || len(s.Extremes) == 0)
}

// Summarize groups the store's own matches of c as s asks, skipping c's comparisons as a store may; a
// sloppy store sends each match as a row of its own, which a store unsure two values are equal may.
func (p *pushing) Summarize(_ context.Context, c ns.Conjunction, s ns.Summary) iter.Seq2[ns.SummaryRow, error] {
	return func(yield func(ns.SummaryRow, error) bool) {
		p.mu.Lock()
		p.summaries++
		p.summarized = append(p.summarized, s)
		fail := p.pushFail
		p.mu.Unlock()
		if fail != nil {
			yield(ns.SummaryRow{}, fail)
			return
		}
		schema := func(rel string) ns.Schema { sc, _ := p.Source.Schema(rel); return sc }
		all := func(rel string, _ map[int]ns.Value) []ns.Tuple { return p.Source.Tuples(rel) }
		uncompared := c
		uncompared.Compare = nil // left to the engine, which checks them on each row
		for _, row := range summaryOf(c, s, schema, evalConjunction(uncompared, schema, all, false), p.sloppy) {
			p.mu.Lock()
			p.yielded++
			p.mu.Unlock()
			if !yield(row, nil) {
				return
			}
		}
	}
}

// rowValues is a match's value for each variable of c, read as its argument's type.
func rowValues(c ns.Conjunction, schema func(string) ns.Schema, row []ns.Tuple) map[int]ns.Value {
	vals := map[int]ns.Value{}
	for k, a := range c.Atoms {
		read := normalizeTuples([]ns.Tuple{row[k]}, schema(a.Relation).Types)[0]
		for pos, x := range a.Args {
			if _, ok := vals[x.Var]; x.Var > 0 && !ok {
				vals[x.Var] = read.Vals[pos]
			}
		}
	}
	return vals
}

// pageOf is the matches behind the first c.Page.Limit distinct rows of c.Page.Columns, ordered by its
// keys and then the columns, in the engine's order.
func pageOf(c ns.Conjunction, schema func(string) ns.Schema, rows [][]ns.Tuple) [][]ns.Tuple {
	pg := c.Page
	type answer struct {
		vals map[int]ns.Value
		key  string
	}
	keyOf := func(vals map[int]ns.Value) string {
		var sb strings.Builder
		for _, v := range pg.Columns {
			sb.WriteString(valueKey(vals[v]) + "\x00")
		}
		return sb.String()
	}
	seen := map[string]bool{}
	var answers []answer
	for _, row := range rows {
		vals := rowValues(c, schema, row)
		if !pageAfter(pg, vals) {
			continue
		}
		if k := keyOf(vals); !seen[k] {
			seen[k] = true
			answers = append(answers, answer{vals, k})
		}
	}
	slices.SortFunc(answers, func(x, y answer) int {
		for _, k := range pg.Order {
			if o := orderValues(x.vals[k.Var], y.vals[k.Var]); o != 0 {
				if k.Desc {
					return -o
				}
				return o
			}
		}
		for _, v := range pg.Columns {
			if o := orderValues(x.vals[v], y.vals[v]); o != 0 {
				return o
			}
		}
		return 0
	})
	keep := map[string]bool{}
	for i, a := range answers {
		// Rows tied with the last one kept go too, since the engine's order may break the tie either way.
		if i >= pg.Limit && (i == 0 || orderTie(pg, answers[i-1].vals, a.vals) != 0) {
			break
		}
		keep[a.key] = true
	}
	var out [][]ns.Tuple
	for _, row := range rows {
		if keep[keyOf(rowValues(c, schema, row))] {
			out = append(out, row)
		}
	}
	return out
}

// pageAfter reports whether an answer's values come strictly after a page's cursor, if it has one.
func pageAfter(pg *ns.Page, vals map[int]ns.Value) bool {
	if pg.After == nil {
		return true
	}
	for i, k := range pg.Order {
		c := orderValues(vals[k.Var], pg.After[i])
		if k.Desc {
			c = -c
		}
		if c != 0 {
			return c > 0
		}
	}
	return false
}

// orderTie compares two answers by a page's keys and columns, 0 when the order can't tell them apart.
func orderTie(pg *ns.Page, x, y map[int]ns.Value) int {
	for _, k := range pg.Order {
		if o := orderValues(x[k.Var], y[k.Var]); o != 0 {
			return o
		}
	}
	for _, v := range pg.Columns {
		if o := orderValues(x[v], y[v]); o != 0 {
			return o
		}
	}
	return 0
}

// summaryOf groups rows, c's matches, by their values at s.Group and s.Values, read as their types and
// identical in text, number and unit, counting each group's matches and gathering its citations, and
// returns each row's values as stored; split sends each match as a row of its own.
func summaryOf(c ns.Conjunction, s ns.Summary, schema func(string) ns.Schema, rows [][]ns.Tuple, split bool) []ns.SummaryRow {
	var out []ns.SummaryRow
	at := map[string]int{}
	bounds := map[string]*ns.Value{}
	for i, row := range rows {
		vals := rowValues(c, schema, row)
		raw := rawValues(c, row)
		var r ns.SummaryRow
		var sb strings.Builder
		for _, v := range s.Group {
			r.Group = append(r.Group, raw[v])
			sb.WriteString(identityKey(vals[v]))
		}
		for _, v := range s.Values {
			r.Values = append(r.Values, raw[v])
			sb.WriteString(identityKey(vals[v]))
		}
		k := sb.String()
		if split {
			k += strconv.Itoa(i)
		}
		j, ok := at[k]
		if !ok {
			j = len(out)
			at[k] = j
			r.Extremes = make([][]ns.Extreme, len(s.Extremes))
			out = append(out, r)
		}
		out[j].Matches++
		for _, t := range row {
			out[j].Cites = append(out[j].Cites, t.Cites...)
		}
		for i, v := range s.Extremes {
			bound := func(kind, unit string) *ns.Value {
				k := fmt.Sprint(j, "\x00", i, "\x00", kind, "\x00", unit)
				if bounds[k] == nil {
					bounds[k] = &ns.Value{}
				}
				return bounds[k]
			}
			out[j].Extremes[i] = addExtreme(out[j].Extremes[i], vals[v], raw[v], bound)
		}
	}
	return out
}

// addExtreme folds a match's value, read as its type, into a row's extremes by unit: a number joins
// its unit's count, NaN or bounds, which the row reports as stored, as a store would; anything else
// adds nothing. bound holds each of the row's bounds as read, by kind and unit, to compare by.
func addExtreme(es []ns.Extreme, read, raw ns.Value, bound func(kind, unit string) *ns.Value) []ns.Extreme {
	if read.Absent || read.Num == nil {
		return es
	}
	i := slices.IndexFunc(es, func(e ns.Extreme) bool { return e.Unit == read.BaseUnit })
	if i < 0 {
		es = append(es, ns.Extreme{Unit: read.BaseUnit, Min: ns.Absent(), Max: ns.Absent()})
		i = len(es) - 1
	}
	e := &es[i]
	e.Numbers++
	x := *read.Num
	switch {
	case x != x:
		e.NaN = true
	default:
		if lo := bound("min", read.BaseUnit); e.Min.Absent || x < *lo.Num {
			e.Min, *lo = raw, read
		}
		if hi := bound("max", read.BaseUnit); e.Max.Absent || x > *hi.Num {
			e.Max, *hi = raw, read
		}
	}
	return es
}

// rawValues is a match's value for each variable of c as the store holds it, which a summary returns:
// the engine reads it as its argument's type.
func rawValues(c ns.Conjunction, row []ns.Tuple) map[int]ns.Value {
	vals := map[int]ns.Value{}
	for k, a := range c.Atoms {
		for pos, x := range a.Args {
			if _, ok := vals[x.Var]; x.Var > 0 && !ok {
				vals[x.Var] = row[k].Vals[pos]
			}
		}
	}
	return vals
}

// identityKey keys a value by everything an aggregate reads of it: absent, its text, number and unit.
func identityKey(v ns.Value) string {
	num := ""
	if v.Num != nil {
		num = ftoa(*v.Num)
	}
	return fmt.Sprintf("%v\x00%s\x00%s\x00%s\x00", v.Absent, v.S, num, v.BaseUnit)
}

// evalConjunction is a store's own answer to c: each combination of the facts candidates offers per
// atom (given the values bound so far) whose values, read as their schema's types, equal the atom's
// constants (read so too) and agree on each variable, and whose numbers pass c's comparisons. Any
// other comparison is left to the engine, as a store unsure of the engine's order may. When c is
// Partial, each combination satisfying the first k atoms is a row too. sloppy ignores constants and
// comparisons.
func evalConjunction(c ns.Conjunction, schema func(string) ns.Schema, candidates func(rel string, bound map[int]ns.Value) []ns.Tuple, sloppy bool) [][]ns.Tuple {
	var out [][]ns.Tuple
	vals := map[int]ns.Value{}
	row := make([]ns.Tuple, len(c.Atoms))
	var walk func(k int)
	walk = func(k int) {
		if c.Partial && k > 0 && k < len(c.Atoms) {
			out = append(out, slices.Clone(row[:k]))
		}
		if k == len(c.Atoms) {
			if c.PrefixesOnly {
				return
			}
			if sloppy || numbersPass(c.Compare, vals) {
				out = append(out, slices.Clone(row))
			}
			return
		}
		a := c.Atoms[k]
		types := schema(a.Relation).Types
		consts := map[int]ns.Value{}
		bound := map[int]ns.Value{}
		for i, x := range a.Args {
			if x.Const != nil {
				consts[i] = normalizeTuples([]ns.Tuple{{Vals: []ns.Value{*x.Const}}}, types[min(i, len(types)):min(i+1, len(types))])[0].Vals[0]
				if !sloppy {
					bound[i] = consts[i]
				}
			}
			if v, ok := vals[x.Var]; x.Var > 0 && ok {
				bound[i] = v
			}
		}
		for _, t := range candidates(a.Relation, bound) {
			read := normalizeTuples([]ns.Tuple{t}, types)[0]
			var set []int
			ok := true
			for i, x := range a.Args {
				v := read.Vals[i]
				switch {
				case x.Const != nil:
					ok = sloppy || valueEq(v, consts[i])
				case x.Var > 0:
					if w, has := vals[x.Var]; has {
						ok = valueEq(v, w)
					} else {
						vals[x.Var] = v
						set = append(set, x.Var)
					}
				}
				if !ok {
					break
				}
			}
			if ok {
				row[k] = t
				walk(k + 1)
			}
			for _, n := range set {
				delete(vals, n)
			}
		}
	}
	walk(0)
	return out
}

// numbersPass reports whether vals pass each comparison between two numbers.
func numbersPass(cmps []ns.PushComparison, vals map[int]ns.Value) bool {
	side := func(a ns.PushArg) ns.Value {
		if a.Const != nil {
			return *a.Const
		}
		return vals[a.Var]
	}
	for _, c := range cmps {
		l, r := side(c.Left), side(c.Right)
		if l.Num != nil && r.Num != nil && !l.Absent && !r.Absent && !cmpNum(*l.Num, c.Op, *r.Num) {
			return false
		}
	}
	return true
}

// pushedQuestions join within the graph: from a constant, with a comparison, in a rule, in a
// recursive rule, and written in an order the plan doesn't run them in, so a witness has to put the
// pushed facts back in written order.
var pushedQuestions = []string{
	`edge("a", ?m), edge(?m, ?o) => ?o`,
	`edge(?x, ?y), weight(?y, ?w), ?w > 2 => ?x, ?w`,
	`p(?x, ?z) :- edge(?x, ?y), edge(?y, ?z); p(?x, ?z) :- edge(?x, ?z), node(?z); p(?a, ?b) => ?a, ?b`,
	`r(?a, ?b) :- edge(?a, ?b); r(?a, ?c) :- edge(?b, ?m), edge(?m, ?c), r(?a, ?b); r(?x, ?y) => ?x, ?y`,
	`weight(?y, ?w), edge("a", ?y) => ?w`,
}

// A semi-naive round may start from the delta of a recursive call that binds a pushed group's
// variables, which it does here because the generator the planner runs first gives the round no size
// to compare the delta with (scanSize). The group is then solved literal by literal, and answers as
// in memory.
func TestABoundGroupIsSolvedLiteralByLiteral(t *testing.T) {
	chain := ns.NewMemSource().Declare("edge", "from", "to")
	for i := range 8 {
		chain.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprint("n", i)), ns.S(fmt.Sprint("n", i+1))}, Cites: []string{fmt.Sprint("e", i)}})
	}
	q := mustParse(t, `r(?a, ?b) :- edge(?a, ?b); r(?a, ?c) :- str.distance("a", "b", ?k), edge(?b, ?m), edge(?m, ?c), r(?a, ?b); r(?x, ?y) => ?x, ?y`)
	want, err := both(q, baseFor(std(chain)))
	if err != nil {
		t.Fatal(err)
	}
	for _, opts := range [][]Option{nil, {Witnesses(), CanonicalCites()}} {
		w, _ := Naive{}.Eval(bg, q, baseFor(std(chain)), opts...)
		src := pushingAt(chain)
		var r Report
		got, err := SemiNaive{}.Eval(bg, q, MustBase(std(chain), src), append(opts, Explain(&r))...)
		if err != nil || !reflect.DeepEqual(got, w) {
			t.Errorf("with %d options: pushed %v %v, in memory %v", len(opts), binds(got), err, binds(w))
		}
		unpushed := 0
		for _, br := range r.Rules {
			for _, l := range br.Literals {
				if strings.HasPrefix(l.Literal, "pushed{") && !slices.Contains(l.Access, "pushed") {
					unpushed += int(l.Passed)
				}
			}
		}
		if src.pushes() != 1 || unpushed == 0 {
			t.Errorf("control: %d pushes, and the rounds starting from the delta passed %d bindings through the group, want one push and some:\n%s", src.pushes(), unpushed, r.String())
		}
	}
	if len(want) <= 8 {
		t.Errorf("control: %d answers, want more than the chain's 8 edges", len(want))
	}
}

// A conjunction the Source evaluates answers as its literals evaluated in memory do: the same rows,
// citations and witnesses, from a store that joins exactly and from a sloppy one whose extra rows the
// engine drops.
func TestAPushedConjunctionAnswersAsItsLiteralsDo(t *testing.T) {
	whole := baseFor(std(graph()))
	for _, sloppy := range []bool{false, true} {
		for _, text := range pushedQuestions {
			q := mustParse(t, text)
			src := pushingAt(graph())
			src.sloppy = sloppy
			b := MustBase(std(graph()), src)
			want, err := both(q, whole)
			if err != nil {
				t.Fatal(err)
			}
			got, err := SemiNaive{}.Eval(bg, q, b)
			if err != nil || !reflect.DeepEqual(binds(got), binds(want)) {
				t.Errorf("%s (sloppy %v): pushed %v %v, in memory %v", text, sloppy, binds(got), err, binds(want))
			}
			if src.pushes() == 0 {
				t.Errorf("control: %s pushed nothing", text)
			}
			for _, opts := range [][]Option{{Witnesses()}, {Witnesses(), CanonicalCites()}} {
				w, _ := Naive{}.Eval(bg, q, whole, opts...)
				g, err := SemiNaive{}.Eval(bg, q, MustBase(std(graph()), src), opts...)
				if err != nil || !reflect.DeepEqual(w, g) {
					t.Errorf("%s (sloppy %v) with %d options: pushed %v %v, in memory %v", text, sloppy, len(opts), g, err, w)
				}
			}
		}
	}
	src := pushingAt(graph())
	src.sloppy = true
	if _, err := (SemiNaive{}).Eval(bg, mustParse(t, pushedQuestions[0]), MustBase(std(graph()), src)); err != nil || src.yielded <= 1 {
		t.Errorf("control: the sloppy store yielded %d rows for one answer, want more for the engine to drop: %v", src.yielded, err)
	}
}

// Two hops from a constant are one call to the Source, and no lookups.
func TestTwoHopsArePushedAsOneCall(t *testing.T) {
	src := pushingAt(graph())
	got, err := SemiNaive{}.Eval(bg, mustParse(t, pushedQuestions[0]), MustBase(std(graph()), src))
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", binds(got), err)
	}
	if src.pushes() != 1 || len(src.lookups) != 0 {
		t.Errorf("pushed %d conjunctions and looked up %v, want one push and no lookups", src.pushes(), src.lookups)
	}
	c := src.pushed[0]
	if len(c.Atoms) != 2 || c.Atoms[0].Args[0].Const == nil || c.Atoms[0].Args[1].Var != c.Atoms[1].Args[0].Var || c.Atoms[0].Args[1].Var == 0 {
		t.Errorf("pushed %+v, want edge(\"a\", ?1), edge(?1, ?2)", c)
	}
	refusing := pushingAt(graph())
	refusing.refuse = func(ns.Conjunction) bool { return true }
	if _, err := (SemiNaive{}).Eval(bg, mustParse(t, pushedQuestions[0]), MustBase(std(graph()), refusing)); err != nil || refusing.pushes() != 0 || len(refusing.lookups) == 0 {
		t.Errorf("control: a store refusing every conjunction was pushed %d and looked up %v: %v", refusing.pushes(), refusing.lookups, err)
	}
}

// The comparisons over a pushed group go with it when the store takes them, and the group is pushed
// without them when it doesn't.
func TestComparisonsArePushedWhenTheStoreTakesThem(t *testing.T) {
	q := mustParse(t, pushedQuestions[1])
	for _, noCompare := range []bool{false, true} {
		src := pushingAt(graph())
		src.noCompare = noCompare
		if _, err := (SemiNaive{}).Eval(bg, q, MustBase(std(graph()), src)); err != nil || src.pushes() != 1 {
			t.Fatalf("noCompare %v: %d pushes: %v", noCompare, src.pushes(), err)
		}
		cmps := src.pushed[0].Compare
		if want := map[bool]int{false: 1, true: 0}[noCompare]; len(cmps) != want {
			t.Errorf("noCompare %v: pushed %+v, want %d comparisons", noCompare, cmps, want)
		}
		if !noCompare && (cmps[0].Op != ">" || cmps[0].Left.Var == 0 || cmps[0].Right.Const == nil) {
			t.Errorf("pushed the comparison as %+v, want ?w > 2", cmps[0])
		}
	}
}

// A group whose variables something earlier binds isn't pushed: it would be a call per binding, where
// its literals' lookups batch. The plan says so, since a group pushed anyway would be solved literal by
// literal at run time and make no call either.
func TestAGroupBoundPartwayIsNotPushed(t *testing.T) {
	q := mustParse(t, `edge(?x, ?y), edge(?y, ?z) => ?x, ?z`)
	pushedIn := func(bind map[Var][]ns.Value) bool {
		var r Report
		if _, err := (SemiNaive{}).Eval(bg, q, MustBase(std(graph()), pushingAt(graph())), Bind(bind), Explain(&r)); err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(r.Goal.Literals, func(l *LiteralReport) bool { return strings.HasPrefix(l.Literal, "pushed{") })
	}
	if pushedIn(map[Var][]ns.Value{"x": {ns.S("a"), ns.S("b")}}) {
		t.Errorf("with ?x bound to two values, the goal pushed its edges")
	}
	if !pushedIn(map[Var][]ns.Value{"x": {ns.S("a")}}) {
		t.Errorf("control: with ?x bound to one value, a constant, the goal pushed nothing")
	}
}

// A pushed row's values are read as their arguments' types, as a lookup's are: a count stored as the
// text "02" is the number 2 a constant names.
func TestAPushedRowIsReadAsItsTypes(t *testing.T) {
	m := ns.NewMemSource().
		DeclareSchema("pins", ns.Schema{Arity: 2, Labels: []string{"net", "count"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}}).
		Declare("tied", "net", "other")
	m.Add("pins", ns.Tuple{Vals: []ns.Value{ns.S("VBUS"), ns.S("02")}}).
		Add("tied", ns.Tuple{Vals: []ns.Value{ns.S("VBUS"), ns.S("USB")}})
	q := mustParse(t, `pins(?n, 2), tied(?n, ?o) => ?o`)
	src := pushingAt(m)
	got, err := SemiNaive{}.Eval(bg, q, MustBase(std(m), src))
	if err != nil || len(got) != 1 || src.pushes() != 1 {
		t.Errorf("pushed %d times, answered %v %v, want USB", src.pushes(), binds(got), err)
	}
	if want, err := both(q, baseFor(std(m))); err != nil || len(want) != 1 {
		t.Errorf("control: in memory, %v %v", binds(want), err)
	}
}

// A failed push fails the query, wrapped with the conjunction, and isn't kept.
func TestAFailedPushIsReportedAndNotKept(t *testing.T) {
	src := pushingAt(graph())
	broken := errors.New("disk on fire")
	src.pushFail = broken
	b := MustBase(std(graph()), src)
	q := mustParse(t, pushedQuestions[0])
	_, err := SemiNaive{}.Eval(bg, q, b)
	if !errors.Is(err, broken) || !strings.Contains(err.Error(), `query: pushing edge("a", ?m), edge(?m, ?o): `) {
		t.Fatalf("got %v, want the store's error wrapped with the conjunction", err)
	}
	src.pushFail = nil
	if got, err := (SemiNaive{}).Eval(bg, q, b); err != nil || len(got) != 1 || src.pushes() != 2 {
		t.Errorf("after the failure, %v %v in %d pushes, want one row from a second push", binds(got), err, src.pushes())
	}
}

// A push is charged to the budget row by row, so a budgeted query stops it rather than holding every
// row first.
func TestAPushStopsAtTheBudget(t *testing.T) {
	src := pushingAt(fanned(500))
	q := mustParse(t, `edge("a", ?n), attr(?n, ?v) => ?n, ?v`)
	_, err := SemiNaive{}.Eval(bg, q, MustBase(std(fanned(500)), src), Budget(50))
	if over := (*BudgetExceeded)(nil); !errors.As(err, &over) {
		t.Fatalf("got %v, want the budget exceeded", err)
	}
	if src.pushes() != 1 || src.yielded > 51 {
		t.Errorf("%d pushes yielded %d rows under a budget of 50, want one push stopped there", src.pushes(), src.yielded)
	}
	whole := pushingAt(fanned(500))
	if _, err := (SemiNaive{}).Eval(bg, q, MustBase(std(fanned(500)), whole)); err != nil || whole.yielded != 500 {
		t.Errorf("control: unbudgeted, the push yielded %d rows, want 500: %v", whole.yielded, err)
	}
}

// Explain counts a push as one call fetching its rows' tuples, and a repeated question as kept.
func TestExplainReportsAPush(t *testing.T) {
	src := pushingAt(graph())
	b := MustBase(std(graph()), src)
	q := mustParse(t, pushedQuestions[0])
	var r Report
	if _, err := (SemiNaive{}).Eval(bg, q, b, Explain(&r)); err != nil {
		t.Fatal(err)
	}
	if r.Pushes != 1 || r.Fetched != 2 || r.Lookups != 0 {
		t.Errorf("reported %d pushes fetching %d tuples and %d lookups, want 1, 2 and 0:\n%s", r.Pushes, r.Fetched, r.Lookups, r.String())
	}
	if lit := r.Goal.Literals[0]; !strings.HasPrefix(lit.Literal, `pushed{edge("a", ?m), edge(?m, ?o)}`) || !slices.Contains(lit.Access, "pushed") {
		t.Errorf("the goal's first literal is %q read %v, want the pushed group", lit.Literal, lit.Access)
	}
	if _, err := (SemiNaive{}).Eval(bg, q, b, Explain(&r)); err != nil {
		t.Fatal(err)
	}
	if r.Pushes != 0 || len(r.Sources) != 1 || r.Sources[0].Kept != 1 {
		t.Errorf("asked again: %d pushes, sources %+v, want the first push kept:\n%s", r.Pushes, r.Sources, r.String())
	}
	// The second Eval answered from what the first kept, so Explain reran it cold, which pushed again.
	if r.Cold == nil || r.Cold.Pushes != 1 || src.pushes() != 2 {
		t.Errorf("asked again, the cold cost is %+v after %d pushes, want the cold run's own push", r.Cold, src.pushes())
	}
}

// A recursive rule whose pushed group runs first keeps starting each round from it, a push the run
// already holds, rather than from the delta, which would bind the group and solve it literal by
// literal.
func TestARecursiveRoundKeepsItsPush(t *testing.T) {
	src := pushingAt(graph())
	var r Report
	got, err := SemiNaive{}.Eval(bg, mustParse(t, pushedQuestions[3]), MustBase(std(graph()), src), Explain(&r))
	if err != nil || len(got) == 0 {
		t.Fatalf("%v %v", binds(got), err)
	}
	rounds := 0
	for _, br := range r.Rules {
		for _, l := range br.Literals {
			if !strings.HasPrefix(l.Literal, "pushed{") {
				continue
			}
			rounds += br.Runs
			if !reflect.DeepEqual(l.Access, []string{"pushed"}) {
				t.Errorf("%s ran %s read %v, want it pushed every round", br.Ran, l.Literal, l.Access)
			}
		}
	}
	if src.pushes() != 1 || rounds < 2 {
		t.Errorf("control: pushed %d times over %d runs of the group's bodies, want one push read in two rounds or more:\n%s", src.pushes(), rounds, r.String())
	}
}
