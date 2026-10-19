package datalog

import (
	"container/heap"
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/panyam/jaala/ns"
)

// The goal's answer is built as its bindings arrive rather than from all of them at once (#120): a
// projection keeps one row per distinct answer, and an aggregate one accumulator per group, so what an
// Eval holds grows with its answer, not with the bindings that produced it.

// errEnough stops the goal's solve once a limit without an order by has its rows (#194). It never
// reaches a host, so it carries no query: prefix and isn't on the error catalogue.
var errEnough = errors.New("enough rows")

// exactSum adds numbers exactly (Shewchuk's algorithm, as Python's math.fsum does), so a sum is the
// true sum rounded once, whatever order its numbers arrive in. Infinities and NaNs are kept apart,
// where adding them is order-free (+Inf and -Inf make NaN). A finite sum that overflows is infinite.
type exactSum struct {
	partials []float64
	special  float64
	inf      bool
}

func (s *exactSum) add(x float64) {
	if math.IsInf(x, 0) || math.IsNaN(x) {
		s.special += x
		s.inf = true
		return
	}
	i := 0
	for _, y := range s.partials {
		if math.Abs(x) < math.Abs(y) {
			x, y = y, x
		}
		hi := x + y
		lo := y - (hi - x)
		if lo != 0 {
			s.partials[i] = lo
			i++
		}
		x = hi
	}
	if math.IsInf(x, 0) {
		s.special += x
		s.inf = true
		x = 0
	}
	s.partials = append(s.partials[:i], x)
}

// addTimes adds x n times: x*n as its rounded product and the product's rounding error, which together
// are x*n exactly, so a summary row of n matches sums as its matches would (#240). An infinity or NaN
// adds once, as n of them would, and a product that overflows adds x n times.
func (s *exactSum) addTimes(x float64, n int) {
	if n == 1 || math.IsInf(x, 0) || math.IsNaN(x) {
		s.add(x)
		return
	}
	m := float64(n)
	p := x * m
	if math.IsInf(p, 0) {
		for range n {
			s.add(x)
		}
		return
	}
	s.add(p)
	s.add(math.FMA(x, m, -p))
}

func (s *exactSum) value() float64 {
	if s.inf {
		return s.special
	}
	n := len(s.partials)
	if n == 0 {
		return 0
	}
	n--
	hi, lo := s.partials[n], 0.0
	for n > 0 {
		x := hi
		n--
		y := s.partials[n]
		hi = x + y
		lo = y - (hi - x)
		if lo != 0 {
			break
		}
	}
	// Round half to even: when the rest of the partials push the same way as lo, the true sum is
	// past the halfway point hi + lo sits on.
	if n > 0 && (lo < 0 && s.partials[n-1] < 0 || lo > 0 && s.partials[n-1] > 0) {
		y := lo * 2
		x := hi + y
		if y == x-hi {
			hi = x
		}
	}
	if hi == 0 {
		return 0 // not -0
	}
	return hi
}

// accumulator is one aggregate's state for one group: what its result needs, never the bindings.
type accumulator struct {
	a        Aggregate
	bindings int                 // count: every binding, bound or not
	keys     map[string]ns.Value // count and list distinct: one value per key, the one ordering first
	list     []ns.Value          // list: every value
	sum      exactSum            // sum, avg
	seen     map[float64]bool    // sum and avg distinct
	best     float64             // min, max
	numbers  int
	nan      bool
	units    []string
}

func (acc *accumulator) add(val ns.Value, ok bool) { acc.addN(val, ok, 1) }

// addN adds n bindings with the value val, as n calls of add would: a store's summary row stands for
// n matches (#240). A sum adds val times n exactly, through its product's rounding error.
func (acc *accumulator) addN(val ns.Value, ok bool, n int) {
	acc.bindings += n
	switch acc.a.Func {
	case "count", "list":
		if !ok || val.Absent || val.S == "" {
			return
		}
		if acc.a.Distinct {
			k := valueKey(val)
			if cur, had := acc.keys[k]; !had || valueBefore(val, cur) {
				if acc.keys == nil {
					acc.keys = map[string]ns.Value{}
				}
				acc.keys[k] = val
			}
		} else if acc.a.Func == "list" {
			for range n {
				acc.list = append(acc.list, val)
			}
		}
		return
	}
	if !ok || val.Num == nil {
		return
	}
	// The unit is read before distinct, since 5 V and 5 A are one number in two units (#80).
	if val.BaseUnit != "" && !containsString(acc.units, val.BaseUnit) {
		acc.units = append(acc.units, val.BaseUnit)
	}
	x := *val.Num
	switch acc.a.Func {
	case "sum", "avg":
		if acc.a.Distinct {
			if acc.seen[x] {
				return
			}
			if acc.seen == nil {
				acc.seen = map[float64]bool{}
			}
			acc.seen[x] = true
			n = 1
		}
		acc.sum.addTimes(x, n)
	case "min", "max":
		// A NaN makes the answer NaN, whenever it arrives, so the result doesn't depend on order.
		switch {
		case math.IsNaN(x):
			acc.nan = true
		case acc.numbers == 0, acc.a.Func == "min" && x < acc.best, acc.a.Func == "max" && x > acc.best:
			acc.best = x
		}
	}
	acc.numbers += n
}

// addExtremes adds n bindings whose numbers a store reduced to their extremes by unit (#252), as
// adding each binding would for min and max: every unit counts toward mixed units, a NaN makes the
// answer NaN, and the smallest (or largest) number wins.
func (acc *accumulator) addExtremes(n int, ext []ns.Extreme) {
	acc.bindings += n
	for _, e := range ext {
		if e.Numbers <= 0 {
			continue
		}
		if e.Unit != "" && !containsString(acc.units, e.Unit) {
			acc.units = append(acc.units, e.Unit)
		}
		if e.NaN {
			acc.nan = true
		}
		v := e.Min
		if acc.a.Func == "max" {
			v = e.Max
		}
		if !v.Absent && v.Num != nil {
			x := *v.Num
			if acc.numbers == 0 || acc.a.Func == "min" && x < acc.best || acc.a.Func == "max" && x > acc.best {
				acc.best = x
			}
		}
		acc.numbers += e.Numbers
	}
}

// result is the aggregate's value, and why when it is absent (Row.Why): count is how many; list joins
// the values in answer order; min, max, sum and avg reduce the numbers, which over none is 0 for sum
// and absent for the others (#122), and over numbers in different units is absent for all four (#80).
// Otherwise the result carries the group's unit. avg is the exact sum over how many numbers it
// added, so it doesn't depend on arrival either (#284).
func (acc *accumulator) result() (ns.Value, string) {
	switch acc.a.Func {
	case "count":
		n := float64(acc.bindings)
		if acc.a.Distinct {
			n = float64(len(acc.keys))
		}
		return ns.Value{S: ftoa(n), Num: &n}, ""
	case "list":
		vals := acc.list
		if acc.a.Distinct {
			vals = make([]ns.Value, 0, len(acc.keys))
			for _, v := range acc.keys {
				vals = append(vals, v)
			}
		}
		sort.SliceStable(vals, func(i, j int) bool { return orderValues(vals[i], vals[j]) < 0 })
		out := make([]string, len(vals))
		for i, v := range vals {
			out[i] = v.S
		}
		return ns.Value{S: strings.Join(out, listSep)}, ""
	}
	sort.Strings(acc.units)
	if len(acc.units) > 1 {
		return ns.Absent(), "mixed units: " + strings.Join(acc.units, ", ")
	}
	if acc.numbers == 0 {
		if acc.a.Func == "sum" {
			return ns.N(0), ""
		}
		if acc.bindings == 0 {
			return ns.Absent(), "no rows"
		}
		return ns.Absent(), "no numbers"
	}
	r := acc.best
	switch {
	case acc.a.Func == "sum":
		r = acc.sum.value()
	case acc.a.Func == "avg":
		r = acc.sum.value() / float64(acc.numbers)
	case acc.nan:
		r = math.NaN()
	}
	unit := ""
	if len(acc.units) == 1 {
		unit = acc.units[0]
	}
	return ns.Value{S: ftoa(r), Num: &r, BaseUnit: unit}, ""
}

// valueBefore reports whether a, one of two values sharing a key, is the one to keep: the first in
// answer order, then the annotation spelledBefore puts first (#148, #201).
func valueBefore(a, b ns.Value) bool {
	if c := orderValues(a, b); c != 0 {
		return c < 0
	}
	return spelledBefore(a, b)
}

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// aggregator groups bindings by the select's variable columns as they arrive and reduces each
// aggregate column per group, for a goal and for a rule head alike. A group's provenance is the union
// of its bindings' cites.
type aggregator struct {
	sel       []Term
	keyVars   []Var
	aggs      []Term // the selected aggregates, then the ones only a having reads
	selected  map[Var]bool
	having    []Compare
	canonical bool // read each value as canonicalAnswers writes it, as a goal's answers are
	groups    map[string]*aggGroup
	// across is, under Across (#181), the versions asked about; each group is then held as segments,
	// each reduced over the bindings holding in its versions (see addAcross), filed in groups by a
	// key of their own and listed by their group's key in segs.
	across ns.VersionSet
	segs   map[string][]*aggGroup
}

type aggGroup struct {
	keyVals []ns.Value
	cites   map[string]bool
	accs    []accumulator
	vs      ns.VersionSet // under Across, the versions this segment of its group holds in
}

func newAggregator(sel []Term, having []Compare, canonical bool) *aggregator {
	ag := &aggregator{sel: sel, having: having, canonical: canonical, selected: map[Var]bool{}, groups: map[string]*aggGroup{}}
	for _, t := range sel {
		ag.selected[colLabel(t)] = true
		if t.Agg != nil {
			ag.aggs = append(ag.aggs, t)
		} else if t.Var != "" {
			ag.keyVars = append(ag.keyVars, t.Var)
		}
	}
	added := map[Var]bool{}
	for _, h := range having {
		if lbl := colLabel(h.Left); !ag.selected[lbl] && !added[lbl] {
			ag.aggs = append(ag.aggs, h.Left)
			added[lbl] = true
		}
	}
	return ag
}

// newGroup is an empty group, keyed by bnd's values, or by nothing for a nil bnd.
func (ag *aggregator) newGroup(bnd *binding) *aggGroup {
	g := &aggGroup{keyVals: make([]ns.Value, len(ag.keyVars)), cites: map[string]bool{}, accs: make([]accumulator, len(ag.aggs))}
	for i, a := range ag.aggs {
		g.accs[i].a = *a.Agg
	}
	if bnd != nil {
		for i, kv := range ag.keyVars {
			g.keyVals[i], _ = ag.value(bnd, kv)
		}
	}
	return g
}

func (ag *aggregator) value(bnd *binding, v Var) (ns.Value, bool) {
	val, ok := bnd.get(v)
	if ok && ag.canonical {
		val = canonicalNumber(val)
	}
	return val, ok
}

func (ag *aggregator) add(bnd *binding) { ag.addSummary(bnd, 1, nil) }

// addN adds n bindings like bnd, as a store's summary row stands for n matches (#240).
func (ag *aggregator) addN(bnd *binding, n int) { ag.addSummary(bnd, n, nil) }

// addSummary adds n bindings like bnd, an aggregate over a variable in ext reading its extremes by
// unit rather than a value (#252).
func (ag *aggregator) addSummary(bnd *binding, n int, ext map[Var][]ns.Extreme) {
	key := groupKeyOf(ag.keyVars, bnd)
	if ag.across != nil {
		ag.addAcross(key, bnd, n, ext)
		return
	}
	g := ag.groups[key]
	if g == nil {
		g = ag.newGroup(bnd)
		ag.groups[key] = g
	}
	ag.addTo(g, bnd, n, ext)
}

// addTo adds n bindings like bnd to the group g (see addSummary).
func (ag *aggregator) addTo(g *aggGroup, bnd *binding, n int, ext map[Var][]ns.Extreme) {
	// One key, several spellings: keep the one ordering first, so arrival doesn't decide.
	for i, kv := range ag.keyVars {
		if v, _ := ag.value(bnd, kv); valueBefore(v, g.keyVals[i]) {
			g.keyVals[i] = v
		}
	}
	for _, c := range bnd.cites {
		if c != "" {
			g.cites[c] = true
		}
	}
	for i := range g.accs {
		if e, ok := ext[g.accs[i].a.Var]; ok {
			g.accs[i].addExtremes(n, e)
			continue
		}
		val, ok := ag.value(bnd, g.accs[i].a.Var)
		g.accs[i].addN(val, ok, n)
	}
}

// rows reduces every group, drops the ones a having rejects, and returns them in the default order.
// With no group-by column the whole answer is one group, and it exists even when nothing matched, so
// count over nothing answers 0 rather than no rows, as SQL's COUNT(*) does (agni issue 726).
func (ag *aggregator) rows() ([]Row, error) {
	if ag.across != nil {
		return ag.rowsAcross()
	}
	if len(ag.keyVars) == 0 && len(ag.groups) == 0 {
		ag.groups[""] = ag.newGroup(nil)
	}
	out := make([]Row, 0, len(ag.groups))
	for _, g := range ag.groups {
		row, keep, err := ag.rowOf(g)
		if err != nil {
			return nil, err
		}
		if keep {
			out = append(out, row)
		}
	}
	return dedupSort(out, selKeys(ag.sel)), nil
}

// rowOf reduces the group g to its row, and whether the having keeps it.
func (ag *aggregator) rowOf(g *aggGroup) (Row, bool, error) {
	row := Row{Bind: make(map[Var]ns.Value, len(ag.keyVars)+len(ag.aggs))}
	for i, kv := range ag.keyVars {
		row.Bind[kv] = g.keyVals[i]
	}
	cites := make([]string, 0, len(g.cites))
	for c := range g.cites {
		cites = append(cites, c)
	}
	sort.Strings(cites)
	row.Cites = cites
	for i, a := range ag.aggs {
		v, why := g.accs[i].result()
		row.Bind[colLabel(a)] = v
		if why != "" {
			if row.Why == nil {
				row.Why = map[Var]string{}
			}
			row.Why[colLabel(a)] = why
		}
	}
	keep, err := passesHaving(row, ag.having)
	if err != nil || !keep {
		return Row{}, false, err
	}
	// Drop the columns only a having asked for: Row.Bind is public, and a consumer walking it
	// would otherwise find a column the query never asked for.
	for _, h := range ag.having {
		if lbl := colLabel(h.Left); !ag.selected[lbl] {
			delete(row.Bind, lbl)
			delete(row.Why, lbl)
		}
	}
	return row, true, nil
}

// answerRow is one answer row while the goal is solved: its values in the answer's columns, as
// written, with the citations, witness and reasons that go with them. bnd is kept only to break a tie
// between two derivations of one row under CanonicalCites.
type answerRow struct {
	vals  []ns.Value
	cites []string
	wit   []*Witness
	why   []string
	bnd   *binding
	key   string
	slot  int           // its place in the page heap
	vs    ns.VersionSet // under Across, the versions this copy of the row holds in (see rowAlts)
}

// order is the answer's order on answerRows: the order by columns, then the default order over the
// evaluated columns, as orderRows after dedupSort gives it.
type order struct {
	by      []int
	desc    []bool
	deflt   []int
	canonic bool
}

func (o *order) compareDefault(a, b *answerRow) int {
	for _, i := range o.deflt {
		if c := orderValues(a.vals[i], b.vals[i]); c != 0 {
			return c
		}
	}
	return 0
}

func (o *order) compare(a, b *answerRow) int {
	for j, i := range o.by {
		c := orderValues(a.vals[i], b.vals[i])
		if o.desc[j] {
			c = -c
		}
		if c != 0 {
			return c
		}
	}
	return o.compareDefault(a, b)
}

// survives reports whether a, a row with b's key, replaces it: the one ordering first by the default
// order, as dedupSort keeps, then under CanonicalCites the shortest derivation (compareBindings).
func (o *order) survives(a, b *answerRow) bool {
	if c := o.compareDefault(a, b); c != 0 {
		return c < 0
	}
	// Equal values can differ in annotation (9 and 9 A): the first one spelledBefore picks wins, so
	// it doesn't hang on which derivation each evaluator found (#201).
	for _, i := range o.deflt {
		if spelledBefore(a.vals[i], b.vals[i]) {
			return true
		}
		if spelledBefore(b.vals[i], a.vals[i]) {
			return false
		}
	}
	return o.canonic && compareBindings(a.bnd, b.bnd) < 0
}

// after reports whether a comes strictly after the cursor, whose values are the order by columns'.
func (o *order) after(a *answerRow, cursor []ns.Value) bool {
	for j, i := range o.by {
		c := orderValues(a.vals[i], cursor[j])
		if o.desc[j] {
			c = -c
		}
		if c != 0 {
			return c > 0
		}
	}
	return false
}

// pageHeap holds the rows of a page under construction with the one ordering last on top, so a
// better row evicts it.
type pageHeap struct {
	rows []*answerRow
	ord  *order
}

func (h *pageHeap) Len() int           { return len(h.rows) }
func (h *pageHeap) Less(i, j int) bool { return h.ord.compare(h.rows[i], h.rows[j]) > 0 }
func (h *pageHeap) Swap(i, j int) {
	h.rows[i], h.rows[j] = h.rows[j], h.rows[i]
	h.rows[i].slot, h.rows[j].slot = i, j
}
func (h *pageHeap) Push(x any) { r := x.(*answerRow); r.slot = len(h.rows); h.rows = append(h.rows, r) }
func (h *pageHeap) Pop() any {
	r := h.rows[len(h.rows)-1]
	h.rows = h.rows[:len(h.rows)-1]
	return r
}

// projector builds a projection's answer as bindings arrive: one row per distinct answer, so a goal
// that finds a million bindings of ten rows holds ten. With a page size (an order by and a limit) it
// holds at most that many; with a limit and no order by it stops the solve once it has them.
type projector struct {
	cols    []Var
	fixed   map[int]ns.Value // the columns the host bound to one value, filled as bound
	ord     *order
	rows    map[string]*answerRow
	page    *pageHeap // nil: hold every row
	size    int
	stopAt  int
	cursor  []ns.Value
	total   map[string]bool
	witness bool
	base    *Base
	negs    []Literal
	held    int
	scratch answerRow
	key     []byte
	// alts are, under Across, each row's copies by key, held instead of rows (see projector.addAcross).
	alts map[string]rowAlts
}

func (p *projector) add(bnd *binding) error {
	// The binding is read into a scratch row, and a row is allocated only when it is kept.
	r := &p.scratch
	if r.vals == nil {
		r.vals = make([]ns.Value, len(p.cols))
	}
	for i, c := range p.cols {
		if v, ok := p.fixed[i]; ok {
			r.vals[i] = v
		} else {
			r.vals[i] = canonicalNumber(bnd.value(c))
		}
	}
	p.key = p.key[:0]
	for _, i := range p.ord.deflt {
		p.key = append(p.key, valueKey(r.vals[i])...)
		p.key = append(p.key, '\x1f')
	}
	if p.total != nil && !p.total[string(p.key)] {
		p.total[string(p.key)] = true
	}
	if p.cursor != nil && !p.ord.after(r, p.cursor) {
		return nil
	}
	r.bnd = nil
	if p.ord.canonic {
		r.bnd = p.canonical(bnd)
	}
	if p.alts != nil {
		p.addAcross(r, bnd, r.bnd, bnd.versions(p.base.across.vs))
		return nil
	}
	cur, had := p.rows[string(p.key)]
	switch {
	case had && !p.ord.survives(r, cur):
		return nil
	case had:
		cur.vals = append(cur.vals[:0], r.vals...)
		p.keep(cur, bnd, r.bnd)
		if p.page != nil {
			heap.Fix(p.page, cur.slot)
		}
		return nil
	case p.page != nil && p.page.Len() == p.size:
		top := p.page.rows[0]
		if p.ord.compare(r, top) >= 0 {
			return nil
		}
		heap.Pop(p.page)
		delete(p.rows, top.key)
	}
	kept := &answerRow{vals: append([]ns.Value(nil), r.vals...), key: string(p.key)}
	p.keep(kept, bnd, r.bnd)
	p.rows[kept.key] = kept
	if p.page != nil {
		heap.Push(p.page, kept)
	}
	p.held = max(p.held, len(p.rows))
	if p.stopAt > 0 && len(p.rows) == p.stopAt && p.total == nil {
		return errEnough
	}
	return nil
}

// canonical is the binding as CanonicalCites compares it: a copy with its negations' witnesses, and
// its numbers written as canonicalAnswers writes them.
func (p *projector) canonical(bnd *binding) *binding {
	c := bnd.keep()
	c.wit = append(c.wit, p.base.negationWitnesses(p.negs, bnd)...)
	canonicalAnswers([]*binding{c})
	return c
}

// keep fills in what a kept row needs from the binding it came from: its citations, and its witness
// when the Eval records them. c is the binding's canonical copy, when there is one already.
func (p *projector) keep(r *answerRow, bnd, c *binding) {
	r.cites = dedupStrings(bnd.cites)
	r.bnd = c
	if !p.witness {
		return
	}
	if c == nil {
		c = p.canonical(bnd)
	}
	r.wit = nil
	if len(c.wit) > 0 {
		r.wit = inWrittenOrder(c.wit)
	}
}

// answer is the rows kept, in the answer's order.
func (p *projector) answer() []*answerRow {
	out := make([]*answerRow, 0, len(p.rows))
	for _, r := range p.rows {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return p.ord.compare(out[i], out[j]) < 0 })
	return out
}
