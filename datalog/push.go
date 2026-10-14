package datalog

import (
	"context"
	"fmt"
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
// A group is pushed only when constants are its only inputs: its first literal binds nothing an
// earlier literal bound, and each literal joining it shares a variable with it and none with a
// literal outside it before its place. A group bound partway through a body would be a call per
// binding, where a lookup batches (#153). Naive and WrittenOrder never push.

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
// Pushes). A group of at least two literals becomes one literal, at its first literal's place.
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

// growGroup is the group starting at pos[i], or nil when none of two literals or more does, marking
// its members used.
func growGroup(b *Base, pos []Literal, i int, bound map[Var]bool, used []bool) *pushGroup {
	if !b.pushable(pos[i]) || sharesVar(pos[i].Pos, bound) {
		return nil
	}
	members := []int{i}
	vars := map[Var]bool{}
	bindAll(pos[i].Pos, vars)
	outside := map[Var]bool{}
	for v := range bound {
		outside[v] = true
	}
	for j := i + 1; j < len(pos); j++ {
		lit := pos[j]
		if used[j] || lit.Pos == nil {
			continue
		}
		if b.pushable(lit) && !sharesVar(lit.Pos, outside) && sharesVar(lit.Pos, vars) {
			g := groupOf(pos, append(members[:len(members):len(members)], j), nil)
			if b.pusher.Pushes(g.conjunction(nil)) {
				members = append(members, j)
				bindAll(lit.Pos, vars)
				continue
			}
		}
		bindAll(lit.Pos, outside)
	}
	if len(members) < 2 {
		return nil
	}
	var cmps []Compare
	for _, lit := range pos {
		if c := lit.Compare; c != nil && pushOperand(c.Left, vars) && pushOperand(c.Right, vars) {
			cmps = append(cmps, *c)
		}
	}
	g := groupOf(pos, members, cmps)
	if len(cmps) > 0 && !b.pusher.Pushes(g.conjunction(nil)) {
		g.compare = nil
	}
	for _, m := range members {
		used[m] = true
	}
	return g
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
	if g.boundIn(bnd) {
		return b.solveUnpushed(g, bnd, yield)
	}
	rows, err := b.push(g)
	if err != nil {
		return err
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
