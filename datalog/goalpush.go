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
	var groupVars, valueVars []Var
	for _, kv := range ag.keyVars {
		n, ok := nums[kv]
		if !ok {
			return false, nil
		}
		s.Group = append(s.Group, n)
		groupVars = append(groupVars, kv)
	}
	seen := map[Var]bool{}
	value := func(v Var) bool {
		n, ok := nums[v]
		if ok && !seen[v] {
			seen[v] = true
			s.Values = append(s.Values, n)
			valueVars = append(valueVars, v)
		}
		return ok
	}
	for _, a := range ag.aggs {
		if (a.Agg.Func != "count" || a.Agg.Distinct) && !value(a.Agg.Var) {
			return false, nil
		}
	}
	for _, cmp := range g.compare {
		for _, t := range []Term{cmp.Left, cmp.Right} {
			if t.Var != "" && !value(t.Var) {
				return false, nil
			}
		}
	}
	if !b.summarizer.Summarizes(c, s) {
		return false, nil
	}
	var rows []ns.Tuple
	err := b.run.explain.at(0, func() error {
		var err error
		rows, err = b.summarize(g, c, s, len(groupVars)+len(valueVars))
		return err
	})
	if err != nil {
		return true, err
	}
	vars := append(groupVars, valueVars...)
	for _, row := range rows {
		bnd := newBinding()
		for i, v := range vars {
			bnd.vals[v] = row.Vals[i]
		}
		bnd.cites = row.Cites
		pass := true
		for _, cmp := range g.compare {
			ok, err := evalCompare(cmp, bnd)
			if err != nil {
				return true, err
			}
			pass = pass && ok
		}
		if pass {
			ag.addN(bnd, int(*row.Vals[len(vars)].Num))
		}
	}
	return true, nil
}

// summarize returns the Source's summary s of c, each row as a tuple of its group and values read as
// their variables' types, then its match count, with its citations; kept as a lookup is, and each row
// charged to the budget as it arrives.
func (b *Base) summarize(g *pushGroup, c ns.Conjunction, s ns.Summary, width int) ([]ns.Tuple, error) {
	key := summaryKey(c, s)
	r := b.run
	if rows, hit, kept := b.lookedUp(key); hit {
		if r.explain != nil {
			b.explainPush(g, 0, !kept, kept)
		}
		return rows, nil
	}
	if err := b.countWork(); err != nil {
		return nil, err
	}
	typeOf := g.varTypes(b, c)
	ctx := r.context()
	var rows []ns.Tuple
	for row, err := range b.summarizer.Summarize(ctx, c, s) {
		if err != nil {
			return nil, pushErr(ctx, g.text, err)
		}
		if err := r.readOne(); err != nil {
			return nil, err
		}
		vals := append(append(make([]ns.Value, 0, width+1), row.Group...), row.Values...)
		if len(row.Group) != len(s.Group) || len(row.Values) != len(s.Values) || row.Matches < 0 {
			return nil, fmt.Errorf("query: summarizing %s: a row's values don't match the summary asked for", g.text)
		}
		for i, n := range append(append([]int(nil), s.Group...), s.Values...) {
			vals[i] = normalizeTuples([]ns.Tuple{{Vals: []ns.Value{vals[i]}}}, typeOf[n])[0].Vals[0]
		}
		rows = append(rows, ns.Tuple{Vals: append(vals, ns.N(float64(row.Matches))), Cites: row.Cites})
	}
	b.keepLookup(key, rows)
	if r.explain != nil {
		b.explainPush(g, len(rows), false, false)
	}
	return rows, nil
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
	return sb.String()
}

// pageGoal answers a goal with a limit from the matches the Source sends for its first rows (#241),
// feeding each match's binding to emit as solving would, and reports whether it did. The page is
// offset plus limit rows of the answer's columns, ordered by the order by and then the columns, as
// the answer is. A goal with a comparison isn't paged: a store skipping one would fill its page with
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
	for _, ob := range written.OrderBy {
		v := ob.Term.Var
		if isFixed[v] {
			continue
		}
		n, ok := nums[v]
		if ob.Term.Agg != nil || !ok {
			return false, nil
		}
		page.Order = append(page.Order, ns.PageKey{Var: n, Desc: ob.Desc})
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
	for k := 0; k+n <= len(rows); k += n {
		if err := b.countWork(); err != nil {
			return true, err
		}
		next, ok := newBinding(), true
		for i, lit := range g.lits {
			if next, ok = unify(lit.Pos.Args, rows[k+i], next); !ok {
				break
			}
		}
		if !ok {
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
