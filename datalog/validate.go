package datalog

import "github.com/panyam/jaala/ns"

// Validate reports why a query cannot run, reading only the query and the vocabulary it would run
// against: the schemas of its base relations, its predicates and its derived modules, which it links
// first (see Link). No tuple is read.
//
// It exists because a rule compiled from a query used to be un-rejectable: RuleFromQuery returned no
// error, and the failure surfaced only when the rule ran, where it was swallowed into a clean pass
// (agni issue 540). Every fault a query can carry turns out to be static — an unknown relation, a
// wrong arity, a negation with nothing to range over, a projected variable nothing binds, a rule head
// colliding with a fact relation, recursion through negation — so the check belongs where the rule is
// BUILT rather than where it first meets a design.
//
// It cannot be implemented by evaluating against an empty fact base, which is the obvious shortcut.
// Solving stops at the first atom that yields nothing, so on an empty base every atom after the first
// goes unexamined and a wrong-arity relation in position two passes validation.
func Validate(q Query, reg *ns.Vocabulary) error {
	return ValidateBound(q, reg)
}

// ValidateBound is Validate for a query whose host will Bind the given goal variables when it runs it.
// A bound variable counts as a constant written in the goal, as it does in Eval, so a variable that
// appears only inside a `not`, or one a generator's mode needs bound, is accepted when the host binds
// it and still refused when it doesn't (#61). Only the names matter: each stands in as an absent
// value, which neither coercion nor a closed Domain refuses (#68).
// Naming a variable the goal does not use is the error Eval gives for binding one.
func ValidateBound(q Query, reg *ns.Vocabulary, vars ...Var) error {
	bind := make(map[Var][]ns.Value, len(vars))
	for _, v := range vars {
		bind[v] = []ns.Value{ns.Absent()}
	}
	q, err := lowerBodyAggregates(q, "")
	if err != nil {
		return err
	}
	if q, err = lowerExpressions(q, reg); err != nil {
		return err
	}
	written := q
	q, cols, err := bindGoal(q, bind)
	if err != nil {
		return err
	}
	// As in evaluate: a bound variable leaves the projection while the goal is checked, and order by
	// is checked against the columns as written.
	sel := cols
	if len(bind) > 0 {
		sel = q.Select
	}
	q, err = Link(q, reg)
	if err != nil {
		return err
	}
	if q, err = coerceConstants(q, reg); err != nil {
		return err
	}
	if err := checkComparisonUnits(q, reg); err != nil {
		return err
	}
	// A vocabulary holding NO base relation cannot say a relation is unknown, and refusing every query on that
	// basis would be a confident wrong answer about the query rather than about the vocabulary. This
	// is not a corner case: a host that builds rules at package init may do so before its relations
	// are registered. "No such relation" and "no relations at all" are different answers, and this is
	// one of the places the difference has to be honoured.
	//
	// Everything else still runs: negation safety, projection safety, rule-head collisions, arity of a
	// derived relation, stratification. Only the checks that need a vocabulary stand down.
	if err := checkNoAggregates("the query", q.Goal); err != nil {
		return err
	}
	if err := checkComparisons(q.Goal); err != nil {
		return err
	}
	if len(reg.BaseRelations()) == 0 {
		return validateWithoutVocabulary(q, reg, written, bind, sel, cols)
	}
	b := newValidationBase(reg)
	b.inputRels = inputRelations(q.Rules, reg)
	if _, _, err := b.checkRules(q.Rules); err != nil {
		return err
	}
	// Every atom of every rule body, then of the goal. Negated literals are atoms too: a `not` over a
	// misspelled relation is as broken as a positive one.
	for _, r := range q.Rules {
		if err := b.checkLiterals(r.Body.Literals); err != nil {
			return err
		}
	}
	if err := b.checkLiterals(q.Goal.Literals); err != nil {
		return err
	}
	if err := checkModes(b, "the query", q.Goal); err != nil {
		return err
	}
	if err := checkFilters(b, q.Goal, nil); err != nil {
		return err
	}
	_, negs := splitNegations(q.Goal.Literals)
	if err := b.checkNegatedRelations(negs); err != nil {
		return err
	}
	if err := checkWrittenAnchors(written.Goal, q.Rules, bind, b.inputRels, reg); err != nil {
		return err
	}
	if err := validateSelect(sel, q.Having, q.Goal); err != nil {
		return err
	}
	if err := validateOrder(cols, written); err != nil {
		return err
	}
	_, err = lowerInputs(q, bind, reg) // every call binds its inputs (#200)
	return err
}

// validateWithoutVocabulary is Validate minus the checks that need a relation catalog installed.
//
// A rule built here is not left unvalidated forever: the query still has to run, and the evaluator
// checks every atom it reaches against the real vocabulary. What is lost is only the EARLY report,
// for a caller that built its rule before any relation was installed.
func validateWithoutVocabulary(q Query, reg *ns.Vocabulary, written Query, bind map[Var][]ns.Value, sel, cols []Term) error {
	b := newValidationBase(reg)
	b.inputRels = inputRelations(q.Rules, reg)
	if _, _, err := b.checkRules(q.Rules); err != nil {
		return err
	}
	if err := checkFilters(b, q.Goal, nil); err != nil {
		return err
	}
	_, negs := splitNegations(q.Goal.Literals)
	if err := b.checkNegatedRelations(negs); err != nil {
		return err
	}
	if err := checkWrittenAnchors(written.Goal, q.Rules, bind, b.inputRels, reg); err != nil {
		return err
	}
	if err := validateSelect(sel, q.Having, q.Goal); err != nil {
		return err
	}
	if err := validateOrder(cols, written); err != nil {
		return err
	}
	_, err := lowerInputs(q, bind, reg) // every call binds its inputs (#200)
	return err
}

// checkLiterals applies checkAtom to every relation-bearing literal, positive or negated. A
// comparison carries no relation and is checked by the solver's own operand rules.
func (b *Base) checkLiterals(lits []Literal) error {
	for _, lit := range lits {
		var a *Atom
		switch {
		case lit.Pos != nil:
			a = lit.Pos
		case lit.Neg != nil:
			a = lit.Neg
		default:
			continue
		}
		if err := b.checkAtom(a); err != nil {
			return err
		}
	}
	return nil
}

// newValidationBase builds a Base carrying a vocabulary and no tuples.
//
// It is deliberately NOT a usable evaluation base: it never reads a tuple, and nothing here
// evaluates. Giving it its own constructor keeps it from being mistaken for one that answers
// questions.
func newValidationBase(reg *ns.Vocabulary) *Base {
	return &Base{
		reg:      reg,
		idb:      map[string][]idbTuple{},
		idbArity: map[string]int{},
		idbIdx:   map[idxKey]*idbIndex{},
		work:     new(int64),
	}
}
