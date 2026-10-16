package ns

import (
	"context"
	"iter"
)

// WalkSource is optionally implemented by a Source that follows a relation's facts from value to value
// itself, such as a database answering SQLite's WITH RECURSIVE (#239). A recursive rule that probes a
// relation with the values its own results produce, as walk(?a, ?c) :- walk(?a, ?b), next(?b, ?c)
// probes next by each ?b it derived, otherwise costs a call per round of its fixpoint, since a round's
// values aren't known before the last round's answer. The planned evaluator asks a WalkSource for
// every fact the walk will reach in one call instead, files them as the lookups the rounds would have
// made, and derives the relation from them as it would have, so an answer, its citations and its
// witnesses are the same.
type WalkSource interface {
	// Walks reports whether the store walks rel from position from to position to, with the positions
	// in bound fixed to constants. The planner asks while planning, so it must be cheap and do no I/O,
	// and the answer must not change for the life of a Base.
	Walks(rel string, from, to int, bound []int) bool
	// Walk returns every fact of w.Relation matching w.Bound whose value at w.From is reached: one of
	// w.Start, or the value at w.To of a fact it returns. Values compare as Lookup's do, each read as
	// its argument's declared type. Like Lookup, it may return facts that don't match or aren't
	// reached, but must not leave out one that is: the engine treats what it returns for a reached
	// value as every fact the value has, and asks the store nothing more about it.
	//
	// Each fact is charged to the Eval's Budget as it arrives, and the engine may stop ranging early.
	// An error yielded stops the walk and fails the query. What a completed walk returned is kept as
	// lookups' results are (see LookupSource); a walk that stopped is not kept.
	Walk(ctx context.Context, w Walk) iter.Seq2[Tuple, error]
}

// A Walk asks a WalkSource for the facts of Relation reached from Start: each fact whose value at From
// is a start value or the value at To of a fact reached, and whose values at Bound's positions are
// those constants.
type Walk struct {
	Relation string
	From, To int
	Bound    map[int]Value
	Start    []Value
	// Units is the table the engine folds values by, as a Conjunction's (#205): a Federation folds by it
	// a value one member reached before walking it in another. Nil folds nothing.
	Units *Units
}

// ConjunctionWalkSource is optionally implemented by a Source that follows a step of several literals
// itself (#250): a recursive rule such as reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?m), attr(?m,
// "kind", "func"), edge(?m, ?c) walks the conjunction of its edge and attr literals from ?b to ?c, a
// call (or batch) per round however its bindings batch (#246). The planned evaluator asks for the whole
// walk in one call instead, as WalkSource does for a single relation, and files each value's matches
// where a round under that value looks, so answers, citations and witnesses don't change.
type ConjunctionWalkSource interface {
	// WalksConjunction reports whether the store walks c from its variable in to its variable out. It
	// is asked while planning and must be cheap.
	WalksConjunction(c Conjunction, in, out int) bool
	// WalkConjunction returns every match of c, one Tuple per atom as Push's rows, whose value at in is
	// reached: one of start, or the value at out of a match it returns. As with Walk, a match for a
	// reached value must not be left out, since the engine asks nothing more about that value, and
	// values compare as Lookup's do. Each row is charged to the Budget as it arrives.
	WalkConjunction(ctx context.Context, c Conjunction, in, out int, start []Value) iter.Seq2[[]Tuple, error]
}
