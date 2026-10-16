// Package datalog is a Datalog engine for graph-shaped data: a small declarative query language
// whose every answer carries the provenance of the facts that produced it.
//
// It supports conjunction, comparison, stratified negation, recursion to a fixpoint, aggregation
// with `having` and `distinct` in the answer and in a rule head (stratified, as negation is), `order
// by`, `limit` and `offset`, and computed predicates. A Query is
// the whole program: rules that define derived relations, a goal to solve, and the columns to answer
// with. An answer is sorted, absent values first, then numbers by value, then text, unless `order by`
// says otherwise. Parse reads the text
// syntax; the builders (V, Rel, Pos, Def, Build, ...) construct the same Query in code.
//
// The engine knows nothing about any domain. Names come from an ns.Vocabulary, the namespace tree a
// host composes from base relations, predicates and derived modules (see package ns). This package
// provides Language, which lets a vocabulary hold modules written in Datalog, and NewBase, which pairs
// a vocabulary with one Source's facts for querying. A host builds and checks its vocabulary once and
// builds a Base per dataset; every Base over a vocabulary shares its resolved modules.
//
// A query naming a derived relation pulls in the module that defines it, and whatever that module
// reads, when it is linked (Link, which Eval does first). A linked query is an ordinary query: the
// evaluator never sees a module. Inside a module's text a bare name is that module's member first and
// the root's second, and a member starting with "_" is private to its module.
//
// A derived relation may declare its arguments' types in a rule head, `has_test_point(?n: net) :- ...`,
// and what it leaves undeclared is inferred through its rules, following agni's column typing.
// ColumnKinds carries the kinds through to a query's answer columns. A constant, or a bound value, is
// read as the type of the argument it stands in, or of the number it is compared with, and refused
// when it cannot be, so a question asked with the wrong type fails rather than answering empty.
//
// A Base caches and indexes what it reads, and keeps the derived relations SemiNaive evaluated in
// full for later queries (see Base.LimitDerivedCache, Base.Forget and ns.Versioned), so one Base
// serves many queries, concurrently, and an Evaluator answers a Query over it. A Source that
// implements ns.LookupSource is asked for the facts a call with bound arguments needs, rather than
// read whole, and what it returns is kept for that one Eval. SemiNaive is the one to run: it derives recursive rules
// semi-naively, inlines single-rule non-recursive relations so a bound argument reaches the literals
// that can use it, derives a relation called with bound arguments only for the values demanded
// (magic sets, with right-linear recursion from a constant start reduced to the set of nodes it
// visits), and plans each body, so a literal runs once as much as possible is bound and a generator
// once one of its Modes is satisfied. Each round of the fixpoint starts from its delta when that is
// the smaller side. Naive is the reference: written order, naive
// fixpoint, kept simple so every faster strategy can be tested against it. Every Eval takes a
// context, which reaches the host's generators and cancellable Sources, and options: Bind gives goal
// variables values from the host (one, or a set the variable ranges over), Budget limits the work one Eval may do, and Witnesses records how
// each answer was derived (Row.Witness), as the tree of rules and facts behind it.
//
// A Source that implements ns.TimedSource serves several versions of its facts, each fact once with
// the versions it holds in, and At(v) answers at one of them as a Base over only that version's facts
// would, while Base.Diff lists a relation's facts gained and lost between two.
//
// Evaluation is guaranteed to terminate because no rule can invent a value: every answer is built
// from constants in the facts and values a host's generators draw from finite data. That guarantee
// is why the value model is scalar for now.
//
// The engine was extracted from agni (github.com/panyam/agni), an EDA tool that queries circuit
// designs, and the examples in these comments use its vocabulary: component-on-net(ref, net) places
// a part on a net, reaches(from, to) walks a circuit's series connections, and so on. References to
// "agni issue N" point at the history behind a rule.
//
// The package imports only the standard library and jaala/ns, and builds for GOOS=js GOARCH=wasm.
package datalog
