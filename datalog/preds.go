package datalog

import (
	"errors"
	"fmt"
	"github.com/panyam/jaala/ns"
	"slices"
	"strings"
)

// This file is the single positive primitive of the evaluator: extendAtom. Every callable relation —
// a fact relation (EDB), a rule-derived relation (IDB), or a computed built-in (a filter, or a
// host-supplied generator) — is "given a partial binding, yield zero-or-more extended bindings."
// solve() drives the positive body through extendAtom; negation reuses the SAME primitive (atomHolds
// runs extendAtom and asks only whether it yields anything — negation as failure). That unification
// is why there is one dispatch, not one per kind, and why `not R(...)` works uniformly for EDB, IDB,
// filters, and generators.

// extendBuiltin runs a builtin under the current binding.
func extendBuiltin(bi ns.Builtin, atom *Atom, bnd *binding, b *Base, yield func(*binding) error) error {
	if b.run != nil && b.run.explain != nil {
		if bi.Holds != nil {
			b.run.explain.access("filter")
		} else {
			b.run.explain.access("generator")
		}
	}
	if bi.Holds != nil {
		args := make([]ns.Value, len(atom.Args))
		for i, a := range atom.Args {
			v, ok := resolve(a, bnd)
			if !ok {
				return fmt.Errorf("query: %s needs all arguments bound (a variable must appear in a relation before %s tests it)", atom.Relation, atom.Relation)
			}
			args[i] = v
		}
		ok, err := bi.Holds(args)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if b.witnessing() {
			c := bnd.clone()
			c.last = leaf(atom.Relation, args, nil)
			return yield(c)
		}
		return yield(bnd)
	}
	args := make([]ns.Arg, len(atom.Args))
	for i, a := range atom.Args {
		v, ok := resolve(a, bnd)
		args[i] = ns.Arg{Value: v, Bound: ok}
	}
	return bi.Gen(b.run.context(), b.src, args, func(vals []ns.Value, cites []string) error {
		if err := b.countWork(); err != nil {
			return err
		}
		if len(vals) != len(atom.Args) {
			return fmt.Errorf("query: internal: %s emitted %d values for %d arguments", atom.Relation, len(vals), len(atom.Args))
		}
		next := bnd.clone()
		for j, arg := range atom.Args {
			if !bindArg(next, arg, vals[j], false) {
				return nil
			}
		}
		next.cites = append(next.cites, cites...)
		if b.witnessing() {
			next.last = leaf(atom.Relation, vals, cites)
		}
		return yield(next)
	})
}

// extendAtom is the single positive primitive: it yields every binding that satisfies atom as an
// extension of bnd. Dispatch is by relation kind — a computed built-in, an EDB fact relation, or an
// IDB rule relation — each checked for arity first so a wrong-arity atom fails clearly. yield is
// called per solution and its error (from a deeper solve, or the negation early-stop) propagates.
func (b *Base) extendAtom(atom *Atom, bnd *binding, yield func(*binding) error) error {
	if err := b.checkAtom(atom); err != nil {
		return err
	}
	rel := atom.Relation
	if bi, ok := b.reg.Predicate(rel); ok {
		return extendBuiltin(bi, atom, bnd, b, yield)
	}
	if _, ok := b.schemaOf(rel); ok {
		return b.extendEDB(atom, bnd, yield)
	}
	return b.extendIDB(atom, bnd, yield)
}

// schemaOf is the schema of the base relation registered at rel.
func (b *Base) schemaOf(rel string) (ns.Schema, bool) { return b.reg.Schema(rel) }

// checkArgValues rejects a CONSTANT naming a value its argument cannot hold, for the arguments whose
// values are a vocabulary the Source defines. A variable is unaffected, and a relation declaring no
// domain is unchanged.
//
// It exists because the alternative is silence: a misspelled constant matches nothing and answers
// "no results", which reads as a fact about the data rather than a typo. An empty answer to a
// question that was never valid is the worst available outcome.
//
// An absent constant is no misspelling: query text can't write one, so it comes from a host's Bind,
// or from the placeholder ValidateBound binds, and it is left to match what it matches (#68).
func (b *Base) checkArgValues(atom *Atom, s ns.Schema) error {
	for i, arg := range atom.Args {
		if arg.Const == nil || arg.Const.Absent || i >= len(s.Labels) || i >= len(s.Types) {
			continue
		}
		label := s.Labels[i]
		allowed := s.Types[i].Domain
		if len(allowed) == 0 {
			continue
		}
		got := arg.Const.S
		if slices.Contains(allowed, got) {
			continue
		}
		return fmt.Errorf("query: %s's %q argument cannot be %q%s (it holds one of: %s)",
			atom.Relation, label, got, ns.DidYouMeanValue(allowed, got), strings.Join(allowed, ", "))
	}
	return nil
}

// checkAtom reports whether an atom names something the evaluator can read, at an arity that relation
// accepts. It is the three-way dispatch's precondition, split out so Validate can apply it to EVERY
// atom without evaluating.
//
// Splitting it matters: solving stops as soon as an atom yields nothing, so a wrong-arity atom LATER
// in a body is never reached on data where an earlier one matches nothing. Checking arity only where
// a solve happens to arrive is checking it sometimes.
func (b *Base) checkAtom(atom *Atom) error {
	rel := atom.Relation
	if bi, ok := b.reg.Predicate(rel); ok {
		if !bi.Accepts(len(atom.Args)) {
			return fmt.Errorf("query: %s takes %s args, got %d", rel, bi.ArityLabel(), len(atom.Args))
		}
		return nil
	}
	if s, ok := b.schemaOf(rel); ok {
		if len(atom.Args) != s.Arity {
			return fmt.Errorf("query: relation %q takes %d args, got %d", rel, s.Arity, len(atom.Args))
		}
		return b.checkArgValues(atom, s)
	}
	if b.isIDB(rel) {
		if len(atom.Args) != b.idbArity[rel] {
			return fmt.Errorf("query: relation %q takes %d args, got %d", rel, b.idbArity[rel], len(atom.Args))
		}
		if s, ok := b.derivedSchema(rel); ok {
			return b.checkArgValues(atom, s)
		}
		return nil
	}
	return fmt.Errorf("query: %s", b.reg.Unknown(rel))
}

// extendEDB fans an EDB atom over the tuples of its relation, unifying each into the binding.
//
// When the binding already fixes some of the atom's arguments, the candidates come from an index on
// exactly those positions instead of from the whole relation. unify still decides every candidate,
// so the index only ever has to avoid MISSING a match; see index.go.
func (b *Base) extendEDB(atom *Atom, bnd *binding, yield func(*binding) error) error {
	if b.looksUp(atom.Relation) {
		if rows, ok, err := b.lookup(atom, bnd); err != nil || ok {
			if err != nil {
				return err
			}
			return b.unifyEach(atom, rows, nil, true, bnd, yield)
		}
	}
	rows, err := b.edbTuples(atom.Relation)
	if err != nil {
		return err
	}
	if b.run != nil && b.run.explain != nil {
		b.explainEDB(atom, rows, bnd)
	}
	pos, all := b.edbCandidates(atom, rows, bnd)
	return b.unifyEach(atom, rows, pos, all, bnd, yield)
}

// unifyEach unifies atom with each candidate, rows[pos[i]] for every i, or every row when all is set.
func (b *Base) unifyEach(atom *Atom, rows []ns.Tuple, pos []int, all bool, bnd *binding, yield func(*binding) error) error {
	for i := 0; ; i++ {
		var t ns.Tuple
		if all {
			if i >= len(rows) {
				break
			}
			t = rows[i]
		} else {
			if i >= len(pos) {
				break
			}
			t = rows[pos[i]]
		}
		if err := b.countWork(); err != nil {
			return err
		}
		if next, ok := unify(atom.Args, t, bnd); ok {
			if b.witnessing() {
				next.last = leaf(atom.Relation, t.Vals, t.Cites)
			}
			if err := yield(next); err != nil {
				return err
			}
		}
	}
	return nil
}

// extendIDB fans a rule-defined atom over the derived tuples of its relation, carrying each tuple's
// provenance forward — the same shape as extendEDB, over the materialized IDB store.
func (b *Base) extendIDB(atom *Atom, bnd *binding, yield func(*binding) error) error {
	if b.run != nil && b.run.explain != nil {
		b.explainIDB(atom, bnd)
	}
	for _, t := range b.idbCandidates(atom, bnd) {
		if err := b.countWork(); err != nil {
			return err
		}
		out := bnd.clone()
		ok := true
		for j, arg := range atom.Args {
			if !bindArg(out, arg, t.vals[j], j < 64 && t.weak&(1<<j) != 0) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		out.cites = append(out.cites, t.cites...)
		out.last, out.parts = t.wit, t.parts
		if err := yield(out); err != nil {
			return err
		}
	}
	return nil
}

// errStop unwinds extendAtom after the first yield — negation only needs existence, not enumeration.
var errStop = errors.New("query: stop")

// atomHolds reports whether atom has any solution under bnd (negation as failure): `not atom` holds
// exactly when this is false. It runs the same extendAtom the positive solve uses, stopping at the
// first match. A malformed atom (an unbound filter argument, wrong arity) surfaces as an error rather
// than silently reading as "no match".
func (b *Base) atomHolds(atom *Atom, bnd *binding) (bool, error) {
	found := false
	err := b.extendAtom(atom, bnd, func(*binding) error { found = true; return errStop })
	if err != nil && err != errStop {
		return false, err
	}
	return found, nil
}

// arityAccepts reports whether n is a valid argument count for any callable relation — built-in, EDB,
// or IDB — and whether the relation exists at all. It is the SAME admission test extendAtom applies
// on the positive path, so a variadic built-in cannot be accepted in a positive atom while its
// negation is rejected.
func (b *Base) arityAccepts(rel string, n int) (ok bool, known bool) {
	if bi, found := b.reg.Predicate(rel); found {
		return bi.Accepts(n), true
	}
	if s, found := b.schemaOf(rel); found {
		return n == s.Arity, true
	}
	if ar, found := b.idbArity[rel]; found {
		return n == ar, true
	}
	return false, false
}

// arityLabelOf renders a relation's accepted argument count for an error message.
func (b *Base) arityLabelOf(rel string) string {
	if bi, ok := b.reg.Predicate(rel); ok {
		return bi.ArityLabel()
	}
	if s, ok := b.schemaOf(rel); ok {
		return fmt.Sprintf("%d", s.Arity)
	}
	return fmt.Sprintf("%d", b.idbArity[rel])
}

// derivedSchema is a derived member's signature as a Schema, so a query constant outside a closed
// vocabulary is refused for it exactly as for a base relation.
func (b *Base) derivedSchema(rel string) (ns.Schema, bool) {
	sigs := b.sigs
	if sigs == nil {
		res, err := resolved(b.reg)
		if err != nil {
			return ns.Schema{}, false
		}
		sigs = res.sigs
	}
	sig, ok := sigs[rel]
	if !ok {
		return ns.Schema{}, false
	}
	s := ns.Schema{Arity: len(sig), Labels: make([]string, len(sig)), Types: make([]ns.ArgType, len(sig))}
	for i, a := range sig {
		s.Labels[i], s.Types[i] = a.Name, a.ArgType
	}
	return s, true
}
