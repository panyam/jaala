package datalog

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/panyam/jaala/ns"
)

// A Source that implements ns.PushSource evaluates a conjunction over its own facts (#237), so a join
// within one store is one call that ships the rows that join, not each literal's tuples. After plan,
// the planned SemiNaive replaces each group of a body's base literals the Source agrees to push with
// one literal over a relation named for the group (pushPrefix), whose arguments are the group's
// variables; solving it asks the Source, and unifies each row's tuples with the group's literals as
// written, so a row the store got wrong is dropped and the binding, citations and witnesses are the
// ones solving the literals one by one would give. The comparisons over the group's variables go to
// the store as well when it takes them, and stay in the body, so the engine checks them too.
//
// A group may read variables an earlier literal binds, its inputs, when the Source takes them (#246):
// prefetchPushed then collects the bindings the body before the group gives them and asks for all of
// them in one call (Conjunction.Bindings), as a batched lookup does, filing each binding's rows where
// solving the group under that binding looks. Each literal joining a group shares a variable with it
// and none with a literal outside it before its place. Naive and WrittenOrder never push.

// pushPrefix starts a pushed group's relation name, which the parser never accepts.
const pushPrefix = "\x00p:"

func isPushed(rel string) bool { return strings.HasPrefix(rel, pushPrefix) }

// A pushGroup is a body's base literals pushed to the Source as one conjunction.
type pushGroup struct {
	lits    []Literal // as written in the planned body, with their written positions
	compare []Compare // the comparisons pushed with them, which the body keeps too
	text    string    // the literals as query text, for errors
}

// pushConjunctions replaces, in each body of q, the groups of base literals the Source pushes with one
// literal each (see pushBody). It runs after plan, on the order solve runs a body in.
func pushConjunctions(b *Base, q Query) Query {
	if b.pusher == nil || b.noIndex || b.run == nil {
		return q
	}
	out := q
	out.Rules = make([]Rule, len(q.Rules))
	for i, r := range q.Rules {
		r.Body = pushBody(b, r.Body)
		out.Rules[i] = r
	}
	out.Goal = pushBody(b, q.Goal)
	return out
}

// pushBody groups body's base literals for the Source: each literal that binds nothing bound before
// it starts a group, which takes each later literal sharing a variable with the group and none with a
// literal before it outside the group, for as long as the Source pushes the group (ns.PushSource's
// Pushes) and until a literal outside it only filters the group (see filtersGroup). A group of at least two literals becomes one literal, at its first literal's place, and so
// does one literal the store filters better than a whole read (#263, filtersAlone).
func pushBody(b *Base, body Body) Body {
	pos, negs := splitNegations(body.Literals)
	bound := map[Var]bool{}
	used := make([]bool, len(pos))
	var out []Literal
	for i, lit := range pos {
		if used[i] {
			continue
		}
		if g := growGroup(b, pos, i, bound, used); g != nil {
			atom := g.atom(b)
			out = append(out, Literal{Pos: atom})
			bindAll(atom, bound)
			continue
		}
		out = append(out, lit)
		if lit.Pos != nil {
			bindAll(lit.Pos, bound)
		}
	}
	return Body{Literals: append(out, negs...)}
}

// growGroup is the group starting at pos[i], or nil when none of two literals or more does and pos[i]
// alone isn't worth pushing, marking its members used.
func growGroup(b *Base, pos []Literal, i int, bound map[Var]bool, used []bool) *pushGroup {
	if !b.pushable(pos[i]) {
		return nil
	}
	members := []int{i}
	vars := map[Var]bool{}
	bindAll(pos[i].Pos, vars)
	outside := map[Var]bool{}
	for j := i + 1; j < len(pos); j++ {
		lit := pos[j]
		if used[j] || lit.Pos == nil {
			continue
		}
		if b.pushable(lit) && !sharesVar(lit.Pos, outside) && sharesVar(lit.Pos, vars) {
			g := groupOf(pos, append(members[:len(members):len(members)], j), nil)
			if b.pusher.Pushes(g.offered(bound)) {
				members = append(members, j)
				bindAll(lit.Pos, vars)
				continue
			}
		}
		if filtersGroup(lit.Pos, vars, bound) {
			break // what follows would join before this filter narrows the group (#291)
		}
		bindAll(lit.Pos, outside)
	}
	alone := len(members) == 1
	if alone && !filtersAlone(pos[i].Pos, bound) {
		return nil
	}
	var cmps []Compare
	for _, lit := range pos {
		if c := lit.Compare; c != nil && pushOperand(c.Left, vars) && pushOperand(c.Right, vars) {
			cmps = append(cmps, *c)
		}
	}
	g := groupOf(pos, members, cmps)
	if len(cmps) > 0 && !b.pusher.Pushes(g.offered(bound)) {
		g.compare = nil
	}
	if alone && ((g.compare == nil && !repeatsVar(pos[i].Pos)) || !b.pusher.Pushes(g.offered(bound))) {
		return nil // nothing the store filters by, or it doesn't push it
	}
	for _, m := range members {
		used[m] = true
	}
	return g
}

// filtersAlone reports whether a literal could be worth pushing alone (#263): nothing before it binds
// it and it has no constant, which a lookup answers already. Whether it is depends on what the store
// filters it by, a repeated variable or a comparison going with it.
func filtersAlone(a *Atom, bound map[Var]bool) bool {
	for _, t := range a.Args {
		if t.Const != nil || (t.Var != "" && t.Var != "_" && bound[t.Var]) {
			return false
		}
	}
	return true
}

// repeatsVar reports whether a variable appears twice in a, a join within one fact.
func repeatsVar(a *Atom) bool {
	seen := map[Var]bool{}
	for _, t := range a.Args {
		if t.Var == "" || t.Var == "_" {
			continue
		}
		if seen[t.Var] {
			return true
		}
		seen[t.Var] = true
	}
	return false
}

// offered is g as the planner offers it to Pushes: its variables that bound, what the literals before
// it bind, are its Inputs (#246), which a store answers for many bindings in one call.
func (g *pushGroup) offered(bound map[Var]bool) ns.Conjunction {
	c, nums := g.numbered(nil)
	for _, lit := range g.lits {
		for _, t := range lit.Pos.Args {
			if n := nums[t.Var]; n > 0 && bound[t.Var] && !slices.Contains(c.Inputs, n) {
				c.Inputs = append(c.Inputs, n)
			}
		}
	}
	slices.Sort(c.Inputs)
	return c
}

// pushable reports whether lit calls a base relation the Source could be asked to push: one it serves
// and the Base doesn't already hold whole, whose index answers without asking.
func (b *Base) pushable(lit Literal) bool {
	if lit.Pos == nil {
		return false
	}
	if _, base := b.schemaOf(lit.Pos.Relation); !base {
		return false
	}
	return b.edb == nil || !b.edb.holds(lit.Pos.Relation)
}

// pushOperand reports whether a comparison's operand can go to the Source: a constant, or a variable
// the group binds.
func pushOperand(t Term, vars map[Var]bool) bool {
	return t.Const != nil || (t.Agg == nil && t.Expr == nil && t.Var != "" && vars[t.Var])
}

// filtersGroup reports whether a, outside a group, reads one of the group's variables and binds
// nothing new: it only narrows what the group found, so a literal after it joins after the filter. A
// group growing past it made the store join every row the filter would have dropped (#291: Declaire's
// http.path pushed edge, attr and an argument edge over every callsite, where http.sends(?c) kept
// 1,228 of 80,491).
func filtersGroup(a *Atom, vars, bound map[Var]bool) bool {
	for _, t := range a.Args {
		if t.Var != "" && t.Var != "_" && !vars[t.Var] && !bound[t.Var] {
			return false
		}
	}
	return sharesVar(a, vars)
}

func sharesVar(a *Atom, vars map[Var]bool) bool {
	for _, t := range a.Args {
		if t.Var != "" && t.Var != "_" && vars[t.Var] {
			return true
		}
	}
	return false
}

func groupOf(pos []Literal, members []int, cmps []Compare) *pushGroup {
	g := &pushGroup{compare: cmps}
	texts := make([]string, len(members))
	for k, m := range members {
		g.lits = append(g.lits, pos[m])
		texts[k] = pos[m].Pos.String()
	}
	g.text = strings.Join(texts, ", ")
	return g
}

// atom is the literal standing for g in its body, over the variables g binds, in the order they're
// first written; the run keeps g under its name for extendPushed.
func (g *pushGroup) atom(b *Base) *Atom {
	a := &Atom{Relation: pushPrefix + "{" + g.text + "}"}
	seen := map[Var]bool{}
	for _, lit := range g.lits {
		for _, t := range lit.Pos.Args {
			if t.Var != "" && t.Var != "_" && !seen[t.Var] {
				seen[t.Var] = true
				a.Args = append(a.Args, Term{Var: t.Var})
			}
		}
	}
	if b.run.pushed == nil {
		b.run.pushed = map[string]*pushGroup{}
	}
	b.run.pushed[a.Relation] = g
	return a
}

// conjunction is g as the Source is asked it, its variables numbered in the order they're first
// written and, under bnd, a bound one read as its value.
func (g *pushGroup) conjunction(bnd *binding) ns.Conjunction {
	c, _ := g.numbered(bnd)
	return c
}

// numbered is conjunction, with the number it gave each variable.
func (g *pushGroup) numbered(bnd *binding) (ns.Conjunction, map[Var]int) {
	nums := map[Var]int{}
	arg := func(t Term) ns.PushArg {
		switch {
		case t.Const != nil:
			c := *t.Const
			return ns.PushArg{Const: &c}
		case t.Var == "" || t.Var == "_":
			return ns.PushArg{}
		}
		if bnd != nil {
			if v, ok := bnd.vals[t.Var]; ok {
				return ns.PushArg{Const: &v}
			}
		}
		n, ok := nums[t.Var]
		if !ok {
			n = len(nums) + 1
			nums[t.Var] = n
		}
		return ns.PushArg{Var: n}
	}
	var c ns.Conjunction
	for _, lit := range g.lits {
		pa := ns.PushAtom{Relation: lit.Pos.Relation, Args: make([]ns.PushArg, len(lit.Pos.Args))}
		for i, t := range lit.Pos.Args {
			pa.Args[i] = arg(t)
		}
		c.Atoms = append(c.Atoms, pa)
	}
	for _, cmp := range g.compare {
		c.Compare = append(c.Compare, ns.PushComparison{Op: cmp.Op, Left: arg(cmp.Left), Right: arg(cmp.Right)})
	}
	return c, nums
}

// boundIn reports whether bnd binds any of g's variables, which happens when a semi-naive round
// starts its body elsewhere (see deltaFirst).
func (g *pushGroup) boundIn(bnd *binding) bool {
	for _, lit := range g.lits {
		for _, t := range lit.Pos.Args {
			if t.Var != "" && t.Var != "_" {
				if _, ok := bnd.vals[t.Var]; ok {
					return true
				}
			}
		}
	}
	return false
}

// pushKey is a conjunction's key in the lookup caches: no lookup's key starts with pushPrefix, and the
// constants are keyed by valueKey, so a number and text never share an entry (#162).
func pushKey(c ns.Conjunction) string {
	var sb strings.Builder
	sb.WriteString(pushPrefix)
	arg := func(a ns.PushArg) {
		switch {
		case a.Const != nil:
			k := valueKey(*a.Const)
			sb.WriteString("c" + strconv.Itoa(len(k)) + ":" + k)
		case a.Var > 0:
			sb.WriteString("v" + strconv.Itoa(a.Var))
		default:
			sb.WriteByte('_')
		}
		sb.WriteByte(0)
	}
	for _, a := range c.Atoms {
		sb.WriteString(strconv.Itoa(len(a.Relation)) + ":" + a.Relation)
		for _, x := range a.Args {
			arg(x)
		}
		sb.WriteByte(';')
	}
	for _, cmp := range c.Compare {
		sb.WriteString(cmp.Op)
		arg(cmp.Left)
		arg(cmp.Right)
		sb.WriteByte(';')
	}
	if p := c.Page; p != nil {
		sb.WriteString("page" + strconv.Itoa(p.Limit))
		for _, v := range p.Columns {
			sb.WriteString("," + strconv.Itoa(v))
		}
		for _, k := range p.Order {
			sb.WriteString(";" + strconv.Itoa(k.Var) + strconv.FormatBool(k.Desc))
		}
	}
	return sb.String()
}

// extendPushed solves a pushed group's literal: it asks the Source for the group's rows, once per Eval
// and not at all when an earlier Eval's are kept, and unifies each row's tuples with the group's
// literals, yielding a binding per row that matches. A group some of whose variables are already
// bound is solved literal by literal instead, as it would have been without the Source.
func (b *Base) extendPushed(atom *Atom, bnd *binding, yield func(*binding) error) error {
	g := b.run.pushed[atom.Relation]
	var rows []ns.Tuple
	if g.boundIn(bnd) {
		// A binding prefetchPushed asked for is kept under the group with its inputs as constants;
		// any other is solved literal by literal.
		kept, hit, fromBase := b.lookedUp(pushKey(g.conjunction(bnd)))
		if !hit {
			return b.solveUnpushed(g, bnd, yield)
		}
		if b.run.explain != nil {
			b.explainPush(g, 0, !fromBase, fromBase)
		}
		rows = kept
	} else {
		var err error
		if rows, err = b.push(g); err != nil {
			return err
		}
	}
	n := len(g.lits)
	for k := 0; k+n <= len(rows); k += n {
		if err := b.countWork(); err != nil {
			return err
		}
		next, ok := bnd, true
		for i, lit := range g.lits {
			if next, ok = unify(lit.Pos.Args, rows[k+i], next); !ok {
				break
			}
		}
		if !ok {
			continue
		}
		if b.witnessing() {
			next.parts = make([]placed, n)
			for i, lit := range g.lits {
				t := rows[k+i]
				next.parts[i] = placed{at: lit.at, node: leaf(lit.Pos.Relation, t.Vals, t.Cites)}
			}
		}
		if err := yield(next); err != nil {
			return err
		}
	}
	return nil
}

// solveUnpushed solves g's literals in turn from bnd, counting their work against the pushed literal.
func (b *Base) solveUnpushed(g *pushGroup, bnd *binding, yield func(*binding) error) error {
	if e := b.run.explain; e != nil && !e.quiet {
		e.quiet = true
		defer func() { e.quiet = false }()
	}
	return solve(g.lits, 0, bnd, b, func(ext *binding) error {
		if b.witnessing() {
			ext.parts = []placed{} // solving the literals placed their witnesses already
		}
		return yield(ext)
	})
}

// push returns g's rows, each len(g.lits) tuples in a row, read as their arguments' types: from what
// this Eval or an earlier one kept, else from the Source, charging each row to the budget as it
// arrives and keeping a completed push as a lookup is kept.
func (b *Base) push(g *pushGroup) ([]ns.Tuple, error) { return b.pushAs(g, g.conjunction(nil)) }

// pushAs is push asking the Source for c, g's conjunction with a Page (#241).
func (b *Base) pushAs(g *pushGroup, c ns.Conjunction) ([]ns.Tuple, error) {
	key := pushKey(c)
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
	types := make([][]ns.ArgType, len(g.lits))
	for i, lit := range g.lits {
		types[i] = b.argTypesOf(lit.Pos.Relation)
	}
	ctx := r.context()
	var rows []ns.Tuple
	for row, err := range b.pusher.Push(ctx, c) {
		if err != nil {
			return nil, pushErr(ctx, g.text, err)
		}
		if len(row) != len(g.lits) {
			return nil, fmt.Errorf("query: pushing %s: a row of %d tuples for %d literals", g.text, len(row), len(g.lits))
		}
		if err := r.readOne(); err != nil {
			return nil, err
		}
		for i, t := range row {
			if want := len(g.lits[i].Pos.Args); len(t.Vals) != want {
				return nil, fmt.Errorf("query: pushing %s: a row's %s tuple has %d values, want %d", g.text, g.lits[i].Pos.Relation, len(t.Vals), want)
			}
			rows = append(rows, normalizeTuples([]ns.Tuple{t}, types[i])[0])
		}
	}
	b.keepLookup(key, rows)
	if r.explain != nil {
		b.explainPush(g, len(rows)/len(g.lits), false, false)
	}
	return rows, nil
}

// pushErr is a failed push's error, wrapped with the group's text.
func pushErr(ctx context.Context, text string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("query: evaluation stopped pushing %s: %w", text, ctx.Err())
	}
	return fmt.Errorf("query: pushing %s: %w", text, err)
}

// prefetchPushed asks the Source, for each pushed group in lits after the first literal, for the
// matches of every binding the literals before it give its variables that no Eval has kept, in
// batches of batchSize (#246), so solving the group under each binding finds its rows kept. A Volatile
// predicate stops it, as it stops prefetch.
func prefetchPushed(b *Base, lits []Literal) error {
	if b.pusher == nil {
		return nil
	}
	for i, lit := range lits {
		atom := lit.Pos
		if atom == nil {
			continue
		}
		if bi, ok := b.reg.Predicate(atom.Relation); ok {
			if bi.Volatile {
				return nil
			}
			continue
		}
		if i == 0 || !isPushed(atom.Relation) {
			continue
		}
		g := b.run.pushed[atom.Relation]
		if err := b.run.explain.at(i, func() error { return prefetchGroup(b, lits[:i], g) }); err != nil {
			return err
		}
	}
	return nil
}

// prefetchGroup solves prefix and pushes g for the bindings it gives g's variables, grouped by which
// of them are bound.
func prefetchGroup(b *Base, prefix []Literal, g *pushGroup) error {
	type batch struct {
		vars    []Var
		binding [][]ns.Value
	}
	pending := map[string]*batch{}
	var order []string
	asked := map[string]bool{}
	e := b.run.explain
	if e != nil {
		e.quiet = true
	}
	err := solve(prefix, 0, newBinding(), b, func(bnd *binding) error {
		var vars []Var
		var vals []ns.Value
		seen := map[Var]bool{}
		for _, lit := range g.lits {
			for _, t := range lit.Pos.Args {
				if v, ok := bnd.vals[t.Var]; ok && t.Var != "" && t.Var != "_" && !seen[t.Var] {
					seen[t.Var] = true
					vars = append(vars, t.Var)
					vals = append(vals, v)
				}
			}
		}
		if len(vars) == 0 {
			return nil
		}
		key := pushKey(g.conjunction(bnd))
		if _, done := b.run.looked[key]; done || asked[key] {
			return nil
		}
		if _, kept := b.lookups.get(key); kept {
			return nil
		}
		asked[key] = true
		k := strings.Join(varNames(vars), "\x00")
		if pending[k] == nil {
			pending[k] = &batch{vars: vars}
			order = append(order, k)
		}
		pending[k].binding = append(pending[k].binding, vals)
		return nil
	})
	if e != nil {
		e.quiet = false
	}
	if err != nil {
		return err
	}
	for _, k := range order {
		p := pending[k]
		for start := 0; start < len(p.binding); start += batchSize {
			if err := b.pushBatch(g, p.vars, p.binding[start:min(start+batchSize, len(p.binding))]); err != nil {
				return err
			}
		}
	}
	return nil
}

func varNames(vars []Var) []string {
	out := make([]string, len(vars))
	for i, v := range vars {
		out[i] = string(v)
	}
	return out
}

// pushBatch asks the Source for g's matches under each of bindings, values of vars, in one call
// (Conjunction.Bindings), and keeps each binding's rows as pushing g with that binding's values as
// constants would. A Source that declines the group with these inputs is left to solving, literal by
// literal.
func (b *Base) pushBatch(g *pushGroup, vars []Var, bindings [][]ns.Value) error {
	c, nums := g.numbered(nil)
	// vars come in the order g's literals first write them, which is the order numbered numbers them,
	// so Inputs is ascending and each binding is in Inputs' order as collected.
	for _, v := range vars {
		c.Inputs = append(c.Inputs, nums[v])
	}
	if !b.pusher.Pushes(c) {
		return nil
	}
	c.Bindings = bindings
	if err := b.countWork(); err != nil {
		return err
	}
	r := b.run
	types := make([][]ns.ArgType, len(g.lits))
	for i, lit := range g.lits {
		types[i] = b.argTypesOf(lit.Pos.Relation)
	}
	// Where to read each input in a row: the first literal and argument it is written at.
	type place struct{ lit, arg int }
	places := make([]place, len(vars))
	for i, v := range vars {
		places[i] = place{-1, -1}
		for li, lit := range g.lits {
			if a := slices.IndexFunc(lit.Pos.Args, func(t Term) bool { return t.Var == v }); a >= 0 {
				places[i] = place{li, a}
				break
			}
		}
	}
	ctx := r.context()
	byBinding := map[string][]ns.Tuple{}
	fetched := 0
	for row, err := range b.pusher.Push(ctx, c) {
		if err != nil {
			return pushErr(ctx, g.text, err)
		}
		if len(row) != len(g.lits) {
			return fmt.Errorf("query: pushing %s: a row of %d tuples for %d literals", g.text, len(row), len(g.lits))
		}
		if err := r.readOne(); err != nil {
			return err
		}
		read := make([]ns.Tuple, len(row))
		for i, t := range row {
			if want := len(g.lits[i].Pos.Args); len(t.Vals) != want {
				return fmt.Errorf("query: pushing %s: a row's %s tuple has %d values, want %d", g.text, g.lits[i].Pos.Relation, len(t.Vals), want)
			}
			read[i] = normalizeTuples([]ns.Tuple{t}, types[i])[0]
		}
		var sb strings.Builder
		for _, p := range places {
			sb.WriteString(valueKey(read[p.lit].Vals[p.arg]) + "\x00")
		}
		byBinding[sb.String()] = append(byBinding[sb.String()], read...)
		fetched++
	}
	for _, vals := range bindings {
		var sb strings.Builder
		bnd := newBinding()
		for i, v := range vars {
			sb.WriteString(valueKey(vals[i]) + "\x00")
			bnd.vals[v] = vals[i]
		}
		b.keepLookup(pushKey(g.conjunction(bnd)), byBinding[sb.String()])
	}
	if r.explain != nil {
		b.explainPush(g, fetched, false, false)
	}
	return nil
}
