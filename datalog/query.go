package datalog

import "github.com/panyam/jaala/ns"

// A Query is a datalog program answered over a fact base: Rules define derived (IDB) relations and
// Goal is the conjunction to solve. Goal's free variables — narrowed by Select — are the answer
// columns. EDB relations come from the Base's Source; IDB relations come from Rules; predicates are
// computed (see ns.Vocabulary.AddPredicate).
//
// The whole IR is what Parse produces. For the query text
//
//	component.mpn(?r,?m), net.max_voltage(?n,?v), ?v < 30 => ?r, ?n
//
// the Query is: no Rules; Goal.Literals = [ Atom component.mpn(?r,?m), Atom net.max_voltage(?n,?v),
// Compare(?v < 30) ]; Select = [?r, ?n].
type Query struct {
	Rules []Rule
	Goal  Body
	// Select is the answer columns: each item is a variable (a group key) or an aggregate over the
	// group formed by the variable columns (count/min/max/sum/list). Empty selects Goal's variables
	// in first-seen order. When any item is an aggregate, the result is grouped-and-reduced.
	Select []Term
	// Having filters the GROUPS a Select's aggregates form, after the reduce. Each item is a Compare
	// whose left term is an aggregate, so `=> ?p, count(?n) having count(?n) < 2` keeps the groups of
	// size one. A Goal comparison cannot express this: it is applied per binding, before there is a
	// group to count.
	//
	// An aggregate may appear here without being selected, which is how you ask for the subjects
	// rather than the tally. That still groups: a Having aggregate makes the query grouped-and-reduced
	// exactly as a Select one does, and its column is computed, filtered on, and then not printed
	// (Columns is keyed off Select alone).
	Having []Compare
	// OrderBy sorts the answer by answer columns, after the reduce and the having filter. Each item
	// names a Select column (a variable or an aggregate, as written there). Rows that tie on every
	// item keep the default order, so the answer stays deterministic. Empty keeps the default order
	// alone: absent values first, then numbers by value, then other strings by their text.
	OrderBy []Order
	// After keeps only the rows strictly after these values in the OrderBy columns, one value per
	// column, so a page continues from the last row of the one before it by value (#194). It needs
	// an OrderBy naming every variable column. The After option sets it from a row.
	After []ns.Value
	// Limit keeps at most this many rows of the ordered answer, after skipping Offset of them. Zero
	// means no limit. With an OrderBy the goal holds only Limit plus Offset rows while it is solved;
	// without one it answers the first Limit distinct rows it finds and stops there, so which rows
	// depends on the evaluator. Offset needs an OrderBy, since a page has to continue from the one
	// before it in a stable order.
	Limit  int
	Offset int
}

// An Order is one `order by` item: an answer column, ascending unless Desc. Descending reverses the
// whole order, so absent values come last.
type Order struct {
	Term Term
	Desc bool
}

// A Rule derives its Head for every binding satisfying Body. A rule is recursive when its Head
// relation is reachable from a Body atom in the rule dependency graph; recursion terminates by
// finiteness of the fact base (no function symbols). Rules materialize by stratified fixpoint
// (see Naive and SemiNaive): a program that reads a relation under negation from inside that relation's own
// recursive cycle is rejected, which is what keeps `not` well-defined.
//
// Example (transitive closure over a derived edge): connected(?a,?c) :- connected(?a,?b), link(?b,?c)
// is Rule{Head: Atom connected(?a,?c), Body: [Atom connected(?a,?b), Atom link(?b,?c)]}.
//
// A head may aggregate (#4): degree(?n, count(distinct ?m)) :- edge(?n, ?m) derives one tuple per
// group of the head's plain variables, reducing its body's bindings as an answer's aggregate does
// (see Aggregate), so a rule can count. It is evaluated once every relation its body reads is
// complete, which stratification arranges as it does for negation, so a relation can't aggregate
// over its own recursion. Such a rule must be its relation's only rule. As in an answer, a group
// exists only where the body has a binding: a part with no pins gets no count of zero, and a head
// with no plain variable gets one tuple even over nothing.
type Rule struct {
	Head Atom
	Body Body
	Hops int // 0 = run to fixpoint; >0 = bound recursion depth (reserved; the fixpoint is finite regardless)
	// HeadTypes are the types the rule declares for its head's arguments, by position, written
	// `has_test_point(?n: net)`. Empty, or a zero entry, declares nothing for that argument, which is
	// then inferred (see ns.Vocabulary.Lookup). KindFrom and Owner name other head VARIABLES, without "?".
	HeadTypes []ns.ArgType
	// text is the rule as written, set for a witnessed Eval before any rewrite, so a derived node can
	// name the rule its author wrote rather than its planned or adorned form.
	text string
}

// A Body is an implicit conjunction (AND) of Literals. Disjunction (OR) is several Rules sharing
// one Head relation — never an OR node — so the program stays stratifiable. Example: the body of
// `a(?x), b(?x,?y), ?y < 5` is Body{Literals: [Atom a, Atom b, Compare]}.
type Body struct{ Literals []Literal }

// A Literal is exactly one of: a positive atom, a negated atom (stratified negation), a
// comparison built-in, or an aggregate over a body of its own. Exactly one field is non-nil.
// Examples: `param(?m,"VIN",?v)` is Literal{Pos: &Atom{...}}; `not param(?m,"VIN",?v)` is
// Literal{Neg: ...}; `?v < 30` is Literal{Compare: ...}; `?c = count(?n) : { pin(?r, ?n, _) }` is
// Literal{Agg: ...}.
type Literal struct {
	Pos     *Atom
	Neg     *Atom
	Compare *Compare
	Agg     *BodyAggregate
	// Opt is `optional { body }` (#282): the clause's row holds whether body matches or not, and a
	// variable only body binds is absent when it doesn't. lowerOptionals rewrites it into rules before
	// anything else reads the program, so nothing after it sees one.
	Opt *Body
	// at is the literal's written position in its body (1-based), set for a witnessed Eval before any
	// rewrite; 0 marks a literal a rewrite added. See Witness.
	at int
	// computed marks a call lowerExpressions wrote for an expression, which waits until one of its
	// function's modes is satisfied (deferComparisons) and can't feed recursion.
	computed bool
}

// A BodyAggregate binds Result to an aggregate over the bindings of Body, reduced once per value of
// the variables Body shares with the rest of the clause (#71): `part(?r), ?c = count(?n) : { pin(?r,
// ?n, _) }` counts each part's pins. Every value the rest of the clause gives those variables gets a
// row, so a part with no pins counts 0. Over no bindings count and sum are 0, min and max absent and
// list empty, as an aggregate over nothing answers in a projection (#122).
//
// Body reads only relations complete before the clause's own, as a rule head aggregate does, and a
// shared variable has to be bound outside the braces by a positive relation and inside them by one.
type BodyAggregate struct {
	Result Var
	Agg    Aggregate
	Body   Body
}

// An Atom applies a relation to argument terms: Relation(Args...). Relation is an EDB name
// a Source serves, a computed predicate, or an IDB name a Rule defines. Example: `component.mpn(?r,?m)`
// is Atom{Relation: "component.mpn", Args: [Var("r"), Var("m")]}.
type Atom struct {
	Relation string
	Args     []Term
	// Fact is the variable `edge(?a, ?b) as ?r` binds to the fact the atom matched (#279): the
	// Source's Tuple.ID, or the relation and values when it gives none. Only a positive atom over a
	// base relation binds one, and lowerFacts rewrites it into a call to the relation's fact relation
	// before any check reads the program, so nothing after it sees the field set.
	Fact Var
}

// A Compare is a built-in predicate over two terms, evaluated once both are bound: Left Op Right,
// Op in {<, <=, =, !=, >, >=}. Numeric when both bound values carry a number, string otherwise.
type Compare struct {
	Left  Term
	Op    string
	Right Term
}

// A Term is a variable, a constant, or an aggregate over the group formed by the projection's plain
// variables. Exactly one of Var/Const/Agg is set (Var == "" means not a variable). An Agg is legal in
// a Select column, on the left of a Having, and in a rule head, which groups by the head's plain
// variables (see Rule); in a body literal there is no group for it to reduce.
type Term struct {
	Var   Var
	Const *ns.Value
	Agg   *Aggregate
	// Expr is arithmetic, or a call to a function relation, legal as a comparison's operand (#5).
	Expr *Expr
}

// Var is a logic variable name (the leading "?" is stripped at parse time).
type Var string

// An Aggregate reduces Var over each group of the projection's plain-variable columns. Example:
// `component-on-net(?ref,?net) => ?net, count(?ref)` groups by ?net and counts the ?ref bindings
// per group (parts per net); Aggregate{Func: "count", Var: "ref"}. min/max/sum reduce Var's numeric
// value over the group, and list joins its values.
//
// EVERY AGGREGATE REDUCES BINDINGS, NOT VALUES, unless Distinct is set. A binding is the unit the
// whole evaluator deals in, so a group holds one row per solution and a goal that binds anything Var
// does not determine repeats Var once per combination. On a net carrying 7 test points and 20
// capacitors, `component.class(?tp,"test_point"), component-on-net(?tp,?net),
// component.class(?c,"capacitor"), component-on-net(?c,?net) => ?net, count(?tp)` reports 140.
//
// Distinct reduces the SET of Var's values instead: count(distinct ?tp) is 7 on that net, and
// list(distinct ?tp) names those 7 once each. It is uniform across every function rather than
// special-cased on count, deliberately. Making list implicitly distinct would put count(?tp) and
// list(?tp) in one projection disagreeing about what the group holds, 140 against 7, with nothing in
// the query saying why — which is this same trap one function over, and harder to see because both
// columns look right on a group of size one.
//
// A derived relation is the other way to get a distinct reduce, by projecting the extra variable away
// before the group forms:
//
//	has_tp(?n) :- component-on-net(?x,?n), component.class(?x,"test_point");
//	component.class(?p,"capacitor"), component-on-net(?p,?n), has_tp(?n) => ?p, count(?n)
//
// The rule makes has_tp a SET of nets, so count(?n) counts nets whether or not Distinct is set.
type Aggregate struct {
	Func string // count | min | max | sum | list
	Var  Var
	// Distinct reduces Var's distinct values rather than one entry per binding. Spelled
	// `count(distinct ?x)`. Bare aggregates keep their binding-wise meaning, so no existing query
	// moves.
	Distinct bool
}

// Row is one answer: the projected variables bound to values, plus the provenance of the base
// facts that produced it — so an answer stays verifiable.
type Row struct {
	Bind  map[Var]ns.Value
	Cites []string
	// Witness says how the row holds, one node per goal literal in written order, when the Eval asked
	// for witnesses (see Witnesses). It is nil otherwise, and on an aggregate row.
	Witness []*Witness
	// Why says why an aggregate column is absent, keyed like Bind: "no rows" (a group over nothing),
	// "no numbers" (min, max or avg over text) or "mixed units: A, V" (min, max, sum or avg over numbers in
	// different units, #80). A host shows it beside the absent value. It is nil when no column is
	// absent, and an aggregate a rule stores keeps only its value, so it's set on a goal's own
	// aggregates.
	Why map[Var]string
	// Versions are the versions the row holds in, when an Across Eval answered it (#180), else nil. Its
	// Cites are then its citations in the latest of them, and CitesAt gives another's.
	Versions ns.VersionSet
	// across and raw are, for a row an Across Eval answered, its Source and its citations as made,
	// which CitesAt resolves.
	across *acrossSource
	raw    []string
}
