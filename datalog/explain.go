package datalog

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/panyam/jaala/ns"
)

// Explain asks an Eval to fill r with what it did (#147): the plan each body ran in, where its work
// went, which derived relations were evaluated in full, under demand or read from the Base, and how
// each base relation was read. A query that is slow then shows which rule, literal or relation spent
// the work, rather than one total to bisect by hand.
//
// r is reset when the Eval starts and filled when it ends, whether or not it succeeds, so a query
// stopped by its Budget still reports where its work went. A Report is plain data, with JSON tags,
// and String renders it as text. Explaining costs time and memory in proportion to the rules and
// literals a query runs, not its tuples, and an Eval that doesn't ask for it pays one nil check per
// unit of work.
func Explain(r *Report) Option {
	return func(o *evalOptions) { o.explain = r }
}

// A Report is what Explain fills: the totals, then the goal and each rule body as they ran, the derived
// relations, and the base relations read. Work is Base.Work's unit (see Budget); times are wall clock.
type Report struct {
	Work    int64         `json:"work"`
	Budget  int64         `json:"budget,omitempty"`
	Elapsed time.Duration `json:"elapsed_ns"`
	Rows    int           `json:"rows"`
	// Held is the most answer rows (or groups, for an aggregate) the goal held at once, before
	// offset and limit, which is what the answer's memory grows with rather than the bindings the goal
	// found (#120). A limit with an order by holds at most limit plus offset.
	Held  int    `json:"held"`
	Error string `json:"error,omitempty"`
	// Lookups are the calls the Eval made to the Source's Lookup (ns.LookupSource), Batches those to
	// its LookupBatch (ns.BatchLookupSource, #153), so the round trips are the two together, and
	// Fetched the tuples the Source returned to this Eval: those calls', and those of each relation it
	// read whole that the Base didn't already hold.
	Lookups int64 `json:"lookups,omitempty"`
	Batches int64 `json:"batches,omitempty"`
	Fetched int64 `json:"fetched,omitempty"`
	// Pushes are the calls to the Source's Push (ns.PushSource, #237), each a conjunction of several
	// relations and another round trip; Fetched counts the tuples of the rows they returned.
	Pushes int64 `json:"pushes,omitempty"`
	// Walks are the calls to the Source's Walk (ns.WalkSource, #239), each answering every round of a
	// recursive relation's probes of one relation; Fetched counts the facts they returned.
	Walks int64 `json:"walks,omitempty"`
	// Cold is what the Eval would have cost on a Base that kept nothing from earlier queries. Unset
	// when the Eval failed.
	Cold *ColdReport `json:"cold,omitempty"`
	// Goal is the goal's body as it ran, after any rewrite reordered it.
	Goal *BodyReport `json:"goal,omitempty"`
	// Rules are the rule bodies that ran, in the order they first did. A rewrite can run one rule as
	// several bodies: under demand, each with a guard; in a recursive relation, each round's variant
	// reading what the last round derived.
	Rules []*BodyReport `json:"rules,omitempty"`
	// Relations are the derived relations the query reached, by the names the program gives them.
	Relations []*RelationReport `json:"relations,omitempty"`
	// Sources are the base relations the Eval read, by name.
	Sources []*SourceReport `json:"sources,omitempty"`
}

// A ColdReport is an Eval's cost with nothing kept from earlier queries: no derived relation reused
// (#140), and every relation it read whole counted as fetched. Unlike Report's Work, which depends on
// what ran on the Base before, it is the same for one query over one Base however often it runs, so
// it is the number to compare queries or plans by, or to suggest a budget from. When the Eval did reuse
// something (or the Base held whole a relation the Source would otherwise have been asked to look up),
// Explain finds it by running the query again on a copy of the Base that keeps nothing, which doesn't
// count toward Base.Work but does read the Source again; otherwise it is Report's own numbers.
type ColdReport struct {
	Work    int64 `json:"work"`
	Lookups int64 `json:"lookups,omitempty"`
	Batches int64 `json:"batches,omitempty"`
	Fetched int64 `json:"fetched,omitempty"`
	Pushes  int64 `json:"pushes,omitempty"`
	Walks   int64 `json:"walks,omitempty"`
	// Error is why the cold run stopped, such as the Eval's Budget, when it did.
	Error string `json:"error,omitempty"`
}

// A BodyReport is one body as it ran: a rule's, or the goal's.
type BodyReport struct {
	// Rule is the rule as written, when this body came from one; a body the demand rewrite added has
	// none. Ran is the body as evaluated, after inlining, demand and planning, with the names those
	// rewrites give their relations made readable (see ExplainName).
	Rule string `json:"rule,omitempty"`
	Ran  string `json:"ran"`
	// Relation is the relation the body derives tuples of, by its written name; "" for the goal and
	// for a body that only stores a demanded prefix.
	Relation string `json:"relation,omitempty"`
	Runs     int    `json:"runs"`             // times the body was solved
	Tuples   int    `json:"tuples,omitempty"` // tuples it added that were new
	// Work is its literals' work, and what storing the tuples it derived cost.
	Work     int64            `json:"work"`
	Elapsed  time.Duration    `json:"elapsed_ns"`
	Literals []*LiteralReport `json:"literals"`
}

// A LiteralReport is one literal of a body, in the order it ran: positive literals first, as solved,
// then the negated ones, which filter each solution.
type LiteralReport struct {
	Literal string `json:"literal"`
	// Access says how it was read, each way it was: "index (from)" for a base relation probed on its
	// from argument, "scan", "derived index (to)", "derived scan", "generator", "filter",
	// "comparison", "lookup (from)" for a call the Source looked up (ns.LookupSource), or "not" over
	// one of those.
	Access []string `json:"access,omitempty"`
	// Reached is how often solving got to it, Passed how many bindings it passed on (for a `not`,
	// the ones it let through), and Work the candidates it examined.
	Reached int64 `json:"reached"`
	Passed  int64 `json:"passed"`
	Work    int64 `json:"work"`
}

// A RelationReport is one derived relation the query reached.
type RelationReport struct {
	Relation string `json:"relation"`
	// How is "full" (every tuple derived), "demand" (only those the query's bound arguments asked for;
	// Adornments says which arguments were bound, b, or free, f), "factored" (a right-linear
	// recursion walked from the goal's constants), "reused" (read from what the Base kept from an
	// earlier query, #140), or "inlined" (its body copied into its callers, so it was never derived).
	How        string        `json:"how"`
	Adornments []string      `json:"adornments,omitempty"`
	Demanded   int           `json:"demanded,omitempty"` // values demanded, summed over adornments
	Tuples     int           `json:"tuples"`
	Rounds     int           `json:"rounds,omitempty"`
	Work       int64         `json:"work"` // its bodies' work, and the demand rules feeding it
	Elapsed    time.Duration `json:"elapsed_ns"`
}

// A SourceReport is one base relation the Eval read.
type SourceReport struct {
	Relation string `json:"relation"`
	// Tuples is the relation's size when the Eval read it whole, and 0 when it only looked it up.
	Tuples int `json:"tuples"`
	// Cached is true when the Base already held the relation's tuples from an earlier query, false
	// when this Eval read them from the Source.
	Cached  bool           `json:"cached"`
	Scans   int64          `json:"scans,omitempty"` // reads of every tuple
	Indexes []*IndexReport `json:"indexes,omitempty"`
	// Lookups are the calls made to the Source's Lookup for it, Batches those to its LookupBatch,
	// Fetched the tuples they returned, Hits the calls this Eval answered from what its own earlier
	// lookup or batch of the same values returned, and Kept those it answered from what an earlier
	// Eval's did, kept by the Base (#212).
	Lookups int64 `json:"lookups,omitempty"`
	Batches int64 `json:"batches,omitempty"`
	Fetched int64 `json:"fetched,omitempty"`
	Hits    int64 `json:"hits,omitempty"`
	Kept    int64 `json:"kept,omitempty"`
	// Pushes are the pushed conjunctions it was part of (#237); a conjunction answered from what was
	// kept counts in Hits or Kept as a lookup does, and its tuples of their rows in Fetched.
	Pushes int64 `json:"pushes,omitempty"`
	// Walks are the walks that fetched its facts (#239).
	Walks int64 `json:"walks,omitempty"`
}

// An IndexReport is one index of a base relation the Eval probed.
type IndexReport struct {
	On     []string `json:"on"`     // the arguments it is keyed on, by label
	Built  bool     `json:"built"`  // built by this Eval, rather than kept by the Base
	Probes int64    `json:"probes"` // lookups made in it
}

// explainer is an Eval's Explain state, on its run: the report it fills, and the body and literal the
// work being counted belongs to.
type explainer struct {
	reg     *ns.Vocabulary // for the order solve runs a body in (deferComparisons)
	report  *Report
	start   time.Time
	bodies  map[string]*BodyReport
	order   []*BodyReport
	body    *BodyReport
	cur     *LiteralReport
	rounds  map[string]int
	sources map[string]*SourceReport
	linked  Query           // the program before any rewrite, to name what it reached
	held    map[string]bool // relations read from the Base's kept ones
	// cachedTuples and cachedIndexes are what the Base held when the Eval began, so what the Eval
	// reads or builds itself, while planning or solving, shows as its own.
	cachedTuples  map[string]bool
	cachedIndexes map[idxKey]bool
	// pushes counts the calls to the Source's Push, which each name several relations (#237).
	pushes int64
	// walks counts the calls to the Source's Walk (#239).
	walks int64
	// quiet is set while prefetch runs a body's prefix again, whose literals were reached already: its
	// work still counts against the body, but no literal counts it as reached or passed.
	quiet bool
}

func newExplainer(r *Report, b *Base) *explainer {
	*r = Report{}
	e := &explainer{report: r, reg: b.reg, start: time.Now(), bodies: map[string]*BodyReport{}, rounds: map[string]int{}, sources: map[string]*SourceReport{}}
	if b.edb != nil {
		e.cachedTuples, e.cachedIndexes = b.edb.held()
	}
	return e
}

// count attributes one unit of work to the body and literal being solved.
func (e *explainer) count() {
	if e.cur != nil {
		e.cur.Work++
	}
	if e.body != nil {
		e.body.Work++
	}
}

// enter makes the body of r, or the goal when r is nil, the one work is counted against, until the
// returned function restores the one before.
func (e *explainer) enter(r *Rule, goal Body) func() {
	key, written, relation := "goal: "+goal.String(), "", ""
	body := goal
	if r != nil {
		key, written, body = r.String(), ExplainName(r.text), r.Body
		if len(r.Body.Literals) == 0 {
			key = r.Head.String() // a fact, such as the demand a goal's constants make
		}
		relation = explainRelation(r.Head.Relation)
	}
	br, ok := e.bodies[key]
	if !ok {
		pos, negs := splitNegations(body.Literals)
		pos = deferComparisons(pos, e.reg) // the order solve runs them in, which literal(i) indexes
		br = &BodyReport{Rule: written, Ran: ExplainName(key), Relation: relation}
		if r == nil {
			br.Ran = ExplainName(goal.String())
		}
		for _, l := range append(pos, negs...) {
			br.Literals = append(br.Literals, &LiteralReport{Literal: ExplainName(l.String())})
		}
		e.bodies[key] = br
		if r == nil {
			e.report.Goal = br
		} else {
			e.order = append(e.order, br)
		}
	}
	br.Runs++
	prevBody, prevCur, began := e.body, e.cur, time.Now()
	e.body, e.cur = br, nil
	return func() {
		br.Elapsed += time.Since(began)
		e.body, e.cur = prevBody, prevCur
	}
}

// literal makes the body's literal at i (solving order: positive literals, then negated ones) the one
// work is counted against, and returns it with the one it replaces.
func (e *explainer) literal(i int) (lr, prev *LiteralReport) {
	if e.body == nil || i >= len(e.body.Literals) {
		return nil, e.cur
	}
	lr, prev = e.body.Literals[i], e.cur
	lr.Reached++
	e.cur = lr
	return lr, prev
}

// at runs f with literal i of the current body as the one its work and access count against, when e
// explains an Eval.
func (e *explainer) at(i int, f func() error) error {
	if e == nil || e.body == nil || i >= len(e.body.Literals) {
		return f()
	}
	prev := e.cur
	e.cur = e.body.Literals[i]
	defer func() { e.cur = prev }()
	return f()
}

// access records how the current literal was read, once per way.
func (e *explainer) access(how string) {
	if e.cur == nil {
		return
	}
	if e.cur.Literal != "" && strings.HasPrefix(e.cur.Literal, "not ") && !strings.HasPrefix(how, "not ") {
		how = "not " + how
	}
	for _, a := range e.cur.Access {
		if a == how {
			return
		}
	}
	e.cur.Access = append(e.cur.Access, how)
}

// readSource records a base relation's read.
func (e *explainer) readSource(rel string, tuples int, cached bool) *SourceReport {
	s, ok := e.sources[rel]
	if !ok {
		s = &SourceReport{Relation: rel, Tuples: tuples, Cached: cached}
		e.sources[rel] = s
	}
	return s
}

// probe records a lookup in one of rel's indexes, keyed on the arguments on.
func (e *explainer) probe(rel string, on []string, built bool) {
	s := e.sources[rel]
	if s == nil {
		return
	}
	k := strings.Join(on, ",")
	for _, x := range s.Indexes {
		if strings.Join(x.On, ",") == k {
			x.Probes++
			x.Built = x.Built || built
			return
		}
	}
	s.Indexes = append(s.Indexes, &IndexReport{On: on, Built: built, Probes: 1})
}

// finish fills the report's totals and relations once the Eval ends.
func (e *explainer) finish(b *Base, rows int, err error, budget int64) {
	r := e.report
	r.Elapsed = time.Since(e.start)
	if b.run != nil {
		r.Work = b.run.used
	}
	r.Budget = budget
	r.Rows = rows
	if b.run != nil {
		r.Held = b.run.held
	}
	if err != nil {
		r.Error = err.Error()
	}
	r.Rules = e.order
	var cold int64
	for _, rel := range sortedKeys(e.sources) {
		s := e.sources[rel]
		r.Sources = append(r.Sources, s)
		r.Lookups += s.Lookups
		r.Batches += s.Batches
		r.Fetched += s.Fetched
		cold += s.Fetched + int64(s.Tuples)
		if !s.Cached {
			r.Fetched += int64(s.Tuples)
		}
	}
	r.Pushes, r.Walks = e.pushes, e.walks
	r.Relations = e.relations(b)
	if err == nil {
		r.Cold = &ColdReport{Work: r.Work, Lookups: r.Lookups, Batches: r.Batches, Fetched: cold, Pushes: r.Pushes, Walks: r.Walks}
	}
}

// warm reports whether the Eval's cost depended on what the Base kept from earlier queries in a way
// its own numbers can't undo: it reused a derived relation, answered a lookup from an earlier query's,
// or probed the index of a relation the Base held whole, where a fresh Base would have asked the
// Source to look the call up.
func (e *explainer) warm(b *Base) bool {
	if len(e.held) > 0 {
		return true
	}
	if b.looker == nil && b.pusher == nil && b.walker == nil {
		return false
	}
	for _, s := range e.sources {
		if s.Kept > 0 || (s.Cached && len(s.Indexes) > 0 && b.lookable(s.Relation)) {
			return true
		}
	}
	return false
}

// coldCost evaluates q again on a copy of b that keeps nothing: no derived relations, and when the
// Source looks relations up, no base relations either. It doesn't count toward b's Work.
func coldCost(ctx context.Context, q Query, b *Base, opts []Option, rewrite func(*Base, Query) Query, fixpoint func(*Base, []Rule) error) *ColdReport {
	cold := *b
	cold.derived, cold.work, cold.lookups = nil, nil, nil
	if b.looker != nil {
		cold.edb = newEDBCache()
	}
	var r Report
	_, err := evaluate(ctx, q, &cold, append(opts[:len(opts):len(opts)], Explain(&r), func(o *evalOptions) { o.cold = true }), rewrite, fixpoint)
	if err != nil {
		return &ColdReport{Work: r.Work, Lookups: r.Lookups, Batches: r.Batches, Error: err.Error()}
	}
	return r.Cold
}

// relations reports each derived relation the goal reached, from the program as linked and what the
// Eval derived.
func (e *explainer) relations(b *Base) []*RelationReport {
	// What a reused relation read isn't needed, so it is left out rather than called inlined.
	needed := e.linked
	needed.Rules = nil
	for _, r := range e.linked.Rules {
		if !e.held[r.Head.Relation] {
			needed.Rules = append(needed.Rules, r)
		}
	}
	for _, r := range e.linked.Rules {
		if e.held[r.Head.Relation] {
			needed.Rules = append(needed.Rules, Rule{Head: r.Head})
		}
	}
	reach := reached(needed)
	byRel := map[string]*RelationReport{}
	var names []string
	for _, rule := range e.linked.Rules {
		rel := rule.Head.Relation
		if !reach[rel] || strings.Contains(rel, "\x00") {
			continue
		}
		shown := displayName(rel)
		if _, ok := byRel[shown]; !ok {
			byRel[shown] = &RelationReport{Relation: shown}
			names = append(names, shown)
		}
	}
	adorns := map[string]map[string]bool{}
	for name, tuples := range b.idb {
		if strings.Contains(name, deltaSep) {
			continue
		}
		shown := explainRelation(name)
		rr := byRel[shown]
		if rr == nil {
			continue
		}
		switch {
		case isMagic(name):
			rr.Demanded += len(tuples)
			continue
		case strings.Contains(name, fromSep):
			rr.How = "factored"
			continue
		case strings.Contains(name, "\x00answer"):
			rr.How = "factored"
		case strings.Contains(name, "\x00/"):
			if rr.How == "" {
				rr.How = "demand"
			}
			if adorns[shown] == nil {
				adorns[shown] = map[string]bool{}
			}
			adorns[shown][name[strings.Index(name, "\x00/")+2:]] = true
		default:
			if e.held[name] {
				rr.How = "reused"
				rr.Tuples += len(tuples)
				continue
			} else if rr.How == "" || rr.How == "demand" {
				rr.How = "full"
			}
		}
		rr.Tuples += len(tuples)
		rr.Rounds = max(rr.Rounds, e.rounds[name])
	}
	for _, br := range e.order {
		if rr := byRel[br.Relation]; rr != nil {
			rr.Work += br.Work
			rr.Elapsed += br.Elapsed
		}
	}
	sort.Strings(names)
	out := make([]*RelationReport, 0, len(names))
	for _, n := range names {
		rr := byRel[n]
		if rr.How == "" {
			rr.How = "inlined"
		}
		rr.Adornments = sortedKeys(adorns[n])
		out = append(out, rr)
	}
	return out
}

// explainRelation is the written relation a rewritten one derives tuples of: an adorned, factored or
// delta relation's own, and a demand relation's, the one it guards. A stored prefix and a set-bound
// variable's values belong to none.
func explainRelation(rel string) string {
	if isMagic(rel) {
		rel = strings.TrimPrefix(rel, magicPrefix)
		if i := strings.LastIndexByte(rel, '/'); i >= 0 {
			rel = rel[:i]
		}
		return displayName(rel)
	}
	if strings.HasPrefix(rel, "\x00") {
		return ""
	}
	return shownName(rel)
}

// ExplainName makes query text that holds the names rewrites give their relations and variables
// readable: reach/bf is reach called with its first argument bound, demand:reach/bf the values demanded
// of it, reach/bf+ what a semi-naive round added to it, prefix:0 a stored body prefix, reach:from1 and
// reach:answer1 a factored walk, pushed{edge(?a, ?b), edge(?b, ?c)} a conjunction the Source evaluated
// (#237), values:x a set-bound variable's values, and ?1.x a variable inlining
// renamed. Report uses it for every rule and literal it shows.
func ExplainName(s string) string {
	r := strings.NewReplacer(
		magicPrefix, "demand:",
		supPrefix, "prefix:",
		pushPrefix, "pushed",
		bindPrefix, "values:",
		aggSep, ":",
		deltaSep, "+",
		fromSep, ":from",
		"\x00answer", ":answer",
		"\x00/", "/",
		"\x00", "",
	)
	return r.Replace(s)
}

func sortedKeys[V any](m map[string]V) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// String renders the report as text: the totals, the relations, then each body with its literals in
// the order they ran.
func (r *Report) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "work %d", r.Work)
	if r.Budget > 0 {
		fmt.Fprintf(&sb, " of a budget of %d", r.Budget)
	}
	fmt.Fprintf(&sb, ", %d rows, %s\n", r.Rows, r.Elapsed.Round(time.Microsecond))
	if r.Error != "" {
		fmt.Fprintf(&sb, "error: %s\n", r.Error)
	}
	if r.Lookups > 0 || r.Batches > 0 || r.Fetched > 0 || r.Pushes > 0 || r.Walks > 0 {
		fmt.Fprintf(&sb, "fetched %d tuples from the source", r.Fetched)
		if r.Lookups > 0 || r.Batches > 0 {
			fmt.Fprintf(&sb, " in %d lookups", r.Lookups)
		}
		if r.Batches > 0 {
			fmt.Fprintf(&sb, " and %d batches", r.Batches)
		}
		if r.Pushes > 0 {
			fmt.Fprintf(&sb, ", %d pushed conjunctions", r.Pushes)
		}
		if r.Walks > 0 {
			fmt.Fprintf(&sb, ", %d walks", r.Walks)
		}
		sb.WriteString("\n")
	}
	if c := r.Cold; c != nil && (c.Work != r.Work || c.Lookups != r.Lookups || c.Batches != r.Batches || c.Fetched != r.Fetched || c.Pushes != r.Pushes || c.Walks != r.Walks || c.Error != "") {
		fmt.Fprintf(&sb, "cold: work %d, fetched %d tuples, %d lookups", c.Work, c.Fetched, c.Lookups)
		if c.Batches > 0 {
			fmt.Fprintf(&sb, ", %d batches", c.Batches)
		}
		if c.Pushes > 0 {
			fmt.Fprintf(&sb, ", %d pushed conjunctions", c.Pushes)
		}
		if c.Walks > 0 {
			fmt.Fprintf(&sb, ", %d walks", c.Walks)
		}
		if c.Error != "" {
			fmt.Fprintf(&sb, ", error: %s", c.Error)
		}
		sb.WriteString("\n")
	}
	if len(r.Relations) > 0 {
		sb.WriteString("\nrelations\n")
		for _, rr := range r.Relations {
			fmt.Fprintf(&sb, "  %s: %s", rr.Relation, rr.How)
			if len(rr.Adornments) > 0 {
				fmt.Fprintf(&sb, " [%s]", strings.Join(rr.Adornments, " "))
			}
			if rr.Demanded > 0 {
				fmt.Fprintf(&sb, ", %d demanded", rr.Demanded)
			}
			fmt.Fprintf(&sb, ", %d tuples", rr.Tuples)
			if rr.Rounds > 0 {
				fmt.Fprintf(&sb, ", %d rounds", rr.Rounds)
			}
			fmt.Fprintf(&sb, ", work %d", rr.Work)
			if rr.Demanded > 0 {
				fmt.Fprintf(&sb, " (%d per demanded value)", rr.Work/int64(rr.Demanded))
			}
			sb.WriteString("\n")
		}
	}
	if r.Goal != nil {
		sb.WriteString("\ngoal\n")
		r.Goal.write(&sb)
	}
	if len(r.Rules) > 0 {
		sb.WriteString("\nrules\n")
		for _, br := range r.Rules {
			br.write(&sb)
		}
	}
	if len(r.Sources) > 0 {
		sb.WriteString("\nsources\n")
		for _, s := range r.Sources {
			how := "read"
			if s.Cached {
				how = "cached"
			}
			if s.Tuples == 0 && s.Scans == 0 && len(s.Indexes) == 0 {
				fmt.Fprintf(&sb, "  %s: looked up", s.Relation)
			} else {
				fmt.Fprintf(&sb, "  %s: %d tuples, %s", s.Relation, s.Tuples, how)
			}
			if s.Pushes > 0 {
				fmt.Fprintf(&sb, ", in %d pushed conjunctions", s.Pushes)
			}
			if s.Walks > 0 {
				fmt.Fprintf(&sb, ", %d walks", s.Walks)
			}
			if s.Lookups > 0 || s.Batches > 0 || s.Hits > 0 || s.Kept > 0 || s.Pushes > 0 {
				fmt.Fprintf(&sb, ", %d lookups", s.Lookups)
				if s.Batches > 0 {
					fmt.Fprintf(&sb, " and %d batches", s.Batches)
				}
				fmt.Fprintf(&sb, " fetching %d tuples", s.Fetched)
				if s.Hits > 0 {
					fmt.Fprintf(&sb, ", %d answered from earlier lookups", s.Hits)
				}
				if s.Kept > 0 {
					fmt.Fprintf(&sb, ", %d from earlier queries' lookups", s.Kept)
				}
			}
			if s.Scans > 0 {
				fmt.Fprintf(&sb, ", %d scans", s.Scans)
			}
			for _, x := range s.Indexes {
				built := "kept"
				if x.Built {
					built = "built"
				}
				fmt.Fprintf(&sb, ", index (%s) %s, %d probes", strings.Join(x.On, ", "), built, x.Probes)
			}
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func (br *BodyReport) write(sb *strings.Builder) {
	if br.Rule != "" && br.Rule != br.Ran {
		fmt.Fprintf(sb, "  %s\n    ran as %s\n", br.Rule, br.Ran)
	} else {
		fmt.Fprintf(sb, "  %s\n", br.Ran)
	}
	fmt.Fprintf(sb, "    %d runs", br.Runs)
	if br.Tuples > 0 {
		fmt.Fprintf(sb, ", %d new tuples", br.Tuples)
	}
	fmt.Fprintf(sb, ", work %d, %s\n", br.Work, br.Elapsed.Round(time.Microsecond))
	for _, l := range br.Literals {
		fmt.Fprintf(sb, "    %-40s reached %d, passed %d, work %d", l.Literal, l.Reached, l.Passed, l.Work)
		if len(l.Access) > 0 {
			fmt.Fprintf(sb, ", %s", strings.Join(l.Access, " | "))
		}
		sb.WriteString("\n")
	}
}

// explainEDB records how a base relation is about to be read for atom: through which index, or by a
// scan. It runs before the candidates are taken, so an index it finds missing is one this Eval builds.
func (b *Base) explainEDB(atom *Atom, rows []ns.Tuple, bnd *binding) {
	e := b.run.explain
	s := e.readSource(atom.Relation, len(rows), e.cachedTuples[atom.Relation])
	s.Tuples = len(rows)
	mask, _, ok := boundArgs(atom.Args, bnd)
	if b.edb == nil || b.noIndex || len(rows) < indexMinFacts || !ok {
		s.Scans++
		e.access("scan")
		return
	}
	on := maskLabels(b, atom.Relation, mask)
	e.probe(atom.Relation, on, !e.cachedIndexes[idxKey{rel: atom.Relation, mask: mask}])
	e.access("index (" + strings.Join(on, ", ") + ")")
}

// explainLookup records a call the Source looked up for atom, fetching tuples.
func (b *Base) explainLookup(atom *Atom, mask patternMask, fetched int) {
	e := b.run.explain
	s := e.readSource(atom.Relation, 0, false)
	s.Lookups++
	s.Fetched += int64(fetched)
	e.access("lookup (" + strings.Join(maskLabels(b, atom.Relation, mask), ", ") + ")")
}

// explainLookupHit records a call for atom's pattern answered from what this Eval looked up already,
// or, when kept, from what an earlier Eval did (#212).
func (b *Base) explainLookupHit(atom *Atom, mask patternMask, kept bool) {
	e := b.run.explain
	s := e.readSource(atom.Relation, 0, false)
	if kept {
		s.Kept++
	} else {
		s.Hits++
	}
	e.access("lookup (" + strings.Join(maskLabels(b, atom.Relation, mask), ", ") + ")")
}

// explainBatch records a batch of lookups for atom's pattern that fetched tuples.
func (b *Base) explainBatch(atom *Atom, mask patternMask, fetched int) {
	s := b.run.explain.readSource(atom.Relation, 0, false)
	s.Batches++
	s.Fetched += int64(fetched)
	b.run.explain.access("batched lookup (" + strings.Join(maskLabels(b, atom.Relation, mask), ", ") + ")")
}

// explainIDB records how a derived relation is about to be read for atom.
func (b *Base) explainIDB(atom *Atom, bnd *binding) {
	mask, _, ok := boundArgs(atom.Args, bnd)
	if len(b.idb[atom.Relation]) < indexMinFacts || !ok {
		b.run.explain.access("derived scan")
		return
	}
	b.run.explain.access("derived index (" + strings.Join(maskLabels(b, atom.Relation, mask), ", ") + ")")
}

// maskLabels names the arguments a pattern binds: by the relation's labels, a base relation's schema or
// a module member's signature, when it has them, else by position from #1.
func maskLabels(b *Base, rel string, mask patternMask) []string {
	s, ok := b.schemaOf(rel)
	if !ok {
		s, _ = b.derivedSchema(rel)
	}
	var out []string
	for i := 0; i < maskWidth; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		if i < len(s.Labels) && s.Labels[i] != "" {
			out = append(out, s.Labels[i])
		} else {
			out = append(out, fmt.Sprintf("#%d", i+1))
		}
	}
	return out
}

// explainPush records a pushed conjunction for each relation in g: a call to the Source that returned
// rows, or one answered from what this Eval (hit) or an earlier one (kept) pushed.
func (b *Base) explainPush(g *pushGroup, rows int, hit, kept bool) {
	e := b.run.explain
	if !hit && !kept {
		e.pushes++
	}
	seen := map[string]bool{}
	for _, lit := range g.lits {
		rel := lit.Pos.Relation
		s := e.readSource(rel, 0, false)
		s.Fetched += int64(rows)
		if seen[rel] {
			continue
		}
		seen[rel] = true
		switch {
		case kept:
			s.Kept++
		case hit:
			s.Hits++
		default:
			s.Pushes++
		}
	}
	e.access("pushed")
}

// explainWalk records a walk of atom's relation from its probe on mask that fetched facts (#239).
func (b *Base) explainWalk(atom *Atom, mask patternMask, fetched int) {
	e := b.run.explain
	e.walks++
	s := e.readSource(atom.Relation, 0, false)
	s.Walks++
	s.Fetched += int64(fetched)
	e.access("walked (" + strings.Join(maskLabels(b, atom.Relation, mask), ", ") + ")")
}
