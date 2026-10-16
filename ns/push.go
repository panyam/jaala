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
// A group bound partway through a body carries the variables bound before it as Inputs (#246). A
// group is at least two literals, or one with nothing bound and no constant that the store filters
// better than a whole read: a variable repeated in it, or a comparison going with it (#263). A literal
// with a constant or a bound variable is a Lookup.
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
	// engine does (see PushComparison), or any filter (see PushFilter), and leave it to the engine. A
	// row repeated is two
	// derivations, as a fact a Source serves twice is two facts, so a row is never sent twice.
	//
	// Each row is charged to the Eval's Budget as it arrives, so the engine may stop ranging early
	// (the budget is spent or the query's context ends) and the sequence should then release what
	// it holds. An error yielded stops the push and fails the query, wrapped with the conjunction.
	// What a completed push returned is kept as a Lookup's is (see LookupSource); one that stopped
	// is not kept.
	Push(ctx context.Context, c Conjunction) iter.Seq2[[]Tuple, error]
}

// FilterSource is optionally implemented by a PushSource that applies filter predicates to a
// conjunction's rows (#297). The planner sends a filter in Conjunction.Filter, and grows a group past
// it, only when PushesFilter names its predicate: a store that ignored a filter it was sent would
// still answer correctly, but it would join every row the filter drops, so a store that doesn't
// implement this is sent none. Like Pushes, it is asked while planning and must be cheap.
type FilterSource interface {
	// PushesFilter reports whether the store applies the predicate registered under name exactly as
	// the vocabulary does, as stdlib's str.glob has SQLite GLOB's semantics.
	PushesFilter(name string) bool
}

// A Conjunction is a group of base literals a PushSource evaluates together, and the comparisons over
// their variables.
type Conjunction struct {
	Atoms   []PushAtom
	Compare []PushComparison
	// Filter is the filter predicates over the conjunction's variables (#297), each one the store
	// names in FilterSource, which it applies as it does Compare, only to whole rows. The planner offers a group with its comparisons and
	// filters, then without the filters, then without either, so a store refusing filters in Pushes is
	// still asked the rest.
	Filter []PushFilter
	// Partial asks for every prefix of each match too: besides the rows satisfying every atom, for
	// each k from 1 below len(Atoms), one row of k tuples per combination of facts satisfying the
	// first k atoms (comparisons apply only to whole rows). A Federation over partitioned members
	// (NewPartitionedFederation) asks it, continuing a prefix in the members holding the facts the
	// next atom needs, so a join whose facts span members is still answered whole. A store that
	// can't return prefixes refuses a Partial conjunction in Pushes, and is then asked literal by
	// literal; one that returns only whole rows loses the matches that span members.
	Partial bool
	// PrefixesOnly, with Partial, asks for the prefixes alone, not the whole matches: a partitioned
	// Federation summarizing or paging a join across members (#253) has each member's whole matches
	// from its own summary or page, and needs only where each match leaves the member. A store that
	// takes Partial takes this too.
	PrefixesOnly bool
	// Page asks for only the matches behind the first rows of a goal's answer (#241); nil asks for
	// every match.
	Page *Page
	// Inputs are variables an earlier part of the body binds (#246), and Bindings their values, one
	// set per binding in Inputs' order: Push answers c once for each, returning every match whose
	// Inputs equal one of the Bindings, compared as Lookup's values are, in one call, as LookupBatch
	// answers a lookup for many values. A match for no binding may come back, since the engine reads
	// each row's inputs and files it under its binding, but a binding's match left out is lost. The
	// planner offers c with Inputs and no Bindings to Pushes; a store that declines is asked literal by
	// literal, as before.
	//
	// With Partial too, as a partitioned Federation asks (#268), a prefix row is one whose atoms agree
	// with some binding on the Inputs they bind: an input only a later atom reads can't filter it. A
	// store that pushes c with Inputs also pushes its suffixes with the Inputs they still read.
	Inputs   []int
	Bindings [][]Value
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
	// After, when set, has one value per Order key: the page starts after them (#254), its rows being
	// those whose values at Order come strictly after After in that order, each key compared in the
	// engine's order and reversed by Desc, as the query's after clause keeps them. The store's first
	// Limit rows are then its first after the cursor.
	After []Value
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
// of bindings needs neither, so a goal counting with no group asks for one row. Extremes are variables
// only min and max read (#252): rather than group by their values, a row reports, per unit, the
// smallest and largest of its matches' numbers there (see Extreme), so a min over a column of distinct
// measurements is a row per group, not per value. A store that can't asks to be sent them as Values,
// by refusing a Summary with Extremes in Summarizes.
type Summary struct {
	Group    []int
	Values   []int
	Extremes []int
}

// An Extreme is a summary row's numbers in one unit at one of its Extremes variables, each value read
// as its argument's declared type: Unit is their unit ("" for none), Numbers how many matches hold a
// number there, NaN whether one of those is NaN, and Min and Max the smallest and largest of the rest
// by value, absent when every one is NaN. A match whose value isn't a number (text, absent) counts in
// Matches only. A row has one Extreme per unit its numbers are in; a store may report a unit in
// several, and the engine combines them.
type Extreme struct {
	Unit     string
	Numbers  int
	NaN      bool
	Min, Max Value
}

// A SummaryRow is one row of a Summary: the values at its Group and Values variables, in their order,
// the number of matches with them, those matches' facts' citations, and per Extremes variable, in
// order, its numbers' extremes by unit.
type SummaryRow struct {
	Group    []Value
	Values   []Value
	Matches  int
	Cites    []string
	Extremes [][]Extreme
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

// A PushFilter is a filter predicate the conjunction's rows must pass: Predicate is its name as the
// vocabulary registered it (str.glob, str.match), and Args one per argument, each a variable of the
// Conjunction or a constant, never a wildcard. A store may apply one only when it answers exactly as
// the predicate does, and otherwise skips it: a row the filter would drop may come back, since the
// engine checks every row again, but a row it keeps must not be left out. stdlib's str.glob has SQLite
// GLOB's semantics (whole string, case-sensitive), so a SQLite store can apply it as GLOB.
type PushFilter struct {
	Predicate string
	Args      []PushArg
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
