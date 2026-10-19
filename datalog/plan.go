package datalog

import (
	"fmt"
	"slices"
	"strings"

	"github.com/panyam/jaala/ns"
)

// checkModes refuses a body in which some generator can never be called in a mode it accepts,
// whatever order its literals run in. It computes the variables the body can bind, starting from
// nothing and adding each atom's variables once that atom could run: a relation can always run, a
// generator once one of its modes is satisfied, and a filter or comparison binds nothing. A
// generator left over when nothing more can bind was never callable.
//
// The check does not depend on clause order, so every evaluator reports the same error for the same
// body, whether it runs the body as written (Naive) or plans it (SemiNaive), and Validate reports it
// without evaluating. where names the body for the message: a rule's head, or "the query".
func checkModes(b *Base, where string, body Body) error {
	return checkModesFrom(b, where, body, nil)
}

// checkModesFrom is checkModes for a body whose inputs (see inputsOf) its caller binds.
func checkModesFrom(b *Base, where string, body Body, inputs map[Var][]ns.Value) error {
	bound, done := modeBound(b, body, inputs)
	for i, lit := range body.Literals {
		a := lit.Pos
		if a == nil {
			a = lit.Neg // a negated generator runs over what the positive body bound
		} else if done[i] {
			continue
		}
		if a == nil {
			continue
		}
		if bi, ok := predicateIn(b.reg, a.Relation); ok && bi.IsGenerator() && !bi.Satisfied(boundFlags(a, bound)) {
			rel := a.Relation
			if isWalk(rel) {
				rel = walkName(rel)
			}
			return checkErr(KindUnbound, fmt.Sprintf("query: %s calls %s, and nothing binds what %s needs first: it needs %s",
				where, a, rel, modesText(bi, a))).call(rel, len(a.Args)).
				fix(fmt.Sprintf("add a relation to the body that binds what %s needs (%s)", shownName(a.Relation), modesText(bi, a)))
		}
	}
	return nil
}

// checkFilters refuses a filter some argument of which nothing in its body binds, with the error
// running it gives (#215). A rule whose head variable only a filter reads passed every check, and
// then Naive and the planned SemiNaive failed at different places. It runs after the rules are
// checked, so a goal calling a filter a rule redefines reports the redefinition.
func checkFilters(b *Base, body Body, inputs map[Var][]ns.Value) error {
	bound, _ := modeBound(b, body, inputs)
	for _, lit := range body.Literals {
		if a := lit.Pos; a != nil {
			if bi, ok := predicateIn(b.reg, a.Relation); ok && !bi.IsGenerator() && slices.Contains(boundFlags(a, bound), false) {
				return unboundFilter(a)
			}
		}
	}
	return nil
}

// modeBound is what a body can bind from inputs, whatever its order: each atom's variables once that
// atom could run. done marks the literals that could.
func modeBound(b *Base, body Body, inputs map[Var][]ns.Value) (map[Var]bool, []bool) {
	bound := map[Var]bool{}
	for v := range inputs {
		bound[v] = true
	}
	done := make([]bool, len(body.Literals))
	for changed := true; changed; {
		changed = false
		for i, lit := range body.Literals {
			if done[i] || lit.Pos == nil {
				continue
			}
			bi, isPred := predicateIn(b.reg, lit.Pos.Relation)
			switch {
			case isPred && !bi.IsGenerator():
				continue // a filter binds nothing
			case isPred && !bi.Satisfied(boundFlags(lit.Pos, bound)):
				continue // not callable yet; maybe once more is bound
			}
			done[i], changed = true, true
			bindAll(lit.Pos, bound)
		}
	}
	return bound, done
}

// boundFlags reports, per argument, whether it is a constant or a variable already bound.
func boundFlags(a *Atom, bound map[Var]bool) []bool {
	out := make([]bool, len(a.Args))
	for i, t := range a.Args {
		out[i] = t.Const != nil || (t.Var != "" && t.Var != "_" && bound[t.Var])
	}
	return out
}

func bindAll(a *Atom, bound map[Var]bool) {
	for _, t := range a.Args {
		if t.Var != "" && t.Var != "_" {
			bound[t.Var] = true
		}
	}
}

// modesText says what a generator's modes need, in the terms of one call: "?t bound, or ?f bound".
func modesText(bi ns.Builtin, a *Atom) string {
	var alts []string
	for _, m := range bi.Modes {
		var need []string
		for i, req := range m {
			if req && i < len(a.Args) {
				need = append(need, a.Args[i].String())
			}
		}
		alts = append(alts, strings.Join(need, " and ")+" bound")
	}
	return strings.Join(alts, ", or ")
}

// whereRule names a rule for a checkModes message.
func whereRule(r Rule) string {
	if isAggHelper(r.Head.Relation) {
		if strings.HasPrefix(r.Head.Relation, "optional"+aggSep) {
			return r.text // an optional's rules carry it as written (#282)
		}
		return "the aggregate " + r.text // a body aggregate's rule carries the literal as written
	}
	return fmt.Sprintf("rule %q", displayName(r.Head.Relation))
}

// plan reorders a query's rule bodies and goal so each literal runs once as much as possible is
// bound, which is SemiNaive's default (see SemiNaive.WrittenOrder). It changes no answer: a body is a
// conjunction, so its literals commute, and negated literals stay where solving already puts them,
// after the positive body.
//
// The query's columns are fixed first, from the goal as written, so reordering the goal does not
// reorder the answer.
func plan(b *Base, q Query) Query {
	out := q
	if len(out.Select) == 0 {
		out.Select = defaultSelect(q.Goal)
	}
	out.Rules = make([]Rule, len(q.Rules))
	for i, r := range q.Rules {
		r.Body = planRule(b, r.Body)
		out.Rules[i] = r
	}
	out.Goal = planBody(b, q.Goal, nil)
	return out
}

// planRule plans a rule body. A body the demand rewrite guarded keeps its guard first and is planned
// from what the guard binds: the guard holds only the demanded values, and ranked from nothing bound
// it would score as an unbound scan and fall behind a relation bound by constants, undoing the
// demand.
func planRule(b *Base, body Body) Body {
	lits := body.Literals
	if len(lits) == 0 || lits[0].Pos == nil || !isGuard(lits[0].Pos.Relation) {
		return planBody(b, body, nil)
	}
	entry := map[Var]bool{}
	bindAll(lits[0].Pos, entry)
	rest := planBody(b, Body{Literals: lits[1:]}, entry)
	return Body{Literals: append([]Literal{lits[0]}, rest.Literals...)}
}

// planGoal plans the goal for the demand rewrite. The relations set-bound variables range over (see
// bindSets) stay first, and the rest is planned from what they bind, as planRule plans a guarded body:
// ranked from nothing bound, they would fall behind a relation bound by constants, and a call would be
// demanded from that relation's values instead of the host's. Once the rewrite has seeded demand from
// them, plan orders the goal like any other, since the adorned relations hold only what was demanded.
func planGoal(b *Base, goal Body) Body {
	lits := goal.Literals
	n := 0
	for n < len(lits) && lits[n].Pos != nil && isBindSet(lits[n].Pos.Relation) {
		n++
	}
	if n == 0 {
		return planBody(b, goal, nil)
	}
	entry, ones := map[Var]bool{}, map[Var]bool{}
	for _, l := range lits[:n] {
		bindAll(l.Pos, entry)
		bindOne(l.Pos, ones)
	}
	rest := planBodyFrom(b, Body{Literals: lits[n:]}, entry, ones)
	return Body{Literals: append(append([]Literal(nil), lits[:n]...), rest.Literals...)}
}

// bindOne adds to ones the variable a relation of bindEqualities binds, which holds one value, so a
// call bound by it is seeded from a constant as one written in place is (see planPick).
func bindOne(a *Atom, ones map[Var]bool) {
	if a != nil && strings.HasPrefix(a.Relation, bindPrefix+"=") && len(a.Args) == 1 && a.Args[0].Var != "" {
		ones[a.Args[0].Var] = true
	}
}

// planBody orders a body greedily. At each step it takes the first check whose arguments are all
// bound, since checking early only discards: a comparison, a filter, or a relation whose arguments
// are all bound, which only asks whether a tuple exists. Failing that, the first generator one of
// whose Modes is satisfied with an argument bound (#36), where a variable only a mode requiring nothing
// would take doesn't count, since each call is then a full walk (generatorBound, #304): a generator call is the host's own work, such
// as a graph walk, and one placed after a relation runs once per row of it, where a relation bound
// only by constants is a scan that costs a cheap comparison per row. A call with nothing bound lists
// every value, so it ranks after a relation the engine derives or holds, which costs no more read
// whole (#293: Declaire's loc.home listed 136,181 homes ahead of http.pattern's 100), and still ahead
// of one the Source looks up, a whole read from the store. Failing that, the relation with the most bound
// arguments (a call the Source looks up on a pattern it has no index for counting as none bound,
// #154), since it fans out least and binds the most for what follows; on a tie with something
// bound, a derived call its constants seed (see planPick), then the base relation estimated to yield
// fewer tuples per call (see fanOut), else the earliest written. A literal nothing can make runnable keeps its written
// place at the end, where solving reports it as it always has. bound is what is bound on entry, such
// as a rule head's demanded arguments (see magic); it is not changed.
func planBody(b *Base, body Body, entry map[Var]bool) Body {
	return planBodyFrom(b, body, entry, nil)
}

// planBodyFrom is planBody with ones, the bound variables that hold one value (bindOne).
func planBodyFrom(b *Base, body Body, entry, ones map[Var]bool) Body {
	pos, negs := splitNegations(body.Literals)
	bound := map[Var]bool{}
	for v := range entry {
		bound[v] = true
	}
	one := map[Var]bool{}
	for v := range ones {
		one[v] = true
	}
	var out []Literal
	for len(pos) > 0 {
		pick := planPick(b, pos, pos, bound, one)
		if pick < 0 {
			// Every call left waits on another: r0(0 * ?0, 0) lowers to num.mul(0, ?0, ?e) and
			// r0(?e, 0, ?r), each binding what the other would solve for. As deferComparisons does, a
			// waiting call then waits only on what could run now (#374), or the written order decides,
			// and the demand rewrite stores a prefix that can't run.
			pick = planPick(b, pos, couldRun(b, pos, bound), bound, one)
		}
		if pick < 0 {
			out = append(out, pos...)
			break
		}
		lit := pos[pick]
		out = append(out, lit)
		if lit.Pos != nil {
			bindAll(lit.Pos, bound)
			bindOne(lit.Pos, one)
		}
		pos = append(pos[:pick:pick], pos[pick+1:]...)
	}
	return Body{Literals: append(out, negs...)}
}

// couldRun is the literals of pos that could run with bound: a relation, or a generator one of whose
// modes is satisfied. A call that can't is no binder for another to wait on.
func couldRun(b *Base, pos []Literal, bound map[Var]bool) []Literal {
	var out []Literal
	for _, lit := range pos {
		if lit.Pos == nil {
			continue
		}
		bi, ok := predicateIn(b.reg, lit.Pos.Relation)
		if !ok || bi.IsGenerator() && bi.Satisfied(boundFlags(lit.Pos, bound)) {
			out = append(out, lit)
		}
	}
	return out
}

// planPick is the literal of pos planBody takes next with bound, -1 for none, a computed call
// waiting while any of waitOn binds what it would solve for (awaitsOperand). Among relations with as
// many arguments bound, a derived call bound only by constants and one-value variables (seeded) goes
// ahead of one that would bind one of its variables, unless that one's bucket holds at most a tuple
// (#392): the demand rewrite
// then starts the call from its constants alone, a walk back from one value, where after a base
// relation bound by a constant, `node(?t, "Func"), reach(?t, "m1")`, it was demanded once per row of
// the base relation's whole bucket.
func planPick(b *Base, pos, waitOn []Literal, bound, ones map[Var]bool) int {
	pick := -1
	for i, lit := range pos {
		if (isCheck(b, lit) || isProbe(b, lit)) && checkReady(lit, bound) {
			pick = i
			break
		}
	}
	if pick < 0 {
		for i, lit := range pos {
			if lit.Pos == nil {
				continue
			}
			flags := boundFlags(lit.Pos, bound)
			if lit.computed && awaitsOperand(lit, waitOn, bound, b.reg) {
				continue
			}
			if bi, ok := predicateIn(b.reg, lit.Pos.Relation); ok && bi.IsGenerator() && bi.Satisfied(flags) && (generatorBound(bi, lit.Pos, flags) > 0 || !slices.ContainsFunc(pos, b.readsWhole) && !b.walksStore(lit.Pos)) {
				pick = i
				break
			}
		}
	}
	if pick < 0 {
		best, bestSize, bestGen, bestSeeded := -1, -1, false, false
		var bestAtom *Atom
		for i, lit := range pos {
			if isCheck(b, lit) || lit.computed && awaitsOperand(lit, waitOn, bound, b.reg) {
				continue
			}
			flags := boundFlags(lit.Pos, bound)
			bi, gen := predicateIn(b.reg, lit.Pos.Relation)
			if gen && !bi.Satisfied(flags) {
				continue
			}
			n := count(flags)
			if gen {
				n = generatorBound(bi, lit.Pos, flags)
			}
			if n > 0 && scansOnLookup(b, lit.Pos, flags) {
				n = 0
			}
			if n < best {
				continue
			}
			size := -1
			if n > 0 {
				size = fanOut(b, lit.Pos, flags)
			}
			seed := n > 0 && !gen && b.seeded(lit.Pos, flags, ones)
			tiny := func(size int) bool { return size >= 0 && size <= 1 }
			var take bool
			switch {
			case n > best:
				take = true
			case seed != bestSeeded && seed:
				take = !tiny(bestSize) && bindsFor(bestAtom, lit.Pos, bound)
			case seed != bestSeeded:
				take = tiny(size) || !bindsFor(lit.Pos, bestAtom, bound)
			default:
				take = size >= 0 && bestSize >= 0 && size < bestSize || n == best && bestGen && b.readsWhole(lit)
			}
			if take {
				pick, best, bestSize, bestGen, bestSeeded, bestAtom = i, n, size, gen, seed, lit.Pos
			}
		}
	}
	return pick
}

// bindsFor reports whether a would bind a variable of b that bound doesn't hold, so b runs once per
// value a binds when it follows a.
func bindsFor(a, b *Atom, bound map[Var]bool) bool {
	for _, t := range a.Args {
		if t.Var == "" || t.Var == "_" || bound[t.Var] {
			continue
		}
		for _, u := range b.Args {
			if u.Var == t.Var {
				return true
			}
		}
	}
	return false
}

// seeded reports whether a, with flags bound, calls a derived relation with every bound argument a
// constant or a variable of ones, which the demand rewrite seeds from those values alone.
func (b *Base) seeded(a *Atom, flags []bool, ones map[Var]bool) bool {
	if _, base := b.schemaOf(a.Relation); base {
		return false
	}
	for i, f := range flags {
		if f && a.Args[i].Const == nil && !ones[a.Args[i].Var] {
			return false
		}
	}
	return true
}

// readsWhole reports whether lit calls a relation the engine derives or holds, which a call with
// nothing bound reads at no more than its size: not a predicate, nor a base relation the Source looks
// up or the goal may stream, read whole from the store (see planBody, #293).
func (b *Base) readsWhole(lit Literal) bool {
	if lit.Pos == nil {
		return false
	}
	if _, ok := predicateIn(b.reg, lit.Pos.Relation); ok {
		return false
	}
	if _, base := b.schemaOf(lit.Pos.Relation); base {
		return b.edb != nil && !b.looksUp(lit.Pos.Relation) && (b.run == nil || !b.run.streamable[lit.Pos.Relation])
	}
	return true
}

// walksStore reports whether a is an engine walk whose steps the Source looks up: from no bound end
// it reads its step relation whole from the store, so it doesn't run first as a generator binding
// nothing does (#396).
func (b *Base) walksStore(a *Atom) bool {
	_, step, ok := walkOf(a.Relation)
	if !ok {
		return false
	}
	for _, r := range b.stepReads(step, Term{Var: closureNode}, true) {
		if b.looksUpBase(&r) {
			return true
		}
	}
	return false
}

// fanOut estimates how many tuples a base relation yields per call with the given arguments bound,
// or -1 when it can't tell: a derived relation, which isn't derived yet when a body is planned, a
// predicate, a Base that doesn't index, or a relation the Source looks up (ns.LookupSource). Bound only by constants, it is the exact bucket the call
// reads; bound by a variable too, the relation's size over the buckets at those positions, the
// average a call reads. It breaks ties between relations with as many arguments bound (#139): in
// tt(?r, ?a) :- part(?r, "capacitor"), pin(?r, ?a) with ?a bound, the constant would scan every
// capacitor for each ?a, where pin reads the few parts on one net.
func fanOut(b *Base, a *Atom, flags []bool) int {
	if b.edb == nil || b.noIndex {
		return -1
	}
	if _, ok := b.schemaOf(a.Relation); !ok || b.looksUp(a.Relation) {
		return -1 // a relation the Source looks up isn't read whole to estimate it
	}
	if b.run != nil && b.run.streamable[a.Relation] {
		return -1 // nor one the goal may stream (#264)
	}
	rows, err := b.edbTuples(a.Relation)
	if err != nil {
		if b.run != nil && b.run.readErr == nil {
			b.run.readErr = err
		}
		return -1
	}
	var mask patternMask
	var consts []ns.Value
	vars := false
	for i, f := range flags {
		if !f || i >= maskWidth {
			continue
		}
		mask |= 1 << uint(i)
		if c := a.Args[i].Const; c != nil {
			consts = append(consts, *c)
		} else {
			vars = true
		}
	}
	if mask == 0 || len(rows) < indexMinFacts {
		return len(rows)
	}
	idx := b.edb.get(a.Relation, rows, mask)
	if vars {
		return len(rows) / max(len(idx), 1)
	}
	return len(idx[tupleKey(consts)])
}

// scansOnLookup reports whether a call with flags bound goes to the Source's Lookup on a pattern the
// Source says it has no index for (ns.IndexedLookups, #154): the store scans the relation whatever is
// bound, so planBody ranks it as if nothing were.
func scansOnLookup(b *Base, a *Atom, flags []bool) bool {
	if b.indexed == nil || !b.looksUp(a.Relation) {
		return false
	}
	_, fact := factOf(a.Relation)
	a = lookedUpAtom(a)
	var bound []int
	for i, f := range flags {
		if f && i < len(a.Args) {
			bound = append(bound, i)
		}
	}
	if fact && len(bound) == 0 {
		return true // only its id bound, a fact relation's call reads its relation whole
	}
	return !b.indexed.Indexed(a.Relation, bound)
}

// isCheck reports whether a literal only tests: a comparison or a filter.
func isCheck(b *Base, lit Literal) bool {
	if lit.Compare != nil {
		return true
	}
	bi, ok := predicateIn(b.reg, lit.Pos.Relation)
	return ok && !bi.IsGenerator()
}

// isProbe reports whether a literal reads a relation rather than calling a predicate. Once all its
// arguments are bound (checkReady) it only asks whether a tuple exists, so it ranks with the checks.
func isProbe(b *Base, lit Literal) bool {
	if lit.Pos == nil {
		return false
	}
	_, isPred := predicateIn(b.reg, lit.Pos.Relation)
	return !isPred
}

// checkReady reports whether every variable a check reads is bound.
func checkReady(lit Literal, bound map[Var]bool) bool {
	var terms []Term
	if lit.Compare != nil {
		terms = []Term{lit.Compare.Left, lit.Compare.Right}
	} else {
		terms = lit.Pos.Args
	}
	for _, t := range terms {
		if t.Var != "" && !bound[t.Var] {
			return false
		}
	}
	return true
}

// generatorBound is how many of a generator call's arguments count as bound for planning. When only a
// mode requiring nothing is satisfied, that is its constants alone: the call is a full walk filtered
// on what is bound, run once for constants but once per row of whatever bound a variable (#304:
// agni's net.reaches(?n, ?rn, ?h) with ?rn bound walked 2,250 steps for each of 220 pins).
func generatorBound(bi ns.Builtin, a *Atom, flags []bool) int {
	if isWalk(a.Relation) {
		// its relation, bounds (and mode) are always given: only an end says where it walks from (#281)
		return count(flags[1:3])
	}
	for _, m := range bi.Modes {
		reads, ok := false, true
		for i, need := range m {
			if need && i < len(flags) { // a position past the call's arity is not required, as in Satisfied
				reads = true
				ok = ok && flags[i]
			}
		}
		if reads && ok {
			return count(flags)
		}
	}
	n := 0
	for _, t := range a.Args {
		if t.Const != nil {
			n++
		}
	}
	return n
}

func count(flags []bool) int {
	n := 0
	for _, f := range flags {
		if f {
			n++
		}
	}
	return n
}
