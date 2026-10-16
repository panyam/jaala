package datalog

import (
	"fmt"
	"github.com/panyam/jaala/ns"
	"sort"
	"strings"
)

// idbTuple is one derived fact of a rule-defined (IDB) relation: positional values plus the
// provenance of the base facts that derived it, so a derived answer stays as verifiable as a
// looked-up one.
type idbTuple struct {
	vals  []ns.Value
	cites []string
	wit   *Witness // its first derivation, when the Eval records witnesses
	// parts are a supplementary tuple's witnesses instead (see magic): the nodes of the literals it
	// stands for, at their written positions, which a body reading it takes as its own.
	parts []placed
	// vs are, under Across, the versions this copy of the tuple holds in (see addAcross); nil, every
	// version.
	vs ns.VersionSet
}

// materialize is Naive's fixpoint: it evaluates the query's user-defined rules into b.idb by stratified
// fixpoint. Rules
// define derived (IDB) relations; a rule derives its head for every binding of its body, and
// recursion (a rule whose body reads its own head, directly or transitively) runs to fixpoint —
// which terminates because the fact base is finite and no rule invents new values (no function
// symbols). Negation is stratified: the program is rejected up front if any relation depends on the
// negation of a relation in its own recursive cycle, which is exactly what makes a `not` safe (the
// negated relation is fully derived before the stratum that reads it runs).
//
// The pipeline is: index rules by head + validate (no head redefines a fact/built-in relation,
// consistent arity, range-restricted heads, known body relations) -> stratify (assign each relation
// a stratum; reject recursion through negation) -> per stratum, run a naive fixpoint until no new
// tuple appears.
//
// Worked example — a transitive closure over a derived edge:
//
//	link(?a,?b) :- component-on-net(?a,?n), component-on-net(?b,?n), ?a != ?b   // reads only EDB
//	conn(?a,?b) :- link(?a,?b)
//	conn(?a,?c) :- conn(?a,?b), link(?b,?c)                                     // recursive (positive)
//
// stratify puts both link and conn in stratum 0 (conn's self-recursion is positive, so it may share
// link's stratum). The fixpoint first derives every link pair, then re-runs the two conn rules until
// no new conn tuple appears — which is where a chain U1-N1-U2-N2-U3 closes up so conn(U1,U3) exists
// even though U1 and U3 share no net directly. See stratify for the strata assignment.
func (Naive) materialize(b *Base, rules []Rule) error {
	byHead, strata, err := b.strataOf(rules)
	if err != nil {
		return err
	}
	for _, stratum := range strata {
		rounds := 0
		defer func(stratum []string) {
			if b.run.explain != nil {
				for _, rel := range stratum {
					b.run.explain.rounds[rel] = rounds
				}
			}
		}(stratum)
		for { // naive fixpoint: re-derive every rule in the stratum until nothing new appears
			rounds++
			if err := b.run.done(); err != nil {
				return err
			}
			changed := false
			for _, rel := range stratum {
				for _, r := range byHead[rel] {
					added, err := b.applyRule(r)
					if err != nil {
						return err
					}
					changed = changed || added
				}
			}
			if !changed {
				break
			}
		}
	}
	return nil
}

// checkRules validates a rule set and returns what evaluating it needs: the rules grouped by head,
// and the strata to derive them in. It populates b.idbArity as it goes, since a rule's own head is
// what gives a derived relation its arity.
//
// It is separated from the evaluators' fixpoints because Validate runs it WITHOUT a design (agni issue 540). Every
// check here reads the rules and the relation vocabulary, never a row, so a broken rule set can be
// rejected where the rule is built rather than where it first runs.
func (b *Base) checkRules(rules []Rule) (map[string][]Rule, [][]string, error) {
	return b.checkRulesAs(rules, true)
}

// strataOf is checkRules for an evaluator's fixpoint, whose rules evaluate checked as linked, and may
// be the demand rewrite's. It leaves out checkComputedRecursion, which reads the program's own
// recursion: demand's cycles, a call's demand read from the call before it, make a rule that
// computes look recursive in a program that isn't (#266).
func (b *Base) strataOf(rules []Rule) (map[string][]Rule, [][]string, error) {
	return b.checkRulesAs(rules, false)
}

func (b *Base) checkRulesAs(rules []Rule, linked bool) (map[string][]Rule, [][]string, error) {
	byHead := map[string][]Rule{}
	for _, r := range rules {
		rel := r.Head.Relation
		if _, ok := b.schemaOf(rel); ok {
			return nil, nil, fmt.Errorf("query: rule head %q redefines a fact relation", rel)
		}
		if _, ok := b.reg.Predicate(rel); ok {
			return nil, nil, fmt.Errorf("query: rule head %q redefines a built-in relation", rel)
		}
		ar := len(r.Head.Args)
		if prev, ok := b.idbArity[rel]; ok && prev != ar {
			return nil, nil, fmt.Errorf("query: rule %q defined with %d and %d args (arity must be consistent)", rel, prev, ar)
		}
		b.idbArity[rel] = ar
		byHead[rel] = append(byHead[rel], r)
	}
	for _, r := range rules {
		if rs := byHead[r.Head.Relation]; len(rs) > 1 && r.aggregates() {
			return nil, nil, fmt.Errorf("query: rule %q aggregates, so it must be the relation's only rule (%d define it)", r.Head.Relation, len(rs))
		}
	}
	for _, r := range rules {
		if err := b.validateRule(r); err != nil {
			return nil, nil, err
		}
	}
	if linked {
		if err := checkComputedRecursion(rules); err != nil {
			return nil, nil, err
		}
	}
	strata, err := stratify(rules, b.idbArity)
	if err != nil {
		return nil, nil, err
	}
	return byHead, strata, nil
}

// validateRule checks a single rule's well-formedness independent of evaluation order: every body
// relation is known, and every head variable is bound by a positive body literal (range
// restriction, so a derived tuple never carries an unbound value). A variable that appears only
// under negation stays existential — the same lenient reading the goal uses — so it is not required
// in the head and is not checked here.
//
// Example:
//
//	bad(?x,?y) :- component-on-net(?x,?n)                 // rejected: ?y is in the head but no
//	                                                      //   positive body literal binds it
//	ok(?x)     :- component-on-net(?x,?n), not linked(?x) // accepted: ?x is bound positively; that
//	                                                      //   it also appears under `not` is irrelevant
func (b *Base) validateRule(r Rule) error {
	for _, lit := range r.Body.Literals {
		var rel string
		switch {
		case lit.Pos != nil:
			rel = lit.Pos.Relation
		case lit.Neg != nil:
			rel = lit.Neg.Relation
		default:
			continue // a comparison has no relation
		}
		// Only when there IS a vocabulary to be unknown in. An empty vocabulary cannot distinguish a
		// misspelled relation from one nobody installed, and answering "unknown" there would be a
		// statement about the query that the vocabulary has no standing to make (see Validate).
		if len(b.reg.BaseRelations()) > 0 && !b.knownRelation(rel) {
			if isAggHelper(r.Head.Relation) {
				return fmt.Errorf("query: %s reads %s", whereRule(r), b.reg.Unknown(rel))
			}
			return fmt.Errorf("query: rule %q reads %s", r.Head.Relation, b.reg.Unknown(rel))
		}
	}
	inputs := inputsBound(r, b.inputRels, b.reg) // a caller's arguments (#200), which every call has to bind
	if err := checkModesFrom(b, whereRule(r), r.Body, inputs); err != nil {
		return err
	}
	if err := checkNoAggregates(whereRule(r), r.Body); err != nil {
		return err
	}
	bound := map[Var]bool{}
	for _, vv := range positiveVars(r.Body) {
		bound[vv] = true
	}
	for v := range inputs {
		bound[v] = true
	}
	for _, arg := range r.Head.Args {
		if arg.Agg != nil {
			if err := validateAggOrVar(arg, bound); err != nil {
				return err
			}
			continue
		}
		if arg.Var == "_" {
			// A tuple needs a value in every place, so a rule with _ in its head derived nothing, while
			// the demand rewrite read the _ as matching anything (#89).
			return fmt.Errorf("query: rule %q has _ in its head, which gives that place no value; use a variable its body binds, or a constant", r.Head.Relation)
		}
		if arg.Var != "" && !bound[arg.Var] {
			return fmt.Errorf("query: rule %q head variable ?%s is not bound by a positive body relation", r.Head.Relation, arg.Var)
		}
	}
	if err := checkComparisonsFrom(r.Body, inputs); err != nil {
		return err
	}
	return checkFilters(b, r.Body, inputs)
}

// checkComparisons refuses a comparison over a variable no positive literal of its body binds. Such a
// comparison fails whenever a binding reaches it, so whether it failed used to depend on the data and
// on the evaluator: SemiNaive drops a rule the goal never reaches, and its rewrites copy a goal's
// comparisons into rules of their own (#89). Checked on every rule as linked and on the goal, as a
// projected variable is (validateSelect), it fails the same way for every evaluator and for Validate.
func checkComparisons(body Body) error {
	return checkComparisonsFrom(body, nil)
}

// checkComparisonsFrom is checkComparisons for a body whose inputs its caller binds.
func checkComparisonsFrom(body Body, inputs map[Var][]ns.Value) error {
	bound := map[Var]bool{}
	for v := range inputs {
		bound[v] = true
	}
	for _, v := range positiveVars(body) {
		bound[v] = true
	}
	for _, lit := range body.Literals {
		if c := lit.Compare; c != nil {
			for _, t := range []Term{c.Left, c.Right} {
				if t.Var != "" && !bound[t.Var] {
					return fmt.Errorf("query: comparison operand is unbound (a variable must appear in a relation before it is compared)")
				}
			}
		}
	}
	return nil
}

// aggregates reports whether the rule's head reduces its body's bindings, as degree(?n, count(?m))
// does (#4). Such a rule derives one tuple per group of its head's plain variables, once every
// relation its body reads is complete, so stratify places it above all of them.
func (r Rule) aggregates() bool {
	for _, t := range r.Head.Args {
		if t.Agg != nil {
			return true
		}
	}
	return false
}

// checkNoAggregates refuses an aggregate inside a body, which only a Query built in Go can hold: an
// aggregate reduces a group, and a body literal has none to reduce.
func checkNoAggregates(where string, body Body) error {
	for _, lit := range body.Literals {
		for _, t := range literalTerms(lit) {
			if t.Agg != nil {
				return fmt.Errorf("query: %s uses %s in a literal; an aggregate can only stand in a rule head or the answer", where, t)
			}
		}
	}
	return nil
}

// knownRelation reports whether a relation name resolves to something the evaluator can read: an EDB
// fact relation, a built-in (reaches or a string filter), or a rule-defined IDB relation.
func (b *Base) knownRelation(rel string) bool {
	if isPushed(rel) {
		return true
	}
	if _, ok := b.reg.Predicate(rel); ok {
		return true
	}
	if _, ok := b.schemaOf(rel); ok {
		return true
	}
	return b.isIDB(rel)
}

// applyRule solves one rule's body and adds a head tuple per solution. It reuses the goal's solver
// (positive backtracking join + post-solve negation filter), so a rule body has the full body
// expressiveness the goal has. Returns whether any new (deduplicated) tuple was added this pass.
func (b *Base) applyRule(r Rule) (bool, error) {
	if b.run != nil && b.run.explain != nil {
		explained := r // a copy, so the parameter doesn't escape on the path that doesn't explain
		defer b.run.explain.enter(&explained, Body{})()
	}
	pos, negs := splitNegations(r.Body.Literals)
	if err := b.checkNegatedRelations(negs); err != nil {
		return false, err
	}
	if r.aggregates() {
		return b.applyAggregate(r, pos, negs)
	}
	lits := deferComparisons(pos, b.reg)
	if err := prefetchWalks(b, lits, r.Head.Relation); err != nil {
		return false, err
	}
	if err := prefetch(b, lits, negs); err != nil {
		return false, err
	}
	added := false
	err := solve(lits, 0, newBinding(), b, func(bnd *binding) error {
		ok, err := passesNegations(bnd, negs, b)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		vals := make([]ns.Value, len(r.Head.Args))
		for j, arg := range r.Head.Args {
			val, ok := resolve(arg, bnd)
			if !ok {
				return nil // guarded by validateRule; belt-and-suspenders for a head var the body left unbound
			}
			vals[j] = val
		}
		t := idbTuple{vals: vals, cites: dedupStrings(bnd.cites)}
		if b.across != nil {
			t.vs = bnd.versions(b.across.vs)
		}
		if isAggDomain(r.Head.Relation) {
			t.cites = nil // what a body aggregate was asked about, not evidence (see isAggDomain)
		}
		if b.witnessing() && isSupplementary(r.Head.Relation) {
			t.parts = append([]placed(nil), bnd.wit...)
		} else if b.witnessing() && !isMagic(r.Head.Relation) && !isBindSet(r.Head.Relation) && !isAggDomain(r.Head.Relation) {
			text := r.text
			if text == "" {
				text = r.String()
			}
			t.wit = derivedNode(r.Head.Relation, vals, text, inWrittenOrder(append(bnd.wit[:len(bnd.wit):len(bnd.wit)], b.negationWitnesses(negs, bnd)...)))
			t.wit.Versions = t.vs
		}
		fresh, err := b.addTuple(r.Head.Relation, t)
		if err != nil {
			return err
		}
		if fresh && b.run.explain != nil && b.run.explain.body != nil {
			b.run.explain.body.Tuples++
		}
		added = added || fresh
		return nil
	})
	return added, err
}

// applyAggregate derives an aggregating rule's tuples: it solves the body as applyRule does, then
// groups and reduces the bindings as a goal's projection does (see aggregate), the head's plain
// variables being the group key. A group's citations are the union of its bindings', and its witness
// names the rule without children, as an aggregate answer row has none.
func (b *Base) applyAggregate(r Rule, pos, negs []Literal) (bool, error) {
	var cols []Term
	seen := map[Var]bool{}
	for _, t := range r.Head.Args {
		if lbl := colLabel(t); (t.Agg != nil || (t.Var != "" && t.Var != "_")) && !seen[lbl] {
			seen[lbl] = true
			cols = append(cols, t)
		}
	}
	ag := newAggregator(cols, nil, false)
	if b.across != nil {
		ag.across = b.across.vs
	}
	lits := deferComparisons(pos, b.reg)
	if err := prefetch(b, lits, negs); err != nil {
		return false, err
	}
	err := solve(lits, 0, newBinding(), b, func(bnd *binding) error {
		ok, err := passesNegations(bnd, negs, b)
		if ok {
			ag.add(bnd)
		}
		return err
	})
	if err != nil {
		return false, err
	}
	rows, err := ag.rows()
	if err != nil {
		return false, err
	}
	added := false
	for _, row := range rows {
		vals := make([]ns.Value, len(r.Head.Args))
		for j, t := range r.Head.Args {
			if t.Const != nil {
				vals[j] = *t.Const
			} else {
				vals[j] = row.Bind[colLabel(t)]
			}
		}
		t := idbTuple{vals: vals, cites: row.Cites, vs: row.Versions}
		if b.witnessing() && !isAggReduce(r.Head.Relation) {
			text := r.text
			if text == "" {
				text = r.String()
			}
			t.wit = derivedNode(r.Head.Relation, vals, text, nil)
			t.wit.Versions = t.vs
		}
		fresh, err := b.addTuple(r.Head.Relation, t)
		if err != nil {
			return false, err
		}
		if fresh && b.run.explain != nil && b.run.explain.body != nil {
			b.run.explain.body.Tuples++
		}
		added = added || fresh
	}
	return added, nil
}

// addTuple appends a derived tuple to its relation unless an equal-valued one is already present
// (set semantics — datalog facts have value identity). The first derivation's cites are kept, so
// provenance is deterministic under the fixpoint's fixed rule and tuple order, unless the Eval keeps
// canonical derivations (see keepCanonical), when it reports a replaced derivation as a change too.
//
// The membership test is a bucket lookup rather than a scan of the relation. It used to
// be linear, so deriving n tuples cost O(n^2) before any join work: a transitive closure over a
// 4,000-component design spent 28 seconds here. valsEqual still decides within the bucket, so set
// semantics and the first-wins provenance rule are unchanged — only the number of comparisons is.
func (b *Base) addTuple(rel string, t idbTuple) (bool, error) {
	if b.across != nil {
		return b.addAcross(rel, t)
	}
	tuples := b.idb[rel]
	x := b.idbIndexFor(rel, fullMask(len(t.vals)))
	x.sync(tuples, fullMask(len(t.vals)))
	for _, i := range x.buckets[tupleKey(t.vals)] {
		if err := b.countWork(); err != nil {
			return false, err
		}
		if valsEqual(tuples[i].vals, t.vals) {
			best := firstAnnotations(tuples[i].vals, t.vals)
			replaced := b.canonical() && (t.wit != nil || t.parts != nil) && b.keepCanonical(rel, i, t)
			// Whichever derivation is kept, the tuple holds the better annotation of the two.
			annotated := !sameAnnotations(b.idb[rel][i].vals, best)
			if annotated {
				b.annotate(rel, i, best, !replaced)
			}
			return replaced || annotated, nil
		}
	}
	b.idb[rel] = append(tuples, t)
	return true, nil
}

// firstAnnotations is two equal tuples' values, each with the annotation spelledBefore puts first (a
// unit, a spelling). Dedup is by value, so without this a tuple kept whichever derivation arrived
// first, and CanonicalCites, keeping another, answered 9 A where plain evaluation answered 9 (#201).
func firstAnnotations(a, b []ns.Value) []ns.Value {
	out := a
	for j := range a {
		if spelledBefore(b[j], a[j]) {
			if &out[0] == &a[0] {
				out = append([]ns.Value(nil), a...)
			}
			out[j] = b[j]
		}
	}
	return out
}

// sameAnnotations reports whether two equal tuples' values are also annotated alike.
func sameAnnotations(a, b []ns.Value) bool {
	for j := range a {
		if a[j].BaseUnit != b[j].BaseUnit || a[j].S != b[j].S {
			return false
		}
	}
	return true
}

// annotate stores tuple i of rel with vals, and its witness node with them, and lists it as revised
// so what was derived from its old annotation is derived again (as keepCanonical does), unless the
// caller already listed it.
func (b *Base) annotate(rel string, i int, vals []ns.Value, list bool) {
	t := b.idb[rel][i]
	t.vals = vals
	if t.wit != nil {
		w := *t.wit
		w.Values = vals
		t.wit = &w
	}
	b.idb[rel][i] = t
	if list {
		if b.run.revised == nil {
			b.run.revised = map[string][]int{}
		}
		b.run.revised[rel] = append(b.run.revised[rel], i)
	}
}

// idbIndexFor returns this query's index of a derived relation at one binding pattern, creating it
// on first use. Per-query because a derived relation only exists for one query; see Base.idbIdx.
func (b *Base) idbIndexFor(rel string, mask patternMask) *idbIndex {
	if b.idbIdx == nil {
		b.idbIdx = map[idxKey]*idbIndex{}
	}
	k := idxKey{rel: rel, mask: mask}
	x, ok := b.idbIdx[k]
	if !ok {
		x = &idbIndex{}
		b.idbIdx[k] = x
	}
	return x
}

func valsEqual(a, b []ns.Value) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !valueEq(a[i], b[i]) {
			return false
		}
	}
	return true
}

// stratify assigns each IDB relation a stratum such that a relation's stratum is >= any relation it
// reads positively and strictly > any it reads under negation or reduces in an aggregating rule, then
// returns the relation names grouped by stratum in ascending order. It rejects a program that reads a
// relation under negation, or aggregates over it, from inside that relation's own recursive cycle —
// the standard condition that keeps `not` and an aggregate well-defined.
//
// Algorithm: iterative relaxation over the predicate-dependency edges (a longest-path / Bellman-Ford
// shape), NOT strongly-connected-components. Each round raises a relation's stratum to satisfy its
// edges — `>=` for a positive edge, strictly `>` for a negative one. A positive cycle is harmless: it
// only equalizes (max, no increment), so there is no need to condense SCCs first. Only a negative
// edge increments, so a negative edge inside a cycle would force the stratum to climb without bound;
// that is caught by failing to converge within |relations| rounds, and the non-convergence IS the
// recursion-through-negation signal. Deterministic and linear in the rules.
//
// Example:
//
//	linked(?a)   :- component-on-net(?a,?n), ...              // reads only EDB      -> stratum 0
//	isolated(?r) :- component-on-net(?r,?n), not linked(?r)   // negates linked      -> stratum 1
//
// isolated's negative edge to linked forces stratum(isolated) > stratum(linked), so linked is fully
// derived before isolated is computed — the guarantee that makes the `not` sound. Contrast the
// unstratifiable `p :- ..., not q` with `q :- ..., not p`: each negative edge forces the other
// strictly higher every round, relaxation never settles, and the program is rejected.
func stratify(rules []Rule, arity map[string]int) ([][]string, error) {
	var edges []depEdge
	for _, r := range rules {
		head, agg := r.Head.Relation, r.aggregates()
		for _, lit := range r.Body.Literals {
			atom, neg := lit.Pos, false
			if lit.Neg != nil {
				atom, neg = lit.Neg, true
			}
			if atom == nil {
				continue // comparison
			}
			if _, isIDB := arity[atom.Relation]; isIDB {
				edges = append(edges, depEdge{from: head, to: atom.Relation, neg: neg, agg: agg})
			}
		}
	}
	stratum := map[string]int{}
	for rel := range arity {
		stratum[rel] = 0
	}
	n := len(arity)
	for round := 0; round <= n; round++ {
		changed := false
		for _, e := range edges {
			want := stratum[e.to]
			if e.neg || e.agg {
				want++
			}
			if stratum[e.from] < want {
				stratum[e.from] = want
				changed = true
			}
		}
		if !changed {
			break
		}
		if round == n {
			// A body aggregate's zero clause negates its reduction, which climbs as the aggregate does,
			// so a cycle through one is reported as through the aggregate.
			if cycle := strictCycle(edges, func(e depEdge) bool { return e.neg && !isAggReduce(e.to) }); len(cycle) > 0 {
				return nil, fmt.Errorf("query: rules are not stratifiable (recursion through negation: %s)", strings.Join(cycle, ", "))
			}
			return nil, fmt.Errorf("query: rules are not stratifiable (recursion through an aggregate: %s)", strings.Join(strictCycle(edges, func(e depEdge) bool { return e.agg }), ", "))
		}
	}
	byStratum := map[int][]string{}
	maxS := 0
	for rel, s := range stratum {
		byStratum[s] = append(byStratum[s], rel)
		if s > maxS {
			maxS = s
		}
	}
	var out [][]string
	for s := 0; s <= maxS; s++ {
		if group := byStratum[s]; len(group) > 0 {
			sort.Strings(group) // deterministic order within a stratum
			out = append(out, group)
		}
	}
	return out, nil
}

// depEdge is one rule dependency: from's rule reads to, under negation when neg, and to reduce it when
// agg (from's rule aggregates).
type depEdge struct {
	from, to string
	neg, agg bool
}

// strictCycle names the relations whose rules read, through an edge strict says must climb (negation
// or an aggregate), a relation that depends back on them: the relations a not-stratifiable error is
// about. Private names are shown as written.
func strictCycle(edges []depEdge, strict func(depEdge) bool) []string {
	adj := map[string][]string{}
	for _, e := range edges {
		adj[e.from] = append(adj[e.from], e.to)
	}
	reaches := func(from, to string) bool {
		seen := map[string]bool{}
		stack := []string{from}
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if n == to {
				return true
			}
			if seen[n] {
				continue
			}
			seen[n] = true
			stack = append(stack, adj[n]...)
		}
		return false
	}
	var out []string
	for _, e := range edges {
		if strict(e) && reaches(e.to, e.from) {
			out = append(out, e.from, e.to)
		}
	}
	return displayNames(out)
}
