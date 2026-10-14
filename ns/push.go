package ns

import (
	"context"
	"iter"
)

// PushSource is optionally implemented by a Source that evaluates a conjunction over its own facts,
// such as a database turning one into a single statement (#237). When a Source implements it, the
// planned evaluator hands it a group of a body's base literals joined by shared variables, with the
// comparisons over them, and solves the rest of the body itself, so a join within the store costs one
// call and ships only the rows that join rather than every intermediate tuple.
//
// The evaluator pushes a group only when nothing earlier in the body binds its variables, so the
// group's only inputs are its constants (a group bound partway through a body is looked up literal by
// literal, as before). A group is at least two literals; one literal is a Lookup.
type PushSource interface {
	// Pushes reports whether the store evaluates c. The planner asks while planning, growing a group
	// one literal at a time and offering it with its comparisons and then without, so it must be
	// cheap and do no I/O. The answer for a conjunction must not change for the life of a Base. A
	// store that pushes c also pushes each of its suffixes (the atoms from some k on, and the
	// comparisons) with some variables replaced by constants, which a Federation asks to continue a
	// prefix.
	Pushes(c Conjunction) bool
	// Push answers c: one row per combination of facts satisfying it, each row one Tuple per atom of
	// c, in c.Atoms' order, with the fact's Cites, so an answer keeps its witnesses and citations.
	//
	// As with Lookup, a row may be one that doesn't satisfy c, since the engine checks every row's
	// tuples against the atoms and the comparisons again, but a row that does satisfy it must not be
	// left out, or the answer silently loses it. Values compare as Lookup's do: each read as its
	// argument's declared type, two numbers equal when the numbers are, two texts when the text is,
	// and a number never text. A store may skip any comparison it can't evaluate exactly as the
	// engine does (see PushComparison) and leave it to the engine. A row repeated is two
	// derivations, as a fact a Source serves twice is two facts, so a row is never sent twice.
	//
	// Each row is charged to the Eval's Budget as it arrives, so the engine may stop ranging early
	// (the budget is spent or the query's context ends) and the sequence should then release what
	// it holds. An error yielded stops the push and fails the query, wrapped with the conjunction.
	// What a completed push returned is kept as a Lookup's is (see LookupSource); one that stopped
	// is not kept.
	Push(ctx context.Context, c Conjunction) iter.Seq2[[]Tuple, error]
}

// A Conjunction is a group of base literals a PushSource evaluates together, and the comparisons over
// their variables.
type Conjunction struct {
	Atoms   []PushAtom
	Compare []PushComparison
	// Partial asks for every prefix of each match too: besides the rows satisfying every atom, for
	// each k from 1 below len(Atoms), one row of k tuples per combination of facts satisfying the
	// first k atoms (comparisons apply only to whole rows). A Federation over partitioned members
	// (NewPartitionedFederation) asks it, continuing a prefix in the members holding the facts the
	// next atom needs, so a join whose facts span members is still answered whole. A store that
	// can't return prefixes refuses a Partial conjunction in Pushes, and is then asked literal by
	// literal; one that returns only whole rows loses the matches that span members.
	Partial bool
	// Page asks for only the matches behind the first rows of a goal's answer (#241); nil asks for
	// every match.
	Page *Page
}

// A Page limits a pushed goal to the matches behind its first Limit answer rows. An answer row is a
// match's values at Columns, and rows are distinct: matches with equal values there make one row. The
// rows are ordered by Order's keys and then by Columns, each ascending unless a key says Desc, in the
// engine's order: an absent value first, then numbers by value, then text by its bytes. With no Order
// the store may take any Limit rows. The store returns every match behind each of the rows it takes,
// so the engine picks among them as it would in memory; it may return more matches or more rows,
// never fewer of either. Across stores, the rows the answer needs are among each store's first Limit,
// which is why a store can answer a page of a federation's answer alone.
type Page struct {
	Columns []int
	Order   []PageKey
	Limit   int
}

// A PageKey orders a Page by a variable of the Conjunction.
type PageKey struct {
	Var  int
	Desc bool
}

// SummarySource is optionally implemented by a PushSource that reduces a conjunction's matches itself,
// such as a database answering GROUP BY (#240). The planned evaluator asks it for a goal that
// aggregates, and reduces what comes back with its own rules (exact sums, units, NaN, absent over
// nothing), so a count costs a row per group and store rather than a row per match.
type SummarySource interface {
	// Summarizes reports whether the store summarizes c as s asks. Like Pushes, it is asked while
	// planning and must be cheap.
	Summarizes(c Conjunction, s Summary) bool
	// Summarize returns c's matches as rows of a Summary: every match in exactly one row, whose Group
	// and Values are the match's values at s.Group and s.Values, Matches how many matches the row
	// covers, and Cites the citations of every fact behind them. Two matches share a row only when
	// their values there are identical, the same text, number and unit, since an aggregate reads each
	// one: 5 V and 5 A are one number but two units, and a sum over them is absent. A store may split
	// matches it could have merged, since the engine merges rows by its own equality, but a match
	// counted twice, or left out, changes the answer. A match is a combination of facts satisfying
	// every atom of c, its constants and joins, which the engine can't check again; c's comparisons the
	// store may skip, since the engine asks for the values they read (in s.Values) and checks them on
	// each row. Each row is charged to the Budget as it arrives.
	Summarize(ctx context.Context, c Conjunction, s Summary) iter.Seq2[SummaryRow, error]
}

// A Summary asks for a conjunction's matches grouped by the values of the variables in Group and in
// Values. The engine reads Group as an aggregate's group and Values as the values it reduces; a count
// of bindings needs neither, so a goal counting with no group asks for one row.
type Summary struct {
	Group  []int
	Values []int
}

// A SummaryRow is one row of a Summary: the values at its Group and Values variables, in their order,
// the number of matches with them, and those matches' facts' citations.
type SummaryRow struct {
	Group   []Value
	Values  []Value
	Matches int
	Cites   []string
}

// A PushAtom is one literal of a Conjunction: a relation the Source serves, and an argument per
// position of its schema.
type PushAtom struct {
	Relation string
	Args     []PushArg
}

// A PushArg is one argument of a PushAtom or one side of a PushComparison: a constant when Const is
// set, a variable when Var is (numbered from 1 within the Conjunction, the same number wherever the
// variable appears, so a variable written twice is a join), and otherwise a wildcard matching
// anything. A constant is read as its argument's declared type, as a stored value is: a Federation
// continuing a prefix passes on values as the member that stored them wrote them.
type PushArg struct {
	Var   int
	Const *Value
}

// A PushComparison is a comparison the conjunction's rows must pass: Op is one of = != < <= > >=,
// and each side a variable of the Conjunction or a constant, never a wildcard.
//
// The engine orders values totally: an absent value first, then numbers by value, then text by
// its bytes. An ordering comparison is false when either side is absent, when one side is a number
// and the other text, or when both are numbers with different units; a number with no unit compares
// with a number in any unit. = and != follow equality as Lookup describes it, where two absent
// values are equal. A store that can't match these exactly for some comparison skips it.
type PushComparison struct {
	Op          string
	Left, Right PushArg
}
