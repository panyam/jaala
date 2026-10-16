package datalog

import (
	"context"
	"fmt"

	"github.com/panyam/jaala/ns"
)

// Materialize evaluates each of rels in full over b, with q's rules and the vocabulary's modules, and
// keeps it, and every derived relation it reads, as an Eval that evaluated them in full would: in the
// Base's memory and, for a Versioned Source, in the DerivedStore KeepDerivedIn gave it (#220). A host
// calls it to fill the store ahead of the queries, or to put a relation there that its own tools read
// as facts, such as derived edges a walk follows. q's goal is ignored. A relation already held is read
// rather than derived again, and written to the store when the store doesn't hold it yet.
//
// Every relation in rels must be derived, by one of q's rules or a module's member. It returns the
// first error an evaluation or the store's KeepDerived gave. Under Witnesses or CanonicalCites nothing
// is kept, as in Eval.
func (s SemiNaive) Materialize(ctx context.Context, q Query, b *Base, rels []string, opts ...Option) error {
	for _, rel := range rels {
		arity, ok := materializedArity(q, b.reg, rel)
		if !ok {
			return fmt.Errorf("query: Materialize: %q is not a derived relation", rel)
		}
		g := Atom{Relation: rel, Args: make([]Term, arity)}
		for i := range g.Args {
			g.Args[i] = Term{Var: Var(fmt.Sprintf("v%d", i))}
		}
		one := Query{Rules: q.Rules, Goal: Body{Literals: []Literal{{Pos: &g}}}, Limit: 1}
		var kept error
		rewrite := func(b *Base, q Query) Query {
			// No inlining or demand, so every relation the goal reaches is derived in full under its own name.
			q = reuseDerived(b, withoutUnreached(q))
			if s.WrittenOrder {
				return q
			}
			b.run.batch = true
			return pushConjunctions(b, plan(b, q))
		}
		o := append(opts[:len(opts):len(opts)], func(o *evalOptions) { o.kept = &kept })
		if _, err := evaluate(ctx, one, b, o, rewrite, s.materialize); err != nil {
			return err
		}
		if kept != nil {
			return fmt.Errorf("query: Materialize: keeping %s: %w", rel, kept)
		}
	}
	return nil
}

// materializedArity is the arity of rel when q's rules or a module derive it.
func materializedArity(q Query, reg *ns.Vocabulary, rel string) (int, bool) {
	for _, r := range q.Rules {
		if r.Head.Relation == rel {
			return len(r.Head.Args), true
		}
	}
	if reg == nil {
		return 0, false
	}
	sig, ok := reg.Signature(rel)
	return len(sig), ok
}
