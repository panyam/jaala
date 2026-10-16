package datalog

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/panyam/jaala/ns"
)

// An Option adjusts one Eval: Bind gives goal variables values from the host, Budget limits its work.
type Option func(*evalOptions)

type evalOptions struct {
	bind      map[Var][]ns.Value
	budget    int64
	witness   bool
	canonical bool
	explain   *Report
	cold      bool // a cold run Explain started (see coldCost), which starts none of its own
	after     map[Var]ns.Value
	total     *int
	kept      *error // set by Materialize: where a DerivedStore's failed write is reported
	at        *ns.Version
}

// Bind gives goal variables values from the host, so a parameterized query needs no program text built
// per request: Eval(ctx, q, b, Bind(map[Var][]ns.Value{"target": {ns.S(id)}})). Rules are not
// affected: their variables are their own. Binding a variable the goal does not use is an error.
//
// A variable bound to one value is exactly a constant written in the goal, so inlining, demand (magic
// sets) and planning all start from it, and it stays an answer column, holding its value in every row.
// Like a constant, a bound value is read as the type of the argument it stands in (see
// ns.ArgType.Type), so a host holding only text, such as an input box's, can bind ns.S("3") to a
// number argument; text that argument cannot read is refused. The answer column still holds the value
// as bound.
//
// A variable bound to several values ranges over them, as if the goal were joined with a relation
// holding exactly those values (#132): the answer is the union over them, an aggregate reduces across
// all of them, and demand starts from every one, so a derived relation is evaluated only for the values
// asked. Each value is checked as a single bound value is, and an error names the one refused. Bound
// to no values, the variable matches nothing, so the answer is empty, or the one row an aggregate
// gives over nothing. The values carry no citations.
//
// ValidateBound checks a goal ahead of time knowing which variables the host will bind.
func Bind(values map[Var][]ns.Value) Option {
	return func(o *evalOptions) {
		if o.bind == nil {
			o.bind = map[Var][]ns.Value{}
		}
		for v, vals := range values {
			o.bind[v] = vals
		}
	}
}

// Budget limits one Eval to maxWork units of the work Base.Work counts (candidate comparisons, and
// each solution a generator emits), plus each tuple an ns.StreamSource yields as a relation is read
// whole (#233). Exceeding it stops the Eval with a *BudgetExceeded. The budget is the Eval's own:
// concurrent Evals on one Base do not spend each other's. It bounds work, and memory only so far: the
// goal holds one row per distinct answer or one accumulator per group, not its bindings (#120), and a
// streamed read stops at the budget, but a Source read as one slice arrives whole before it is
// counted, and the derived relations its rules produce are held whole.
func Budget(maxWork int64) Option {
	return func(o *evalOptions) { o.budget = maxWork }
}

// After answers only the rows strictly after row in the query's order by, as the query's after clause
// does: row is the last one of the previous page, as the host got it, so the next page continues from
// it by value rather than by count, and stays put when facts change between pages (#194). The query
// needs an order by naming every variable column, so no row ties with row and is skipped. It replaces
// an after clause the query writes.
func After(row map[Var]ns.Value) Option {
	return func(o *evalOptions) { o.after = row }
}

// Total sets *n to the number of rows the answer has before after, offset and limit, so a host
// showing a page can say how many it left out (#194). A limit without an order by solves the whole
// goal when Total is asked for, since counting needs every row.
func Total(n *int) Option {
	return func(o *evalOptions) { o.total = n }
}

// BudgetExceeded is the error an Eval stops with when its work passes its Budget, so a host can tell a
// query that ran too long from one that is wrong. Work counts the streamed tuples read too, so it can
// be more than Base.Work or Report.Work saw.
type BudgetExceeded struct {
	Work, Limit int64
}

func (e *BudgetExceeded) Error() string {
	return fmt.Sprintf("query: evaluation stopped: work passed its budget of %d (at %d)", e.Limit, e.Work)
}

// evalRun is one Eval's own state, carried on its copy of the Base: its context, and the work it has
// done against its budget.
type evalRun struct {
	ctx    context.Context
	budget int64
	used   int64
	// read counts the tuples a StreamSource has yielded, which the budget charges beside used (#233)
	// but which aren't work: Work and Explain count what evaluation does with the tuples.
	read    int64
	held    int  // the answer rows or groups the goal held at its peak (Report.Held)
	witness bool // record witnesses (see Witnesses)
	// canonical keeps each tuple's canonical derivation (see CanonicalCites), and revised lists, by
	// relation, the tuples a later derivation replaced since the last semi-naive mark.
	canonical bool
	revised   map[string][]int
	// readErr is the first Source read the planner made that failed (see fanOut). Planning can't
	// return an error, so evaluate reports it once the rewrite is done.
	readErr error
	// keys are the cache keys of the relations this Eval may keep once derived, and preload the ones
	// the Base already held (see reuseDerived). Both nil unless SemiNaive's rewrite ran.
	keys    map[string]string
	preload map[string]*derivedEntry
	// gen is the derived cache's generation the Eval may store into, and version the Source's version
	// it read (versioned false when the Source has none), which files what it keeps in a DerivedStore.
	gen       int64
	version   string
	versioned bool
	// looked holds what the Source's Lookup returned, by lookupKey, for this Eval only (see lookup),
	// and lookGen is the generation of the Base's lookupCache it may store into.
	looked  map[string][]ns.Tuple
	lookGen int64
	// explain is the Explain report being filled, or nil.
	explain *explainer
	// pushed are the groups of base literals pushConjunctions handed the Source, by their literal's
	// relation name (#237).
	pushed map[string]*pushGroup
	// walks are, by head relation, the walks its recursive bodies make that the Source walks (#239).
	walks map[string][]walkStep
	// stepWalks are, by head relation, the walks of a pushed group its recursive bodies make that the
	// Source walks (#250).
	stepWalks map[string][]stepWalk
	// batch sets prefetch asking a batching Source for a body's lookups ahead of solving it (#153),
	// which only the planned SemiNaive does.
	batch bool
	// streamable are the base relations only the goal reads, which its first literal may stream rather
	// than read whole (#264), and stream is that literal while the goal is solved (see streamFirst).
	streamable map[string]bool
	stream     *Atom
}

// ctxCheckEvery is how many units of work pass between checks of the context. Checking on every
// comparison would cost more than the joins it guards; this keeps a cancelled Eval stopping within
// microseconds of work.
const ctxCheckEvery = 1024

// step records one unit of work for this Eval and reports whether it must stop: its budget is spent,
// or its context is done.
func (r *evalRun) step() error {
	if r == nil {
		return nil
	}
	r.used++
	if r.explain != nil {
		r.explain.count()
	}
	return r.check(r.used)
}

// readOne records one tuple a StreamSource yielded and reports whether the Eval must stop, as step
// does, so a relation read whole stops at the budget having held at most that many tuples (#233).
func (r *evalRun) readOne() error {
	if r == nil {
		return nil
	}
	r.read++
	return r.check(r.read)
}

// check stops the Eval once the work and reads together pass the budget, and every ctxCheckEvery
// units of n when its context is done.
func (r *evalRun) check(n int64) error {
	if spent := r.used + r.read; r.budget > 0 && spent > r.budget {
		return &BudgetExceeded{Work: spent, Limit: r.budget}
	}
	if n%ctxCheckEvery == 0 {
		return r.done()
	}
	return nil
}

// done reports whether the Eval's context has ended, as the error the Eval returns.
func (r *evalRun) done() error {
	if r == nil || r.ctx == nil {
		return nil
	}
	if err := r.ctx.Err(); err != nil {
		return fmt.Errorf("query: evaluation stopped: %w", err)
	}
	return nil
}

func (r *evalRun) context() context.Context {
	if r == nil || r.ctx == nil {
		return context.Background()
	}
	return r.ctx
}

// bindGoal substitutes the host's values for goal variables bound to one value, and returns the goal's
// answer columns as written, so a bound variable is still a column. A variable bound to several values,
// or none, stays a variable, for bindSets to range over. It refuses a variable the goal does not use.
func bindGoal(q Query, bind map[Var][]ns.Value) (Query, []Term, error) {
	sel := q.Select
	if len(sel) == 0 {
		sel = defaultSelect(q.Goal)
	}
	if len(bind) == 0 {
		return q, sel, nil
	}
	used := map[Var]bool{}
	for _, l := range q.Goal.Literals {
		for _, t := range literalTerms(l) {
			used[t.Var] = true
		}
	}
	var unknown []string
	for v := range bind {
		if !used[v] || v == "_" {
			unknown = append(unknown, "?"+string(v))
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return q, nil, fmt.Errorf("query: cannot bind %s: the goal does not use it", strings.Join(unknown, ", "))
	}
	sub := func(t Term) Term {
		if vals := bind[t.Var]; len(vals) == 1 && t.Var != "" {
			c := vals[0]
			return Term{Const: &c}
		}
		return t
	}
	out := q
	out.Goal = substBody(q.Goal, sub)
	out.Select = nil
	for _, t := range sel {
		if len(bind[t.Var]) == 1 && t.Var != "" && t.Agg == nil {
			continue // filled in after evaluation: the same value in every row
		}
		out.Select = append(out.Select, t)
	}
	return out, sel, nil
}

// substBody rewrites every term of a body through sub. A rebuilt literal keeps its at.
func substBody(body Body, sub func(Term) Term) Body {
	out := Body{Literals: make([]Literal, len(body.Literals))}
	for i, l := range body.Literals {
		switch {
		case l.Pos != nil:
			a := substAtom(*l.Pos, sub)
			l.Pos = &a
		case l.Neg != nil:
			a := substAtom(*l.Neg, sub)
			l.Neg = &a
		case l.Compare != nil:
			c := *l.Compare
			c.Left, c.Right = sub(c.Left), sub(c.Right)
			l.Compare = &c
		case l.Agg != nil:
			a := *l.Agg
			a.Body = substBody(a.Body, sub)
			l.Agg = &a
		}
		out.Literals[i] = l
	}
	return out
}

func substAtom(a Atom, sub func(Term) Term) Atom {
	out := Atom{Relation: a.Relation, Args: make([]Term, len(a.Args))}
	for i, t := range a.Args {
		out.Args[i] = sub(t)
	}
	return out
}

// literalTerms is every term a literal mentions.
func literalTerms(l Literal) []Term {
	switch {
	case l.Pos != nil:
		return l.Pos.Args
	case l.Neg != nil:
		return l.Neg.Args
	case l.Compare != nil:
		return []Term{l.Compare.Left, l.Compare.Right}
	}
	return nil
}
