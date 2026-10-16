package datalog

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/panyam/jaala/ns"
)

// A goal whose whole body a Source could evaluate goes to it whole (#240, #241). A goal that
// aggregates asks the Source for a summary of its matches (ns.SummarySource), a row per group and
// distinct value with how many matches it covers, and reduces those with its own aggregator, so a
// count costs a row per store rather than a row per match. A goal with a limit asks for the matches
// behind its first rows (ns.Page) and answers from them as it would from every match. Either way the
// engine computes the answer, so its rules (exact sums, units, NaN, absent over nothing, the order,
// which binding a row cites) hold as in memory. Only the planned SemiNaive pushes a goal.

// goalGroup is the goal's body as one group the Source could take whole: base relations it serves and
// the Base doesn't hold, groups pushConjunctions made, and comparisons over their variables, with no
// negation. It is nil when the body is anything else.
func goalGroup(b *Base, pos, negs []Literal) *pushGroup {
	if b.run == nil || !b.run.batch || b.noIndex || b.pusher == nil || len(negs) > 0 {
		return nil
	}
	g := &pushGroup{}
	for _, lit := range pos {
		switch {
		case lit.Compare != nil:
			g.compare = append(g.compare, *lit.Compare)
		case lit.Pos != nil && isPushed(lit.Pos.Relation):
			g.lits = append(g.lits, b.run.pushed[lit.Pos.Relation].lits...)
		case b.pushable(lit):
			g.lits = append(g.lits, lit)
		default:
			return nil
		}
	}
	if len(g.lits) == 0 {
		return nil
	}
	vars := map[Var]bool{}
	texts := make([]string, len(g.lits))
	for i, lit := range g.lits {
		bindAll(lit.Pos, vars)
		texts[i] = lit.Pos.String()
	}
	for _, c := range g.compare {
		if !pushOperand(c.Left, vars) || !pushOperand(c.Right, vars) {
			return nil
		}
	}
	g.text = strings.Join(texts, ", ")
	return g
}

// summarizeGoal feeds ag the goal's matches as the Source summarizes them, and reports whether it
// did. The summary groups by ag's group columns and by the values its aggregates reduce, a plain
// count needing none, and by every variable a comparison reads, so the comparisons are checked on each
// row here whether or not the Source applied them.
func summarizeGoal(b *Base, g *pushGroup, ag *aggregator) (bool, error) {
	if b.summarizer == nil {
		return false, nil
	}
	c, nums := g.numbered(nil)
	var s ns.Summary
	var groupVars, valueVars, extremeVars []Var
	for _, kv := range ag.keyVars {
		n, ok := nums[kv]
		if !ok {
			return false, nil
		}
		s.Group = append(s.Group, n)
		groupVars = append(groupVars, kv)
	}
	// A variable only min and max read, and nothing compares, goes as Extremes (#252); any other
	// reduced or compared variable as Values.
	extreme := map[Var]bool{}
	for _, a := range ag.aggs {
		if f := a.Agg.Func; f == "min" || f == "max" {
			if _, held := extreme[a.Agg.Var]; !held {
				extreme[a.Agg.Var] = true
			}
		} else if f != "count" || a.Agg.Distinct {
			extreme[a.Agg.Var] = false
		}
	}
	for _, cmp := range g.compare {
		for _, t := range []Term{cmp.Left, cmp.Right} {
			if t.Var != "" {
				extreme[t.Var] = false
			}
		}
	}
	seen := map[Var]bool{}
	add := func(v Var) bool {
		n, ok := nums[v]
		if !ok || seen[v] {
			return ok
		}
		seen[v] = true
		if extreme[v] {
			s.Extremes = append(s.Extremes, n)
			extremeVars = append(extremeVars, v)
		} else {
			s.Values = append(s.Values, n)
			valueVars = append(valueVars, v)
		}
		return true
	}
	for _, a := range ag.aggs {
		if (a.Agg.Func != "count" || a.Agg.Distinct) && !add(a.Agg.Var) {
			return false, nil
		}
	}
	for _, cmp := range g.compare {
		for _, t := range []Term{cmp.Left, cmp.Right} {
			if t.Var != "" && !add(t.Var) {
				return false, nil
			}
		}
	}
	if !b.summarizer.Summarizes(c, s) {
		if len(s.Extremes) == 0 {
			return false, nil
		}
		s.Values = append(s.Values, s.Extremes...) // a store that can't report extremes groups by value
		valueVars = append(valueVars, extremeVars...)
		s.Extremes, extremeVars = nil, nil
		if !b.summarizer.Summarizes(c, s) {
			return false, nil
		}
	}
	var rows []summaryRow
	err := b.run.explain.at(0, func() error {
		var err error
		rows, err = b.summarize(g, c, s)
		return err
	})
	if err != nil {
		return true, err
	}
	vars := append(groupVars, valueVars...)
	for _, row := range rows {
		bnd := newBinding()
		for i, v := range vars {
			bnd.set(v, row.vals[i])
		}
		bnd.cites = row.cites
		pass := true
		for _, cmp := range g.compare {
			ok, err := evalCompare(cmp, bnd)
			if err != nil {
				return true, err
			}
			pass = pass && ok
		}
		if !pass {
			continue
		}
		var ext map[Var][]ns.Extreme
		if len(extremeVars) > 0 {
			ext = make(map[Var][]ns.Extreme, len(extremeVars))
			for i, v := range extremeVars {
				ext[v] = row.extremes[i]
			}
		}
		ag.addSummary(bnd, row.matches, ext)
	}
	return true, nil
}

// A summaryRow is a SummaryRow as the engine reads it: its group then values, read as their variables'
// types, its matches, citations and extremes (with Min and Max read so too).
type summaryRow struct {
	vals     []ns.Value
	matches  int
	cites    []string
	extremes [][]ns.Extreme
}

// summarize returns the Source's summary s of c; kept as a lookup is (as tuples, see encodeSummary),
// and each row charged to the budget as it arrives.
func (b *Base) summarize(g *pushGroup, c ns.Conjunction, s ns.Summary) ([]summaryRow, error) {
	key := summaryKey(c, s)
	r := b.run
	width := len(s.Group) + len(s.Values)
	if rows, hit, kept := b.lookedUp(key); hit {
		if r.explain != nil {
			b.explainPush(g, 0, !kept, kept)
		}
		return decodeSummary(rows, width, len(s.Extremes)), nil
	}
	if err := b.countWork(); err != nil {
		return nil, err
	}
	typeOf := g.varTypes(b, c)
	read := func(v ns.Value, n int) ns.Value {
		return normalizeTuples(b.units(), []ns.Tuple{{Vals: []ns.Value{v}}}, typeOf[n])[0].Vals[0]
	}
	ctx := r.context()
	var rows []summaryRow
	for row, err := range b.summarizer.Summarize(ctx, b.withUnits(c), s) {
		if err != nil {
			return nil, pushErr(ctx, g.text, err)
		}
		if err := r.readOne(); err != nil {
			return nil, err
		}
		if len(row.Group) != len(s.Group) || len(row.Values) != len(s.Values) || len(row.Extremes) != len(s.Extremes) || row.Matches < 0 {
			return nil, fmt.Errorf("query: summarizing %s: a row's values don't match the summary asked for", g.text)
		}
		vals := append(append(make([]ns.Value, 0, width), row.Group...), row.Values...)
		for i, n := range append(append([]int(nil), s.Group...), s.Values...) {
			vals[i] = read(vals[i], n)
		}
		ext := make([][]ns.Extreme, len(row.Extremes))
		for i, es := range row.Extremes {
			for _, e := range es {
				if !e.Min.Absent {
					e.Min = read(e.Min, s.Extremes[i])
				}
				if !e.Max.Absent {
					e.Max = read(e.Max, s.Extremes[i])
				}
				_, e.Unit, _ = b.units().Fold(1, e.Unit) // mV's extremes are V's, as their values are (#205)
				ext[i] = append(ext[i], e)
			}
		}
		rows = append(rows, summaryRow{vals: vals, matches: row.Matches, cites: row.Cites, extremes: ext})
	}
	b.keepLookup(key, encodeSummary(rows))
	if r.explain != nil {
		b.explainPush(g, len(rows), false, false)
	}
	return rows, nil
}

// encodeSummary writes summary rows as the tuples the lookup caches hold: each row's values, its
// match count, then per extremes variable the number of units and, per unit, its unit, numbers, NaN,
// min and max.
func encodeSummary(rows []summaryRow) []ns.Tuple {
	out := make([]ns.Tuple, len(rows))
	for k, r := range rows {
		vals := append(append([]ns.Value(nil), r.vals...), ns.N(float64(r.matches)))
		for _, es := range r.extremes {
			vals = append(vals, ns.N(float64(len(es))))
			for _, e := range es {
				nan := 0.0
				if e.NaN {
					nan = 1
				}
				vals = append(vals, ns.S(e.Unit), ns.N(float64(e.Numbers)), ns.N(nan), e.Min, e.Max)
			}
		}
		out[k] = ns.Tuple{Vals: vals, Cites: r.cites}
	}
	return out
}

// decodeSummary reads back what encodeSummary wrote, rows of width values and extremes variables.
func decodeSummary(tuples []ns.Tuple, width, extremes int) []summaryRow {
	out := make([]summaryRow, len(tuples))
	for k, t := range tuples {
		r := summaryRow{vals: t.Vals[:width], matches: int(*t.Vals[width].Num), cites: t.Cites, extremes: make([][]ns.Extreme, extremes)}
		at := width + 1
		for i := range extremes {
			units := int(*t.Vals[at].Num)
			at++
			for range units {
				v := t.Vals[at : at+5]
				r.extremes[i] = append(r.extremes[i], ns.Extreme{Unit: v[0].S, Numbers: int(*v[1].Num), NaN: *v[2].Num == 1, Min: v[3], Max: v[4]})
				at += 5
			}
		}
		out[k] = r
	}
	return out
}

// varTypes is, per variable number of c, the type of the argument it is first written at, which reads
// a summary's values as a match's would be.
func (g *pushGroup) varTypes(b *Base, c ns.Conjunction) map[int][]ns.ArgType {
	out := map[int][]ns.ArgType{}
	for _, a := range c.Atoms {
		types := b.argTypesOf(a.Relation)
		for p, x := range a.Args {
			if _, ok := out[x.Var]; x.Var > 0 && !ok {
				out[x.Var] = types[min(p, len(types)):min(p+1, len(types))]
			}
		}
	}
	return out
}

// summaryKey keys a summary in the lookup caches, apart from a push of the same conjunction.
func summaryKey(c ns.Conjunction, s ns.Summary) string {
	var sb strings.Builder
	sb.WriteString(pushKey(c))
	sb.WriteString("\x00summary")
	for _, n := range s.Group {
		sb.WriteString(",g" + strconv.Itoa(n))
	}
	for _, n := range s.Values {
		sb.WriteString(",v" + strconv.Itoa(n))
	}
	for _, n := range s.Extremes {
		sb.WriteString(",x" + strconv.Itoa(n))
	}
	return sb.String()
}

// pageGoal answers a goal with a limit from the matches the Source sends for its first rows (#241),
// feeding each match's binding to emit as solving would, and reports whether it did. The page is
// offset plus limit rows of the answer's columns, ordered by the order by and then the columns, as
// the answer is, after the after clause's values when it has one (#254); the projector applies the
// cursor too, so rows a store sends from before it are dropped. A goal with a comparison isn't paged: a store skipping one would fill its page with
// matches failing it.
func pageGoal(b *Base, g *pushGroup, cols []Term, fixed map[int]ns.Value, written Query, emit func(*binding) error) (bool, error) {
	if len(g.compare) > 0 {
		return false, nil
	}
	c, nums := g.numbered(nil)
	page := &ns.Page{Limit: written.Limit + written.Offset}
	isFixed := map[Var]bool{}
	for i, col := range cols {
		if _, ok := fixed[i]; ok {
			isFixed[col.Var] = true
			continue
		}
		n, ok := nums[col.Var]
		if col.Agg != nil || !ok {
			return false, nil
		}
		page.Columns = append(page.Columns, n)
	}
	for i, ob := range written.OrderBy {
		v := ob.Term.Var
		if isFixed[v] {
			continue
		}
		n, ok := nums[v]
		if ob.Term.Agg != nil || !ok {
			return false, nil
		}
		page.Order = append(page.Order, ns.PageKey{Var: n, Desc: ob.Desc})
		if written.After != nil {
			page.After = append(page.After, written.After[i])
		}
	}
	c.Page = page
	if !b.pusher.Pushes(c) {
		return false, nil
	}
	var rows []ns.Tuple
	err := b.run.explain.at(0, func() error {
		var err error
		rows, err = b.pushAs(g, c)
		return err
	})
	if err != nil {
		return true, err
	}
	n := len(g.lits)
	next := newBinding()
	for k := 0; k+n <= len(rows); k += n {
		if err := b.countWork(); err != nil {
			return true, err
		}
		next.reset(mark{})
		if !unifyRow(g.lits, rows[k:k+n], next) {
			continue
		}
		if b.witnessing() {
			for i, lit := range g.lits {
				t := rows[k+i]
				next.wit = append(next.wit, placed{at: lit.at, node: leaf(lit.Pos.Relation, t.Vals, t.Cites)})
			}
		}
		if err := emit(next); err != nil {
			if errors.Is(err, errEnough) {
				return true, nil
			}
			return true, err
		}
	}
	return true, nil
}
