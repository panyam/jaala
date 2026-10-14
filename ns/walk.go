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
}
