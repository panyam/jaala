package datalog

import (
	"cmp"
	"context"
	"fmt"
	"github.com/panyam/jaala/ns"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
)

// Base is the queryable fact base: a Vocabulary's names, with one Source's relations indexed by binding
// pattern on first use. Built once per dataset; many queries reuse it, including concurrently.
//
// Concurrency. Any number of goroutines may Eval on one Base at once, with any evaluators, mixed:
// Naive and SemiNaive in any mode share a Base safely. Each Eval derives its rules into its own copy,
// so derived relations and their indexes never cross queries. What is shared is read-mostly and
// guarded: each base relation is read from the Source once, however many Evals ask for it at the same
// moment (the others wait for that read), and each of its indexes is built once. Work, Source,
// Vocabulary and Unindexed are safe at any time; Work totals the work of every Eval on the Base, while
// each Eval's context and Budget are its own. The vocabulary's resolved modules are shared through its memo.
//
// Not safe: registering into the Vocabulary while Evals run on a Base over it.
//
// Host code is called without any jaala lock held. A generator may run in several Evals at once and
// must be safe for that, or serialize itself, as a host whose engine has one connection would.
type Base struct {
	src ns.Source
	reg *ns.Vocabulary
	// edb caches each base relation's tuples the first time a query reads it, and the indexes over
	// them. It belongs to the SHARED Base because a Source's tuples are immutable for the Base's
	// life, so a second query over the same data reuses what the first one built.
	//
	// A POINTER, so Eval's shallow copy shares the one cache rather than copying a mutex (which vet
	// rejects, correctly: two copies of a lock guard nothing).
	edb *edbCache
	// derived keeps the derived relations SemiNaive evaluated in full, for later queries over the
	// same Base (see derivedCache). Shared, and behind a pointer, for the reasons edb is.
	derived *derivedCache
	// lookups keeps what the Source's Lookup returned, for later queries over the same Base (see
	// lookupCache). Shared, and behind a pointer, for the reasons edb is.
	lookups *lookupCache
	// idb holds the derived (IDB) relations materialized from a query's user-defined rules, and
	// idbArity their positional arity (from the rule heads). Both are nil on the shared Base and
	// populated on a per-query shallow copy, so rules never leak between queries that reuse a Base.
	idb      map[string][]idbTuple
	idbArity map[string]int
	// idbIdx is the derived-relation analogue of the edb indexes and is per-QUERY, living on the
	// shallow copy beside idb: a derived relation only exists for one query, so a cached index of it
	// must not outlive that query.
	idbIdx map[idxKey]*idbIndex
	// work counts candidate COMPARISONS: every fact or tuple the solver actually examines. It is
	// what goes quadratic when an index is missing, and counting it rather than timing it gives a
	// scaling guard a deterministic signal — a duration assertion would be a flake generator across
	// machines, and would say nothing about complexity. Behind a pointer so Eval's shallow copy
	// accumulates into the same counter.
	work *int64
	// noIndex sends every base-relation probe down the full scan. See Unindexed.
	noIndex bool
	// looker is the Source when it answers bound lookups itself (ns.LookupSource), else nil.
	looker ns.LookupSource
	// lookRels, when the Source implements it, says which of its relations it looks up
	// (ns.LookupRelations); nil means all of them.
	lookRels ns.LookupRelations
	// indexed, when the Source implements it, says which lookup patterns it answers from an index
	// (ns.IndexedLookups); nil means all of them.
	indexed ns.IndexedLookups
	// pusher is the Source when it evaluates conjunctions itself (ns.PushSource, #237), else nil.
	pusher ns.PushSource
	// walker is the Source when it follows a relation from value to value itself (ns.WalkSource, #239).
	walker ns.WalkSource
	// stepWalker is the Source when it follows a step of several literals itself
	// (ns.ConjunctionWalkSource, #250).
	stepWalker ns.ConjunctionWalkSource
	// summarizer is the Source when it reduces a conjunction's matches itself (ns.SummarySource, #240).
	summarizer ns.SummarySource
	// run is one Eval's own state (its context and budget), set on that Eval's copy of the Base and
	// nil on a Base no Eval is running on.
	run *evalRun
	// sigs, when set, are the derived members' signatures to check constants against. Only a
	// validation base inside module resolution sets it, since the memoized signatures are not
	// available until that resolution finishes.
	sigs map[string][]ns.ArgSig
	// inputRels, set only inside module resolution, are the members that take an argument from their
	// caller (#200), whose own rules are checked with those arguments bound. Elsewhere lowerInputs
	// has inlined them before any check reads the program.
	inputRels map[string]Rule
}

// NewBase builds a fact base that answers with v's names over src's facts. A host composes and
// checks its vocabulary once, and builds a Base per dataset it reads: every Base over v shares v's
// resolved modules and signatures, and each reads tuples, and hands generators, only its own Source.
//
// The vocabulary stays the authority on what each relation is. src must serve every base relation v
// holds, at the same arity, and NewBase refuses it otherwise, naming each relation that is missing or
// disagrees: a relation silently reading as empty would answer as if the dataset had none of it.
// Relations src serves beyond v's are ignored, since no query can name them.
//
// v is read, never changed, by any number of Bases at once; registering into it while they evaluate
// is not supported. A nil v is a base that knows no names at all.
func NewBase(v *ns.Vocabulary, src ns.Source) (*Base, error) {
	var missing, mismatched []string
	for _, rel := range v.BaseRelations() {
		want, _ := v.Schema(rel)
		var got ns.Schema
		ok := false
		if src != nil {
			got, ok = src.Schema(rel)
		}
		switch {
		case !ok:
			missing = append(missing, rel)
		case got.Arity != want.Arity:
			mismatched = append(mismatched, fmt.Sprintf("%s takes %d args in the vocabulary and %d in the source", rel, want.Arity, got.Arity))
		}
	}
	var problems []string
	if len(missing) > 0 {
		sort.Strings(missing)
		problems = append(problems, "the source does not serve "+strings.Join(missing, ", "))
	}
	problems = append(problems, mismatched...)
	if len(problems) > 0 {
		return nil, fmt.Errorf("query: %s", strings.Join(problems, "; "))
	}
	looker, _ := src.(ns.LookupSource)
	lookRels, _ := src.(ns.LookupRelations)
	indexed, _ := src.(ns.IndexedLookups)
	pusher, _ := src.(ns.PushSource)
	walker, _ := src.(ns.WalkSource)
	stepWalker, _ := src.(ns.ConjunctionWalkSource)
	summarizer, _ := src.(ns.SummarySource)
	return &Base{src: src, reg: v, looker: looker, lookRels: lookRels, indexed: indexed, pusher: pusher, walker: walker, stepWalker: stepWalker, summarizer: summarizer, edb: newEDBCache(), derived: newDerivedCache(), lookups: newLookupCache(), work: new(int64)}, nil
}

// MustBase is NewBase for a Source known to match its vocabulary, such as a test fixture. It panics
// on the error NewBase would return.
func MustBase(v *ns.Vocabulary, src ns.Source) *Base {
	b, err := NewBase(v, src)
	if err != nil {
		panic(err)
	}
	return b
}

// Unindexed returns a Base over the same vocabulary and Source that scans every base relation
// instead of consulting an index. It answers exactly as the indexed Base does, only slower, which is
// what makes it useful: comparing the two is how a host asserts that indexing changed no answer on
// its own data, rather than hoping so.
func (b *Base) Unindexed() *Base {
	nb := *b
	nb.noIndex = true
	return &nb
}

// Source returns the Source this base reads.
func (b *Base) Source() ns.Source { return b.src }

// Vocabulary returns the vocabulary this base resolves names in.
func (b *Base) Vocabulary() *ns.Vocabulary { return b.reg }

// Work reports how many candidate comparisons the solver has performed against this Base. It exists
// for scaling guards: assert the RATIO of work at n and 2n rather than any absolute number, so the
// test measures complexity instead of the machine it runs on.
func (b *Base) Work() int64 {
	if b.work == nil {
		return 0
	}
	return atomic.LoadInt64(b.work)
}

// edbTuples returns a base relation's tuples, reading them from the Source once per Base.
func (b *Base) edbTuples(rel string) ([]ns.Tuple, error) {
	if b.edb == nil {
		t, err := readTuples(b.run, b.src, rel)
		return normalizeTuples(t, b.argTypesOf(rel)), err
	}
	return b.edb.tuples(b.run, rel, b.src, b.argTypesOf(rel))
}

// readTuples reads one relation from the Source for run, through TuplesSeq when the Source streams,
// charging each tuple to run's budget (#233), else through TuplesContext when it can be cancelled,
// wrapping a failure with the relation's name.
func readTuples(run *evalRun, src ns.Source, rel string) ([]ns.Tuple, error) {
	if src == nil {
		return nil, nil
	}
	ctx := run.context()
	failed := func(err error) error {
		if ctx.Err() != nil {
			return fmt.Errorf("query: evaluation stopped reading %s: %w", rel, ctx.Err())
		}
		return fmt.Errorf("query: reading %s: %w", rel, err)
	}
	if ss, ok := src.(ns.StreamSource); ok {
		var out []ns.Tuple
		for t, err := range ss.TuplesSeq(ctx, rel) {
			if err != nil {
				return nil, failed(err)
			}
			if err := run.readOne(); err != nil {
				return nil, err
			}
			out = append(out, t)
		}
		return out, nil
	}
	cs, ok := src.(ns.ContextSource)
	if !ok {
		return src.Tuples(rel), nil
	}
	t, err := cs.TuplesContext(ctx, rel)
	if err != nil {
		return nil, failed(err)
	}
	return t, nil
}

// Evaluator answers a Query over a Base, each answer Row carrying the provenance of the facts that
// derived it. The IR is declarative, so strategies swap behind this interface: Naive is the
// reference, SemiNaive the one a host should run.
//
// ctx bounds the evaluation: when it is cancelled or its deadline passes, Eval stops within a short
// stretch of work and returns an error wrapping ctx.Err(), and the context reaches the host's
// generators and ContextSource reads, so a walk or a read in progress can stop too. Options bind goal
// variables (Bind) and limit work (Budget).
type Evaluator interface {
	Eval(ctx context.Context, q Query, b *Base, opts ...Option) ([]Row, error)
	// EvalTable answers as Eval does, as one Table rather than a map per row (#195).
	EvalTable(ctx context.Context, q Query, b *Base, opts ...Option) (*Table, error)
}

// Naive is the reference interpreter: a backtracking join in written order, with rules derived by a
// naive fixpoint that re-runs every rule over everything derived so far until a round adds nothing.
// It serves the full fragment (conjunction, comparison, stratified negation, aggregation, recursive
// rules, host predicates) through one positive primitive, extendAtom, which negation reuses
// (atomHolds), so a single dispatch covers every kind of relation.
//
// It is kept deliberately simple and is not the fast path. Its job is to be obviously right, so the
// optimized evaluators can be tested against it: every optimization must answer exactly as Naive
// does. A host should use SemiNaive.
type Naive struct{}

// Eval answers the query. It links the query first (see Link), derives its rules, then solves the
// positive body (atoms + comparisons) by backtracking, filters each binding through the negated
// literals (stratified negation), then projects: a plain select-project, or a group-and-reduce when
// the projection contains an aggregate. Results are deduplicated and sorted, so a query is a
// deterministic, regenerable view; each row carries the provenance of the facts that produced it.
func (n Naive) Eval(ctx context.Context, q Query, b *Base, opts ...Option) ([]Row, error) {
	return rowsOf(n.EvalTable(ctx, q, b, opts...))
}

// EvalTable answers q as Eval does, as a Table.
func (n Naive) EvalTable(ctx context.Context, q Query, b *Base, opts ...Option) (*Table, error) {
	return evaluate(ctx, q, b, opts, nil, n.materialize)
}

// evaluate answers q over b, deriving its rules with the evaluator's fixpoint. Everything else is
// shared: Base holds the derived relations and the primitives that read and extend them (checkRules,
// applyRule, solve), and an evaluator decides only how to iterate them to a fixpoint and, through
// rewrite (nil for none), how to rewrite the linked query first.
func evaluate(ctx context.Context, q Query, b *Base, opts []Option, rewrite func(*Base, Query) Query, fixpoint func(*Base, []Rule) error) (*Table, error) {
	var o evalOptions
	for _, opt := range opts {
		opt(&o)
	}
	// The query runs on its own copy of the Base: its context, its budget and its derived relations
	// are its own, while the Source's tuples and their indexes stay shared (see Base).
	gen := b.refresh()
	nb := *b
	nb.run = &evalRun{ctx: ctx, budget: o.budget, witness: o.witness || o.canonical, canonical: o.canonical, lookGen: b.lookups.generation()}
	if o.explain == nil {
		return evaluateOn(&nb, gen, q, o, rewrite, fixpoint)
	}
	nb.run.explain = newExplainer(o.explain, &nb)
	tab, err := evaluateOn(&nb, gen, q, o, rewrite, fixpoint)
	nb.run.explain.finish(&nb, tab.Len(), err, o.budget)
	if err == nil && !o.cold && nb.run.explain.warm(&nb) {
		o.explain.Cold = coldCost(ctx, q, b, opts, rewrite, fixpoint)
	}
	return tab, err
}

// evaluateOn answers q on b, an Eval's own copy of the Base (see evaluate).
func evaluateOn(b *Base, gen int64, q Query, o evalOptions, rewrite func(*Base, Query) Query, fixpoint func(*Base, []Rule) error) (*Table, error) {
	if err := b.run.done(); err != nil {
		return nil, err
	}
	q, err := lowerBodyAggregates(q, "")
	if err != nil {
		return nil, err
	}
	if q, err = lowerExpressions(q, b.reg); err != nil {
		return nil, err
	}
	written := q
	if o.after != nil {
		if written.After, err = cursorOf(written, o.after); err != nil {
			return nil, err
		}
	}
	q, cols, err := bindGoal(q, o.bind)
	if err != nil {
		return nil, err
	}
	// The answer's columns are fixed here, before any rewrite reorders or inlines the goal; a variable
	// the host bound to one value is dropped from them while evaluating and filled back into every row
	// after.
	sel := cols
	if len(o.bind) > 0 {
		sel = q.Select
	}
	q, err = Link(q, b.reg)
	if err != nil {
		return nil, err
	}
	// A rule taking an argument from its caller (#200) is checked as written with that argument bound,
	// then inlined into each call before the evaluator reads the rules.
	b.inputRels = inputRelations(q.Rules, b.reg)
	if q, err = coerceConstants(q, b.reg); err != nil {
		return nil, err
	}
	if err := checkComparisonUnits(q, b.reg); err != nil {
		return nil, err
	}
	if b.witnessing() {
		q = tagWritten(q)
	}
	if err := checkWrittenAnchors(written.Goal, q.Rules, o.bind, b.inputRels, b.reg); err != nil {
		return nil, err
	}
	if err := b.checkWrittenArity(q); err != nil {
		return nil, err
	}
	if q, err = b.bindSets(q, o.bind); err != nil {
		return nil, err
	}
	// Modes are checked on the program as linked, before any rewrite, so an evaluator that inlines a
	// rule away still refuses what its body could never run, with the message Validate gives.
	for _, r := range q.Rules {
		if err := checkModesFrom(b, whereRule(r), r.Body, inputsBound(r, b.inputRels, b.reg)); err != nil {
			return nil, err
		}
	}
	if err := checkModes(b, "the query", q.Goal); err != nil {
		return nil, err
	}
	if err := checkNoAggregates("the query", q.Goal); err != nil {
		return nil, err
	}
	if err := checkComparisons(q.Goal); err != nil {
		return nil, err
	}
	if e := b.run.explain; e != nil {
		// A rewrite keeps a rule's text, so each body it runs can name the rule it came from.
		for i, r := range q.Rules {
			if r.text == "" {
				q.Rules[i].text = r.String()
			}
		}
		e.linked = q
	}
	if len(b.inputRels) > 0 {
		b.idbArity = map[string]int{}
		if _, _, err := b.checkRules(q.Rules); err != nil {
			return nil, err
		}
		if q, err = lowerInputs(q, o.bind, b.reg); err != nil {
			return nil, err
		}
	}
	if rewrite != nil {
		// The rules are checked as linked too, before the rewrite renames them, so an error names
		// the program's relations rather than an adorned or factored one.
		b.idbArity = map[string]int{}
		if _, _, err := b.checkRules(q.Rules); err != nil {
			return nil, err
		}
		q = rewrite(b, q)
		if b.run != nil && b.run.readErr != nil {
			return nil, b.run.readErr
		}
	}
	if len(q.Rules) > 0 || len(b.run.preload) > 0 {
		b.idb = map[string][]idbTuple{}
		b.idbArity = map[string]int{}
		// Fresh alongside idb, and for the same reason: an index of derived tuples describes THIS
		// query's derivations and must not be reachable from the next one. Copying the struct
		// carried the map header across, so leaving this out would have one query probing an index
		// whose positions point into another query's idb slice.
		b.idbIdx = map[idxKey]*idbIndex{}
		installHeld(b)
		if e := b.run.explain; e != nil {
			e.held = map[string]bool{}
			for rel := range b.run.preload {
				e.held[rel] = true
			}
		}
		if err := fixpoint(b, q.Rules); err != nil {
			return nil, err
		}
		keepDerived(b, gen, q.Rules)
	}
	pos, negs := splitNegations(q.Goal.Literals)
	if err := b.checkNegatedRelations(negs); err != nil {
		return nil, err
	}
	if err := validateSelect(sel, q.Having, q.Goal); err != nil {
		return nil, err
	}
	if err := validateOrder(cols, written); err != nil {
		return nil, err
	}

	if e := b.run.explain; e != nil {
		defer e.enter(nil, q.Goal)()
	}
	return answerGoal(b, pos, negs, q.Having, sel, cols, written, o)
}

// splitNegations separates the body into the positive part (atoms + comparisons, solved by
// backtracking) and the negated literals (applied as a post-solve filter). Splitting makes
// negation safe regardless of clause order: a negated literal's variables are bound by the time it
// is checked.
func splitNegations(lits []Literal) (pos, negs []Literal) {
	for _, lit := range lits {
		if lit.Neg != nil {
			negs = append(negs, lit)
		} else {
			pos = append(pos, lit)
		}
	}
	return pos, negs
}

// validateNegations rejects a negated literal over an unknown relation or with the wrong arity —
// caught here so a bad `not` fails clearly instead of silently never matching — or anchored to no
// variable of the body (see checkNegationAnchored). Negation ranges over every callable relation
// uniformly: EDB, IDB, string filters, overlay predicates, AND reaches (negation as failure —
// atomHolds runs the same extendAtom the positive solve uses). Stratification (each evaluator's
// fixpoint) already guarantees a negated IDB relation is fully derived before the rule or goal that
// negates it runs, so the filter is safe.
func (b *Base) validateNegations(body Body, negs []Literal) error {
	return b.validateNegationsFrom(body, negs, nil)
}

// validateNegationsFrom is validateNegations for a body whose inputs its caller binds, which anchor
// a negation as the host's bound variables anchor the goal's.
func (b *Base) validateNegationsFrom(body Body, negs []Literal, inputs map[Var][]ns.Value) error {
	if err := b.checkNegatedRelations(negs); err != nil {
		return err
	}
	return checkAnchored(body, inputs)
}

// checkNegatedRelations is validateNegations without the anchoring check, for a body a rewrite may
// have changed: anchoring is a property of the program as written (see checkWrittenAnchors).
func (b *Base) checkNegatedRelations(negs []Literal) error {
	for _, lit := range negs {
		rel := lit.Neg.Relation
		ok, known := b.arityAccepts(rel, len(lit.Neg.Args))
		if !known {
			return fmt.Errorf("query: negation over %s", b.reg.Unknown(rel))
		}
		if !ok {
			return fmt.Errorf("query: negated relation %q takes %s args, got %d", rel, b.arityLabelOf(rel), len(lit.Neg.Args))
		}
	}
	return nil
}

// checkAnchored applies checkNegationAnchored to each negated literal of body, counting as bound the
// variables its positive literals bind and those in hostBound.
func checkAnchored(body Body, hostBound map[Var][]ns.Value) error {
	bound := map[Var]bool{}
	for _, v := range positiveVars(body) {
		bound[v] = true
	}
	for v := range hostBound {
		bound[v] = true
	}
	_, negs := splitNegations(body.Literals)
	for _, lit := range negs {
		if err := checkNegationAnchored(lit.Neg, bound); err != nil {
			return err
		}
	}
	return nil
}

// checkWrittenArity checks every call's argument count once, on the linked program before any
// rewrite, so a wrong count is the same error from every evaluator and names the relation as written
// (#127). The demand rewrite reads a call's arguments by its relation's arity, so a call it was handed
// with too few panicked it (#133). It also refuses a goal atom naming nothing: solving checks an atom
// only when it reaches it, so an unknown name after an atom that matched nothing went unreported, and
// a rewrite reported it under its own relation's name. A relation whose rules disagree on its arity is
// left to checkRules, which also reports the unknown names in rule bodies.
func (b *Base) checkWrittenArity(q Query) error {
	derived := map[string]int{}
	mixed := map[string]bool{}
	for _, r := range q.Rules {
		if n, ok := derived[r.Head.Relation]; ok && n != len(r.Head.Args) {
			mixed[r.Head.Relation] = true
		}
		derived[r.Head.Relation] = len(r.Head.Args)
	}
	check := func(lits []Literal, goal bool) error {
		for _, l := range lits {
			a, neg := l.Pos, false
			if l.Neg != nil {
				a, neg = l.Neg, true
			}
			if a == nil || mixed[a.Relation] {
				continue
			}
			want, label := -1, ""
			if n, ok := derived[a.Relation]; ok {
				want, label = n, fmt.Sprint(n)
			} else if ok, known := b.arityAccepts(a.Relation, len(a.Args)); !known {
				switch {
				case !goal:
					continue
				case neg:
					return fmt.Errorf("query: negation over %s", b.reg.Unknown(a.Relation))
				default:
					return fmt.Errorf("query: %s", b.reg.Unknown(a.Relation))
				}
			} else if ok {
				continue
			} else {
				label = b.arityLabelOf(a.Relation)
			}
			if want >= 0 && want == len(a.Args) {
				continue
			}
			switch {
			case neg:
				return fmt.Errorf("query: negated relation %q takes %s args, got %d", a.Relation, label, len(a.Args))
			case want < 0:
				if _, isPred := b.reg.Predicate(a.Relation); isPred {
					return fmt.Errorf("query: %s takes %s args, got %d", a.Relation, label, len(a.Args))
				}
			}
			return fmt.Errorf("query: relation %q takes %s args, got %d", a.Relation, label, len(a.Args))
		}
		return nil
	}
	for _, r := range q.Rules {
		if err := check(r.Body.Literals, false); err != nil {
			return err
		}
	}
	return check(q.Goal.Literals, true)
}

// checkWrittenAnchors checks every negation's anchor once, on the program as the author wrote it: the
// goal before Bind turns the host's variables into constants (a host-bound variable anchors, as one
// written in the goal does), and the rules before a rewrite inlines or renames them (#92). Checked
// after, a bound anchor became a constant and an inlined body's anchor a renamed variable, so a
// negation anchored as written was refused, naming a variable nobody wrote.
func checkWrittenAnchors(goal Body, rules []Rule, bind map[Var][]ns.Value, inputRels map[string]Rule, reg *ns.Vocabulary) error {
	for _, r := range rules {
		if err := checkAnchored(r.Body, inputsBound(r, inputRels, reg)); err != nil {
			return err
		}
	}
	return checkAnchored(goal, bind)
}

// checkNegationAnchored rejects a negated atom that shares NO variable with the positive body, which
// is the unsafe-negation case (agni issue 522).
//
// Such a literal has nothing to range over per row, so it collapses to a design-wide constant:
// `entity(?n,"net"), not component.class(?tp,"test_point")` asks whether the design contains no test
// point at all, and on any board that has one it silently filters every row away. The author meant
// "nets with no test point", which needs a negated CONJUNCTION and is a separate gap. Returning zero
// rows for a question that was never asked is the worst available answer, because an empty result
// from a negation reads as a reassuring fact about the design.
//
// The rule is ANCHORING, not full safety, and the difference matters. Classic datalog safety demands
// every variable in a negated literal occur positively, which would reject the shape this language
// documents and people correctly rely on: `component-on-net(?r,?n), not component.mpn(?r,?m) => ?r`
// leaves ?m free ON PURPOSE, and means "no m exists for this r". That is well defined precisely
// because ?r anchors it. What is never meaningful is a negation anchored to nothing.
//
// A negated atom with no variables at all is ground and needs no anchor: `not component.class("U1",
// "test_point")` is a constant filter, and a legitimate one.
func checkNegationAnchored(a *Atom, bound map[Var]bool) error {
	var vars []Var
	for _, t := range a.Args {
		// "_" is the wildcard: it never binds and never anchors, so it does not count either way.
		if t.Var != "" && t.Var != "_" {
			vars = append(vars, t.Var)
		}
	}
	if len(vars) == 0 {
		return nil
	}
	for _, v := range vars {
		if bound[v] {
			return nil
		}
	}
	return fmt.Errorf("query: negated relation %q shares no variable with the rest of the query (?%s appears only inside the `not`, so the negation has nothing to range over and matches either every row or none)",
		a.Relation, vars[0])
}

// isIDB reports whether a relation is a rule-defined (IDB) relation in this query's materialized set.
func (b *Base) isIDB(rel string) bool {
	_, ok := b.idbArity[rel]
	return ok
}

// passesNegations keeps a binding only if every negated literal holds: `not R(args)` holds when R has
// NO solution under the current binding (negation as failure — atomHolds runs the same extendAtom the
// positive solve uses). A negated arg that is a constant or an already-bound variable must match; a
// variable appearing only under negation is a wildcard (existential — "no solution for any value").
//
// Example — `component.mpn(?r,?m), not param(?m,"VIN",?v)` (parts with no VIN datasheet param): the
// positive solve binds ?m; here, for that ?m, ?v is bound by nothing, so it is a wildcard. The
// binding is kept iff NO param fact has subject == the bound ?m and symbol == "VIN" — i.e. "?m has
// no VIN param at any value". A part whose ?m does have such a fact is dropped.
func passesNegations(bnd *binding, negs []Literal, b *Base) (bool, error) {
	if b.run != nil && b.run.explain != nil && b.run.explain.body != nil {
		return passesNegationsExplained(bnd, negs, b, b.run.explain)
	}
	for _, lit := range negs {
		matched, err := b.atomHolds(lit.Neg, bnd)
		if err != nil {
			return false, err
		}
		if matched {
			return false, nil // some solution exists, so the `not` is violated
		}
	}
	return true, nil
}

// passesNegationsExplained is passesNegations counting each negated literal's work and what it let
// through (see Explain). They are the body's last literals.
func passesNegationsExplained(bnd *binding, negs []Literal, b *Base, e *explainer) (bool, error) {
	prev := e.cur
	defer func() { e.cur = prev }()
	for k, lit := range negs {
		lr, _ := e.literal(len(e.body.Literals) - len(negs) + k)
		matched, err := b.atomHolds(lit.Neg, bnd)
		if err != nil {
			return false, err
		}
		if matched {
			return false, nil
		}
		if lr != nil {
			lr.Passed++
		}
	}
	return true, nil
}

// binding is a partial solution: variable bindings plus the cites of the facts consumed so far.
type binding struct {
	vals  map[Var]ns.Value
	cites []string
	// For a witnessed Eval: the witness of each literal solved so far, and of the one just extended
	// by (set by extendAtom, placed by solve, which knows the literal's written position).
	wit   []placed
	last  *Witness
	parts []placed // set instead of last by a supplementary tuple (see idbTuple.parts)
}

func newBinding() *binding { return &binding{vals: map[Var]ns.Value{}} }

func (b *binding) clone() *binding {
	nv := make(map[Var]ns.Value, len(b.vals))
	for k, v := range b.vals {
		nv[k] = v
	}
	return &binding{vals: nv, cites: append([]string(nil), b.cites...), wit: append([]placed(nil), b.wit...)}
}

// solve recurses over the goal literals: a positive atom fans out through extendAtom (the one
// primitive for every relation kind), a comparison prunes. emit is called for every complete
// binding; the first error it (or an atom) returns stops the search.
//
// Worked example — `component.mpn(?r,?m), param(?m,"VIN",?v), ?v < 30`:
//
//	i=0 component.mpn(?r,?m): for each component.mpn fact, bind ?r,?m -> recurse i=1
//	i=1 param(?m,"VIN",?v):   for each param fact whose subject == the bound ?m and symbol == "VIN",
//	                          bind ?v -> recurse i=2
//	i=2 ?v < 30:              keep the binding iff ?v < 30, else prune
//	i=3 (past the end):       emit the binding (its accumulated cites travel with it)
//
// deferComparisons moves each comparison to just after the first literal that leaves its operands
// bound, so a body's comparisons hold wherever they are written: `?a = "a", edge(?a, ?b)` compares
// once edge has bound ?a, in every evaluator, where solving it as written failed on an unbound ?a under
// Naive and answered under the planned SemiNaive (#89). A body already in a solvable order, as every
// planned one is, comes back unchanged. A comparison nothing binds stays at the end and fails there,
// as it did; validateRule refuses one in a rule before any evaluator runs it. A comparison adds no
// citations and its witness position travels with it, so moving one changes no answer.
//
// A call an expression was written as also waits for every relation that binds a variable it reads
// (awaitsBinder), so it reads the spelling all of them agreed on. That can move one in a planned body,
// which the planner ranks by modes alone.
func deferComparisons(lits []Literal, reg *ns.Vocabulary) []Literal {
	bound := map[Var]bool{}
	var rest []Literal
	ready := func(l Literal) bool {
		if l.computed && l.Pos != nil {
			// A call an expression was written as waits for one of its function's modes (#5).
			bi, ok := reg.Predicate(l.Pos.Relation)
			return !ok || !bi.IsGenerator() || bi.Satisfied(boundFlags(l.Pos, bound)) && !awaitsBinder(l, rest, bound, reg)
		}
		c := l.Compare
		if c == nil {
			return true
		}
		for _, t := range []Term{c.Left, c.Right} {
			if t.Var != "" && !bound[t.Var] {
				return false
			}
		}
		return true
	}
	out := make([]Literal, 0, len(lits))
	var waiting []Literal
	for i, lit := range lits {
		rest = lits[i+1:]
		if !ready(lit) {
			waiting = append(waiting, lit)
			continue
		}
		out = append(out, lit)
		if lit.Pos == nil {
			continue
		}
		bindAll(lit.Pos, bound)
		for progress := true; progress; {
			progress = false
			still := waiting[:0]
			for _, w := range waiting {
				if ready(w) {
					out = append(out, w)
					if w.Pos != nil {
						bindAll(w.Pos, bound)
						progress = true
					}
				} else {
					still = append(still, w)
				}
			}
			waiting = still
		}
	}
	return append(out, waiting...)
}

func solve(lits []Literal, i int, bnd *binding, b *Base, emit func(*binding) error) error {
	if b.run == nil || b.run.explain == nil || b.run.explain.quiet {
		return solveAt(lits, i, bnd, b, emit, nil)
	}
	// Explaining: the work from here is literal i's, and what a solution costs to keep (its
	// negations, storing a derived tuple) is the body's. Kept out of solveAt, whose closure would
	// otherwise capture the report and move it to the heap on every call.
	e := b.run.explain
	prev := e.cur
	defer func() { e.cur = prev }()
	if i == len(lits) {
		e.cur = nil
		return emit(bnd)
	}
	lr, _ := e.literal(i)
	if lits[i].Compare != nil {
		e.access("comparison")
	}
	return solveAt(lits, i, bnd, b, emit, lr)
}

// solveAt solves literal i on, counting in lr, when an Eval explains itself, the bindings it passes.
func solveAt(lits []Literal, i int, bnd *binding, b *Base, emit func(*binding) error, lr *LiteralReport) error {
	if i == len(lits) {
		return emit(bnd)
	}
	lit := lits[i]
	switch {
	case lit.Compare != nil:
		ok, err := evalCompare(*lit.Compare, bnd)
		if err != nil {
			return err
		}
		if ok {
			if lr != nil {
				lr.Passed++
			}
			return solve(lits, i+1, bnd, b, emit)
		}
		return nil
	case lit.Neg != nil:
		// Unreachable: Eval splits negated literals out before solving (they are a post-solve
		// filter). A Neg here would be an internal error, not a user one.
		return fmt.Errorf("query: internal: negated literal reached the positive solver")
	case lit.Pos != nil:
		return b.extendAtom(lit.Pos, bnd, func(ext *binding) error {
			if b.witnessing() {
				if ext.parts != nil {
					ext.wit = append(ext.wit[:len(ext.wit):len(ext.wit)], ext.parts...)
				} else {
					ext.wit = append(ext.wit[:len(ext.wit):len(ext.wit)], placed{at: lit.at, node: ext.last})
				}
				ext.last, ext.parts = nil, nil
			}
			if lr != nil {
				lr.Passed++ // the next literal's solve puts lr back as current when it returns
			}
			return solve(lits, i+1, ext, b, emit)
		})
	default:
		return fmt.Errorf("query: empty literal")
	}
}

// unify matches an atom's args against one fact, extending bnd: a constant must equal the field, a
// variable binds it (or must equal its existing binding). Returns the extended binding, or false.
//
// This is logic-programming pattern matching (as in Prolog/datalog), not function application: it
// is symmetric — an argument matches whether the value comes from the fact or from an existing
// binding — and it commits variables into the binding as a side result, closer to destructuring a
// value against a pattern than to calling a function with arguments.
func unify(args []Term, t ns.Tuple, bnd *binding) (*binding, bool) {
	out := bnd.clone()
	for j, arg := range args {
		if !bindArg(out, arg, t.Vals[j]) {
			return nil, false
		}
	}
	out.cites = append(out.cites, t.Cites...)
	return out, true
}

// bindArg unifies one argument term with a value: a constant must equal it, a variable binds it (or
// must match its existing binding). "_" and the empty variable are wildcards.
func bindArg(bnd *binding, arg Term, val ns.Value) bool {
	switch {
	case arg.Const != nil:
		return valueEq(val, *arg.Const)
	case arg.Var == "" || arg.Var == "_":
		return true
	default:
		if bound, ok := bnd.vals[arg.Var]; ok {
			if !valueEq(val, bound) {
				return false
			}
			if val.Num != nil && bound.Num != nil && (val.S != bound.S || val.BaseUnit != bound.BaseUnit) && spelledBefore(val, bound) {
				bnd.vals[arg.Var] = val
			}
			return true
		}
		bnd.vals[arg.Var] = val
		return true
	}
}

// awaitsBinder reports whether a call an expression was written as reads a bound variable that a
// relation among rest also binds. Arithmetic over 1 A, where the data says 1 V, answers in amps, so an
// expression runs only once every relation binding what it reads has met it and bindArg has settled
// the spelling, whatever order the body runs in (#227). Only relations count: a generator may itself
// wait for the expression's result.
func awaitsBinder(lit Literal, rest []Literal, bound map[Var]bool, reg *ns.Vocabulary) bool {
	for _, t := range lit.Pos.Args {
		if t.Var == "" || t.Var == "_" || !bound[t.Var] {
			continue
		}
		for _, o := range rest {
			if o.Pos == nil || o.computed {
				continue
			}
			if _, ok := reg.Predicate(o.Pos.Relation); ok {
				continue
			}
			for _, a := range o.Pos.Args {
				if a.Var == t.Var {
					return true
				}
			}
		}
	}
	return false
}

// A number can reach a variable spelled several ways: a query's 01 or 3.3, a fact's 1 or 3.3V. Which
// one the variable keeps must not depend on the order the evaluator met them in (#148), so when two
// spellings of one number meet, the variable keeps the one spelledBefore ranks first, and an answer
// then writes a plain number canonically (canonicalAnswers). Text that says more than the number, a
// unit or a range, ranks first, so it reaches the answer as the data spelled it.

// spelledBefore reports whether a ranks before b among spellings of one number: text that says more
// than the number before text that only spells it, then a unit before none, then shorter text, then
// the smaller text.
func spelledBefore(a, b ns.Value) bool {
	if pa, pb := spellsNumber(a), spellsNumber(b); pa != pb {
		return pb
	}
	if (a.BaseUnit != "") != (b.BaseUnit != "") {
		return a.BaseUnit != ""
	}
	if a.BaseUnit != b.BaseUnit {
		return a.BaseUnit < b.BaseUnit // 5 A before 5 V, so two units of one number are decided too (#201)
	}
	if len(a.S) != len(b.S) {
		return len(a.S) < len(b.S)
	}
	return a.S < b.S
}

// spellsNumber reports whether v's text is only a spelling of its number, such as 01 or 1.50 for 1.5,
// rather than text saying more, such as 3.3V.
func spellsNumber(v ns.Value) bool {
	if v.Num == nil {
		return false
	}
	f, err := strconv.ParseFloat(v.S, 64)
	return err == nil && f == *v.Num
}

// canonicalNumber is v with a plain number's text written as ftoa writes it, so 01 and 1.0 answer as 1.
// Any other value is returned as it is.
func canonicalNumber(v ns.Value) ns.Value {
	if !spellsNumber(v) {
		return v
	}
	n := *v.Num
	if n == 0 {
		n = 0 // -0 is 0
	}
	var buf [32]byte
	if c := strconv.AppendFloat(buf[:0], n, 'g', -1, 64); string(c) != v.S {
		v.S = string(c) // allocates only when the text changes, so a generator's rows mostly don't
	}
	return v
}

// canonicalAnswers writes each plain number the goal's solutions hold canonically, in their bindings
// and their witnesses, before they are projected, grouped, deduplicated and ordered: an answer is the
// same text whichever spelling of a number its evaluator met (#148). A host-bound column is filled in
// after, so it keeps the value as bound.
func canonicalAnswers(raw []*binding) {
	for _, bnd := range raw {
		for v, val := range bnd.vals {
			if c := canonicalNumber(val); c.S != val.S {
				bnd.vals[v] = c
			}
		}
		for i, p := range bnd.wit {
			bnd.wit[i].node = canonicalWitness(p.node)
		}
	}
}

// canonicalWitness is w with every plain number in it written canonically, copying only the nodes that
// change, since a node can be shared by other derivations.
func canonicalWitness(w *Witness) *Witness {
	if w == nil {
		return nil
	}
	var vals []ns.Value
	for i, v := range w.Values {
		if c := canonicalNumber(v); c.S != v.S {
			if vals == nil {
				vals = append([]ns.Value(nil), w.Values...)
			}
			vals[i] = c
		}
	}
	var kids []*Witness
	for i, k := range w.Children {
		if c := canonicalWitness(k); c != k {
			if kids == nil {
				kids = append([]*Witness(nil), w.Children...)
			}
			kids[i] = c
		}
	}
	if vals == nil && kids == nil {
		return w
	}
	out := *w
	if vals != nil {
		out.Values = vals
	}
	if kids != nil {
		out.Children = kids
	}
	return &out
}

// resolve reads a term's value under the binding: a constant is itself, a bound variable its
// binding. ok is false for an unbound variable.
func resolve(t Term, bnd *binding) (ns.Value, bool) {
	if t.Const != nil {
		return *t.Const, true
	}
	val, ok := bnd.vals[t.Var]
	return val, ok
}

// valueEq reports whether two values are the same value: two numbers when their numbers are equal,
// two texts when their text is, and a number never equals text (#162). It is an equivalence: each
// value has one key (valueKey) and two values are equal exactly when their keys are, so which of two
// equal values a derivation or a join meets first can't change what else equals it. The old rule
// compared a number with text by the number's own spelling, which made "0" equal 0 and 0 equal 00 but
// not "0" equal 00, and an answer depend on rule and join order.
//
// A query's constant is read as its argument's type before evaluation (coerceConstants), and a
// relation's tuples as their arguments' types when the Base reads them (normalizeTuples), so in a
// typed argument a number and text never meet. In an untyped one, quotes and the host's ns.S or ns.N
// decide.
//
// ABSENT UNIFIES ONLY WITH ABSENT, which is what stops it colliding with the empty string. Two
// unstated bounds ARE the same answer to "what does this row state", so this is true rather than
// SQL's UNKNOWN. That is a deliberate deviation: full three-valued logic would have to thread UNKNOWN
// through negation, aggregation and the index, and "both unstated" is the reading an engineer running
// a search actually wants.
func valueEq(a, b ns.Value) bool {
	if a.Absent || b.Absent {
		return a.Absent && b.Absent
	}
	switch {
	case a.Num != nil && b.Num != nil:
		return *a.Num == *b.Num || (*a.Num != *a.Num && *b.Num != *b.Num) // NaN is itself
	case a.Num != nil || b.Num != nil:
		return false
	}
	return a.S == b.S
}

// valueKey is the one key a value has under valueEq: absentKey for an absent value, numberPrefix and a
// number's canonical text, and text's own. The prefix starts with a NUL, which no text from a Source
// holds, so a number never shares a key with text.
func valueKey(v ns.Value) string {
	switch {
	case v.Absent:
		return absentKey
	case v.Num != nil:
		return numberPrefix + numberKey(*v.Num)
	}
	return v.S
}

// numberPrefix starts a number's key (see valueKey).
const numberPrefix = "\x00n"

// numberKey is n written canonically, with -0 as 0 (ftoa writes it "-0", #89).
func numberKey(n float64) string {
	if n == 0 {
		n = 0
	}
	return ftoa(n)
}

// orderingOps are the comparisons that ask which of two values is LARGER. Equality and inequality are
// deliberately not here: asking whether two values are the same is meaningful across kinds (a number
// and a word are simply not equal), while asking which is larger is not.
var orderingOps = map[string]bool{"<": true, "<=": true, ">": true, ">=": true}

// evalCompare evaluates a comparison once both operands are bound: numeric when both carry a number,
// string otherwise.
//
// THREE REFUSALS, and they are the point of this function rather than details of it. Each answers a
// question that HAS no answer, and each previously answered it anyway.
//
// 1. AN ABSENT OPERAND IS NOT COMPARABLE. A datasheet row stating only a maximum leaves its minimum
// absent. Before Value.Absent existed such a field bound to the EMPTY STRING, and ordering fell
// through to cmpStr and answered by LEXICOGRAPHY:
//
//	"" <= "5.0"  -> true      an absent lower bound passes a lower-bound test
//	"" >= "3.0"  -> false     the same absent bound, opposite phrasing, opposite answer
//	"5.0" <= ""  -> false     an absent upper bound fails an upper-bound test
//	"" <= "-2"   -> true      an absent bound "passes" against MINUS TWO, since "" precedes everything
//
// The answer depended on how the author phrased the inequality and on the sign of the constant. This
// now refuses on the FLAG rather than on a nil Num, which matters because a non-numeric string also
// has a nil Num: inferring absence from that coincidence conflated two different things.
//
// 2. A NUMBER AND A NON-NUMBER HAVE NO ORDER. `?name < 5` is a question about nothing, and cmpStr
// used to answer it by comparing "ALPHA" against "5".
//
// 3. UNLIKE DIMENSIONS HAVE NO ORDER. Volts are not smaller or larger than amps. Both sides must
// carry a base unit for this to fire, because a bare literal cannot state one: `?vmax < 5.0` has to
// keep working, so an empty BaseUnit is polymorphic rather than a dimension of its own. SCALE is not
// this layer's problem and never reaches it (the host normalizes it), so this compares "V" against "A"
// and never "mV" against "V".
//
// ALL THREE EVALUATE TO NO MATCH RATHER THAN AN ERROR. An error aborts the whole query, so one
// max-only row among many would make a legitimate range query unusable. No-match leaves that row
// unjudged, which is this engine's posture everywhere else: silence means "I could not tell", never
// "this is fine". An author who wants an absent lower bound to count as unbounded-below writes that
// clause explicitly instead of inheriting it from string ordering.
//
// EQUALITY IS DELIBERATELY NOT DIMENSION-CHECKED, and ordering two non-numbers is untouched
// (`?name < "M"` still splits alphabetically). Equality here is the author's explicit operator, but
// the same values also unify implicitly when a variable repeats across atoms, and unification is
// identity rather than physics. Making one unit-aware and not the other would be incoherent, and
// making both would break joins and the fact index, which bucket by string value.
func evalCompare(c Compare, bnd *binding) (bool, error) {
	l, okl := resolve(c.Left, bnd)
	r, okr := resolve(c.Right, bnd)
	if !okl || !okr {
		return false, fmt.Errorf("query: comparison operand is unbound (a variable must appear in a relation before it is compared)")
	}
	return compareValues(l, c.Op, r), nil
}

// compareValues is the value-level half of evalCompare, split out so a HAVING filter compares by the
// same rules a goal comparison does. The three refusals documented on evalCompare live here; that
// function keeps the binding resolution and the unbound-operand error, which a having does not have
// (its operands are a reduced column and a group key, both already values).
func compareValues(l ns.Value, op string, r ns.Value) bool {
	if l.Absent || r.Absent {
		// An unstated value has no ORDER, but it does have an IDENTITY: two unstated bounds are the
		// same answer to "what does this row state". Routing equality through valueEq keeps the
		// explicit operator and implicit unification agreeing, which is the property that stops
		// `?a = ?b` and a repeated `?a` meaning different things.
		if orderingOps[op] {
			return false
		}
		eq := valueEq(l, r)
		if op == "!=" {
			return !eq
		}
		return eq
	}
	if l.Num != nil && r.Num != nil {
		if orderingOps[op] && l.BaseUnit != "" && r.BaseUnit != "" && l.BaseUnit != r.BaseUnit {
			return false
		}
		return cmpNum(*l.Num, op, *r.Num)
	}
	if orderingOps[op] && (l.Num != nil) != (r.Num != nil) {
		return false
	}
	return cmpStr(l.S, op, r.S)
}

func cmpNum(a float64, op string, b float64) bool {
	switch op {
	case "<":
		return a < b
	case "<=":
		return a <= b
	case ">":
		return a > b
	case ">=":
		return a >= b
	case "!=":
		return a != b
	default: // "="
		return a == b
	}
}

func cmpStr(a, op, b string) bool {
	switch op {
	case "<":
		return a < b
	case "<=":
		return a <= b
	case ">":
		return a > b
	case ">=":
		return a >= b
	case "!=":
		return a != b
	default: // "="
		return a == b
	}
}

// Columns returns the answer column order: the explicit Select, or the goal's variables in
// first-seen order when Select is empty. A caller printing a result table uses this for the header
// and the per-row order (Row.Bind is a map).
func (q Query) Columns() []Var {
	sel := q.Select
	if len(sel) == 0 {
		sel = defaultSelect(q.Goal)
	}
	return selKeys(sel)
}

// colLabel is a select item's column identifier: the variable, or "func(var)" for an aggregate.
// It is also the key an aggregate result is stored under in Row.Bind, so printing keys both alike.
func colLabel(t Term) Var {
	if t.Agg != nil {
		if t.Agg.Distinct {
			return Var(t.Agg.Func + "(distinct " + string(t.Agg.Var) + ")")
		}
		return Var(t.Agg.Func + "(" + string(t.Agg.Var) + ")")
	}
	return t.Var
}

func selKeys(sel []Term) []Var {
	out := make([]Var, 0, len(sel))
	for _, t := range sel {
		out = append(out, colLabel(t))
	}
	return out
}

// defaultSelect is the projection when none is given: the positive goal variables, first-seen. Only
// positive variables — a variable used only under negation is existential and not selectable.
func defaultSelect(goal Body) []Term {
	out := make([]Term, 0)
	for _, vv := range positiveVars(goal) {
		out = append(out, Term{Var: vv})
	}
	return out
}

// positiveVars returns the variables bound by positive atoms (including reaches), first-seen.
func positiveVars(goal Body) []Var {
	seen := map[Var]bool{}
	var out []Var
	for _, lit := range goal.Literals {
		if lit.Pos == nil {
			continue
		}
		for _, a := range lit.Pos.Args {
			if a.Var != "" && a.Var != "_" && !seen[a.Var] {
				seen[a.Var] = true
				out = append(out, a.Var)
			}
		}
	}
	return out
}

// validateSelect rejects a projection over a variable no positive relation binds (an unknown or
// negation-only existential) and an unknown aggregate function — so a bad query errors clearly. The
// having filters are checked by the same rules, since each is an aggregate over the same goal.
func validateSelect(sel []Term, having []Compare, goal Body) error {
	pv := map[Var]bool{}
	for _, vv := range positiveVars(goal) {
		pv[vv] = true
	}
	for _, t := range sel {
		if err := validateAggOrVar(t, pv); err != nil {
			return err
		}
	}
	keys := map[Var]bool{}
	for _, t := range sel {
		if t.Agg == nil && t.Var != "" {
			keys[t.Var] = true
		}
	}
	for _, h := range having {
		if err := validateAggOrVar(h.Left, pv); err != nil {
			return err
		}
		// Caught here rather than at evaluation so the query fails on the query, not on whichever
		// design happened to produce the first group.
		if h.Right.Var != "" && !keys[h.Right.Var] {
			return fmt.Errorf("query: having compares against ?%s, which is not a group key — after grouping only a selected variable or a constant is bound", h.Right.Var)
		}
		if h.Right.Agg != nil {
			return fmt.Errorf("query: having compares two aggregates, which is not supported (compare an aggregate against a constant or a group key)")
		}
	}
	return nil
}

// validateOrder checks the order by columns against the answer's columns as written, so a variable
// the host bound is still one to sort on, and checks the row counts a Query built in Go might carry.
func validateOrder(cols []Term, q Query) error {
	selected := map[Var]bool{}
	for _, c := range cols {
		selected[colLabel(c)] = true
	}
	for _, o := range q.OrderBy {
		if o.Term.Var == "_" || !selected[colLabel(o.Term)] {
			return fmt.Errorf("query: order by %s, which is not an answer column (sort on a column the projection selects)", o.Term)
		}
	}
	if q.Limit < 0 || q.Offset < 0 {
		return fmt.Errorf("query: limit %d offset %d: a row count can't be negative", q.Limit, q.Offset)
	}
	// Paging needs a stable order: a page continues from the one before it (#194).
	if q.Offset > 0 && len(q.OrderBy) == 0 {
		return fmt.Errorf("query: offset needs an order by, so each page continues where the one before it stopped")
	}
	if q.After == nil {
		return nil
	}
	if len(q.OrderBy) == 0 {
		return fmt.Errorf("query: after needs an order by: it continues from the last row in that order")
	}
	if len(q.After) != len(q.OrderBy) {
		return fmt.Errorf("query: after gives %d values for %d order by columns", len(q.After), len(q.OrderBy))
	}
	ordered := map[Var]bool{}
	for _, o := range q.OrderBy {
		ordered[colLabel(o.Term)] = true
	}
	for _, c := range cols {
		if c.Agg == nil && c.Var != "" && !ordered[c.Var] {
			return fmt.Errorf("query: after needs an order by naming ?%s too, or a row tying with the last one would be skipped", c.Var)
		}
	}
	return nil
}

func validateAggOrVar(t Term, pv map[Var]bool) error {
	switch {
	case t.Agg != nil:
		if !validAggFunc(t.Agg.Func) {
			return fmt.Errorf("query: unknown aggregate %q (want count/min/max/sum/list)", t.Agg.Func)
		}
		if t.Agg.Var != "" && !pv[t.Agg.Var] {
			return fmt.Errorf("query: %s aggregates ?%s, which no relation binds", t.Agg.Func, t.Agg.Var)
		}
	case t.Var != "" && !pv[t.Var]:
		return fmt.Errorf("query: projected ?%s is not bound by a positive relation (a variable used only under negation is existential and cannot be selected)", t.Var)
	}
	return nil
}

func validAggFunc(f string) bool {
	switch f {
	case "count", "min", "max", "sum", "list":
		return true
	}
	return false
}

func hasAggregate(sel []Term) bool {
	for _, t := range sel {
		if t.Agg != nil {
			return true
		}
	}
	return false
}

// passesHaving applies the group filters to one reduced row. The left side reads the aggregate column
// just computed; the right side is a constant, or a group key the projection also selected. Comparing
// against a variable the group does not determine is an error rather than a silent false, because a
// filter that quietly matches nothing is indistinguishable from a design with no such group.
func passesHaving(row Row, having []Compare) (bool, error) {
	for _, h := range having {
		left := row.Bind[colLabel(h.Left)]
		var right ns.Value
		switch {
		case h.Right.Const != nil:
			right = *h.Right.Const
		case h.Right.Var != "":
			v, ok := row.Bind[h.Right.Var]
			if !ok {
				return false, fmt.Errorf("query: having compares against ?%s, which is not a group key — only a selected variable or a constant is bound once the rows are grouped", h.Right.Var)
			}
			right = v
		default:
			return false, fmt.Errorf("query: having compares against an aggregate, which is not supported (compare against a constant or a group key)")
		}
		if !compareValues(left, h.Op, right) {
			return false, nil
		}
	}
	return true, nil
}

func groupKeyOf(keyVars []Var, bnd *binding) string {
	var b strings.Builder
	for _, kv := range keyVars {
		b.WriteString(valueKey(bnd.vals[kv]))
		b.WriteByte('\x1f')
	}
	return b.String()
}

// listSep joins a list aggregate's members. A space rather than a comma, because the members land in
// ONE csv cell and a comma there reads as a column break to every naive splitter downstream — the csv
// writer quotes it correctly, and the half of the world that reads csv with strings.Split does not.
const listSep = " "

// dedupSort sorts the answer rows in the default order, column by column, and removes duplicates
// (same projected text), so a query is a deterministic view. Sorting comes first and the first row of
// each key (valueKey) in sorted order is kept, so which row survives doesn't depend on the order the
// rows arrived in. N(1) and S("1") are two values (#162), and answer as two rows.
func dedupSort(rows []Row, sel []Var) []Row {
	sort.SliceStable(rows, func(i, j int) bool {
		for _, v := range sel {
			if c := orderValues(rows[i].Bind[v], rows[j].Bind[v]); c != 0 {
				return c < 0
			}
		}
		return false
	})
	seen := map[string]bool{}
	out := rows[:0]
	for _, r := range rows {
		if k := rowKey(r, sel); !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	return out
}

// orderValues is the default order on one column: absent values first, then numbers by value, then
// everything else by its text, with equal numbers ordered by their text. Comparing as numbers only
// when both values are numbers would not be an order on a mixed column (2 < 10 by value, "10" < "1a"
// and "1a" < "2" by text), so numbers and text are ranked apart instead, as SQLite ranks them. A
// number's unit doesn't take part: this orders a column, it doesn't compare quantities.
func orderValues(a, b ns.Value) int {
	rank := func(v ns.Value) int {
		switch {
		case v.Absent:
			return 0
		case v.Num != nil:
			return 1
		}
		return 2
	}
	if c := cmp.Compare(rank(a), rank(b)); c != 0 {
		return c
	}
	if a.Num != nil && b.Num != nil {
		if c := cmp.Compare(*a.Num, *b.Num); c != 0 {
			return c
		}
	}
	return strings.Compare(a.S, b.S)
}

// orderRows sorts the answer by the query's order by columns. The sort is stable and the rows arrive
// in the default order, so that order breaks every tie.
func orderRows(rows []Row, order []Order) {
	if len(order) == 0 {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		for _, o := range order {
			lbl := colLabel(o.Term)
			c := orderValues(rows[i].Bind[lbl], rows[j].Bind[lbl])
			if o.Desc {
				c = -c
			}
			if c != 0 {
				return c < 0
			}
		}
		return false
	})
}

// page is the rows limit and offset keep: skip offset, then keep at most limit (zero is no limit).
func page(rows []Row, limit, offset int) []Row {
	rows = rows[min(offset, len(rows)):]
	if limit > 0 && limit < len(rows) {
		rows = rows[:limit]
	}
	return rows
}

func rowKey(r Row, sel []Var) string {
	var b strings.Builder
	for _, v := range sel {
		b.WriteString(string(v))
		b.WriteByte('=')
		b.WriteString(valueKey(r.Bind[v]))
		b.WriteByte('\x1f')
	}
	return b.String()
}

// keyText is a value's text, with an absent value apart from the empty string (#62), for ordering
// derivations under CanonicalCites. Answers, groups and distinct key on valueKey, as equality does,
// so a derived column holding both 1 and "1" answers both (#162).
func keyText(v ns.Value) string {
	if v.Absent {
		return absentKey
	}
	return v.S
}

func dedupStrings(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }
