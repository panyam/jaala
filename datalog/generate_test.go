package datalog

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"math/rand"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/panyam/jaala/internal/corpus"
	"github.com/panyam/jaala/ns"
)

// The generated corpus: random programs over random graphs, each answered by every evaluator through
// agree, the comparison both makes. Hand-written tests only reach the shapes their authors thought
// of; this reaches the rewrites (unfold, magic, plan, factor, supplementary relations) with ones
// nobody wrote. A disagreement is shrunk to a small program and graph before it is reported.
//
// A program is built in the structures below and printed as text, rather than as a Query, so
// shrinking edits a small tree and the Soufflé translator (#23) can print the same programs.
//
// JAALA_GEN_SEEDS sets how many seeds run (selfcheck.sh runs thousands), and JAALA_GEN_SEED=n
// replays the one seed a failure names.

// genCase is the corpus's case: a program, its graph, and whether it runs under Witnesses.
type genCase = corpus.Case

type genOutcome int

const (
	genAgreed  genOutcome = iota
	genSkipped            // Naive ran past genBudget
	genInvalid            // the generator wrote something the engine refuses
	genDisagreed
)

// genBudget bounds Naive's work, so an unlucky program over a dense graph is skipped rather than
// slowing the corpus down. The evaluators' work differs, so the comparison itself runs unbudgeted.
const genBudget = 200_000

func caseOpts(c genCase) []Option {
	var opts []Option
	if len(c.Prog.Bind()) > 0 {
		bind := map[Var][]ns.Value{}
		for v, names := range c.Prog.Bind() {
			bind[Var(v)] = []ns.Value{}
			for _, n := range names {
				bind[Var(v)] = append(bind[Var(v)], ns.S(n))
			}
		}
		opts = append(opts, Bind(bind))
	}
	if c.Witnessed {
		opts = append(opts, Witnesses())
	}
	return opts
}

func checkCase(c genCase) (genOutcome, string) {
	text := c.Prog.String()
	q, err := Parse(text)
	if err != nil {
		return genInvalid, err.Error()
	}
	b := baseFor(folding(c.Facts.Source())) // prefixed weights read folded (#205, #303)
	if err := Validate(q, b.reg); err != nil {
		return genInvalid, err.Error()
	}
	var over *BudgetExceeded
	if _, err := (Naive{}).Eval(bg, q, b, append(caseOpts(c), Budget(genBudget))...); errors.As(err, &over) {
		return genSkipped, ""
	}
	want, diff, werr := agree(q, b, caseOpts(c)...)
	if diff != "" {
		return genDisagreed, diff
	}
	countUnitAnswers(want)
	if c.Facts.Prefixed() {
		if diff := checkFolded(want); diff != "" {
			return genDisagreed, diff
		}
	}
	if diff := agreeLookingUp(q, b, want, werr, caseOpts(c)); diff != "" {
		return genDisagreed, diff
	}
	if diff := agreeFederated(q, b, want, werr, caseOpts(c)); diff != "" {
		return genDisagreed, diff
	}
	if diff := agreePushed(q, b, want, werr, caseOpts(c)); diff != "" {
		return genDisagreed, diff
	}
	if diff := agreePartitioned(q, b, want, werr, caseOpts(c)); diff != "" {
		return genDisagreed, diff
	}
	if diff := agreeStreamed(q, b, want, werr, caseOpts(c)); diff != "" {
		return genDisagreed, diff
	}
	if diff := agreeOverlaid(q, b, want, werr, caseOpts(c)); diff != "" {
		return genDisagreed, diff
	}
	if diff := agreeAt(q, b, caseOpts(c), len(text)); diff != "" {
		return genDisagreed, diff
	}
	if diff := agreeAcross(q, b, caseOpts(c), len(text)); diff != "" {
		return genDisagreed, diff
	}
	return genAgreed, ""
}

// agreeStreamed runs q over b's facts through a Source that streams every relation (#264), where the
// planned SemiNaive streams a relation only its goal reads: every evaluator must answer as Naive did
// reading them whole. A program whose goal streamed, on a fresh Base, counts for the control.
func agreeStreamed(q Query, b *Base, want []Row, werr error, opts []Option) string {
	if diff := agreeOver(q, b, MustBase(b.reg, streamingOver(b.src)), "streamed", want, werr, opts); diff != "" {
		return diff
	}
	src := streamingOver(b.src)
	fresh := MustBase(b.reg, src)
	if _, err := (SemiNaive{}).Eval(bg, q, fresh, opts...); err == nil {
		for _, rel := range src.Relations() {
			if src.count(rel) > 0 && !fresh.edb.holds(rel) {
				streamedPrograms++
				break
			}
		}
	}
	return ""
}

// alonePushedPrograms counts those whose planned evaluation pushed one literal alone (#263).
var alonePushedPrograms int

// partitionedBatchPrograms counts those whose partitioned push carried Bindings (#268).
var partitionedBatchPrograms int

// streamedPrograms counts those whose goal streamed a relation into its answer (#264).
var streamedPrograms int

// lookedUpPrograms counts the corpus programs whose evaluation made a lookup, for the corpus's control.
var lookedUpPrograms int

// federatedPrograms counts those whose evaluation made a lookup through a Federation's members.
var federatedPrograms int

// pushedPrograms counts those whose planned evaluation pushed a conjunction to the Source (#237).
var pushedPrograms int

// batchedPrograms counts those whose planned evaluation asked a batching Source for a batch (#153).
var batchedPrograms int

// fetchedWalkPrograms counts those whose planned evaluation over a batching Source fetched a closure's
// or a paths call's steps ahead of its walk (#396), and federatedWalkPrograms those through a
// Federation's batching member.
var fetchedWalkPrograms, federatedWalkPrograms int

// callsWalk reports whether q calls graph.closure or graph.paths.
func callsWalk(q Query) bool {
	called := func(b Body) bool {
		return slices.ContainsFunc(b.Literals, func(l Literal) bool { return l.Pos != nil && walkCalled(l.Pos.Relation) != nil })
	}
	return called(q.Goal) || slices.ContainsFunc(q.Rules, func(r Rule) bool { return called(r.Body) })
}

// walkFetched reports whether the planned SemiNaive over src fetched an engine walk's steps ahead of
// it: a batch or a walk read under a closure's or a paths call's literal, which only prefetchClosures
// makes there.
func walkFetched(q Query, reg *ns.Vocabulary, src ns.Source) bool {
	var r Report
	if _, err := (SemiNaive{}).Eval(bg, q, MustBase(reg, src), Explain(&r)); err != nil {
		return false
	}
	for _, body := range append([]*BodyReport{r.Goal}, r.Rules...) {
		if body == nil {
			continue
		}
		for _, l := range body.Literals {
			if !strings.HasPrefix(l.Literal, "graph.") {
				continue
			}
			if slices.ContainsFunc(l.Access, func(a string) bool { return strings.HasPrefix(a, "batched") || strings.HasPrefix(a, "walked") }) {
				return true
			}
		}
	}
	return false
}

// unitAnswers counts the corpus answers holding a number with a unit, and mixedAnswers those with an
// aggregate absent over mixed units, for the control that units reach the goals (#192).
var unitAnswers, mixedAnswers int

func countUnitAnswers(rows []Row) {
	unit, mixed := false, false
	for _, r := range rows {
		for _, v := range r.Bind {
			unit = unit || v.BaseUnit != ""
		}
		for _, why := range r.Why {
			mixed = mixed || strings.HasPrefix(why, "mixed units")
		}
	}
	if unit {
		unitAnswers++
	}
	if mixed {
		mixedAnswers++
	}
}

// foldedAnswers counts the answers of programs over weights served in mV, kV or A*ohm that hold a
// number in V, for the control that folded values reach the goals (#205).
var foldedAnswers int

// checkFolded fails an answer, over weights served in other spellings of volts, that holds one of
// those spellings rather than V: every evaluator reads values through one fold, so only this (and the
// Soufflé step, which sees the folded numbers) would notice it missing.
func checkFolded(rows []Row) string {
	inV := false
	for _, r := range rows {
		for v, val := range r.Bind {
			if corpus.Folded(val.BaseUnit) != val.BaseUnit {
				return fmt.Sprintf("answer column %s holds %s %s, a spelling the Base folds to %s", v, val.S, val.BaseUnit, corpus.Folded(val.BaseUnit))
			}
			inV = inV || val.BaseUnit == "V"
		}
	}
	if inV {
		foldedAnswers++
	}
	return ""
}

// agreeLookingUp runs q over b's facts through a Source answering lookups (#126), and the planned
// SemiNaive's in batches (#153): every evaluator must
// give the rows and error Naive gave reading them whole, and Naive the same citations and witnesses
// under CanonicalCites.
func agreeLookingUp(q Query, b *Base, want []Row, werr error, opts []Option) string {
	src := batchingAt(b.src)
	diff := agreeOver(q, b, MustBase(b.reg, src), "looking up", want, werr, opts)
	if len(src.lookups) > 0 {
		lookedUpPrograms++
	}
	if len(src.batches) > 0 {
		batchedPrograms++
	}
	if diff == "" && callsWalk(q) && walkFetched(q, b.reg, batchingAt(b.src)) {
		fetchedWalkPrograms++
	}
	return diff
}

// agreeFederated is agreeLookingUp over b's facts dealt across three stores that each answer lookups
// (#211), so a call's facts come back from several members' lookups at once, and that index only
// their first argument (#154), so the planner reorders bodies around their scans.
func agreeFederated(q Query, b *Base, want []Row, werr error, opts []Option) string {
	members := splitInto(b.src, 3, func(int) bool { return true })
	stores := make([]*looking, len(members))
	for i := range members {
		stores[i] = members[i].Source.(*looking)
		members[i].Source = firstIndexed{stores[i]}
	}
	members[0].Source = firstIndexedBatching{&batching{looking: stores[0]}}
	members[2].Source = streamingFirstIndexed{firstIndexed{stores[2]}}
	f, err := ns.NewFederation(members...)
	if err != nil {
		return err.Error()
	}
	diff := agreeOver(q, b, MustBase(b.reg, f), "federated", want, werr, opts)
	if diff == "" && callsWalk(q) && walkFetched(q, b.reg, f) {
		federatedWalkPrograms++
	}
	for _, st := range stores {
		if len(st.lookups) > 0 {
			federatedPrograms++
			break
		}
	}
	return diff
}

// agreePushed is agreeLookingUp over a store that also evaluates conjunctions (#237), and compares the
// planned SemiNaive over it, the only evaluator that pushes, with Naive reading whole under
// CanonicalCites too, so a pushed row's citations and witnesses are checked as well as its bindings.
// Every other program's store takes no comparisons, so a group is pushed both with and without them.
func agreePushed(q Query, b *Base, want []Row, werr error, opts []Option) string {
	src := pushingAt(b.src)
	pushedSeed++
	src.noCompare = pushedSeed%2 == 0
	lb := MustBase(b.reg, src)
	if diff := agreeOver(q, b, lb, "pushed", want, werr, opts); diff != "" {
		return diff
	}
	if slices.ContainsFunc(src.pushed, func(c ns.Conjunction) bool { return len(c.Atoms) > 1 }) {
		pushedPrograms++
	}
	if slices.ContainsFunc(src.pushed, func(c ns.Conjunction) bool { return len(c.Atoms) == 1 && c.Page == nil }) {
		alonePushedPrograms++
	}
	if slices.ContainsFunc(src.pushed, func(c ns.Conjunction) bool { return len(c.Bindings) > 0 }) {
		batchPushedPrograms++
	}
	if src.pages > 0 {
		pagedPrograms++
	}
	if src.summaries > 0 {
		summarizedPrograms++
	}
	if diff := agreeWalked(b, func() (ns.Source, func() int) {
		p := pushingAt(b.src)
		return p, func() int {
			if p.stepWalks > 0 {
				stepWalkedPrograms++
			}
			return p.walks() + p.stepWalks
		}
	}); diff != "" {
		return diff
	}
	if diff := agreeGoals(b, func() (ns.Source, func() (int, int, int)) {
		p := pushingAt(b.src)
		return p, func() (int, int, int) { return p.pages, p.summaries, p.alone() }
	}); diff != "" {
		return diff
	}
	if werr != nil || anyRows(q) {
		return ""
	}
	canon := append(opts[:len(opts):len(opts)], CanonicalCites(), Witnesses())
	w, _ := Naive{}.Eval(bg, q, b, canon...)
	g, err := SemiNaive{}.Eval(bg, q, MustBase(b.reg, pushingAt(b.src)), canon...)
	if err != nil || !reflect.DeepEqual(w, g) {
		return fmt.Sprintf("planned SemiNaive pushed cites differently under CanonicalCites on %v\n whole:  %v\n pushed: %v %v", q, w, g, err)
	}
	return ""
}

// batchPushedPrograms counts those whose planned evaluation pushed a group for a batch of bindings
// (#246).
var batchPushedPrograms int

// pagedPrograms and summarizedPrograms count the goals agreeGoals pushed that a store paged or
// summarized (#241, #240).
var pagedPrograms, summarizedPrograms int

// goalQuestions aggregate, page and filter one literal over a graph's own relations, the shapes a
// store pages, summarizes or pushes alone (#263) and the generated programs rarely take: their goals
// read derived relations or negate, and their literals join or carry constants.
// The weights carry units in some graphs (#192), mixed in some, so the unit rules are exercised.
var goalQuestions = []string{
	`edge(?a, ?b) => ?a, count(?b)`,
	`weight(?n, ?w) => sum(?w), min(?w), max(?w)`,
	`weight(?n, ?w) => count(distinct ?w), list(?w), list(distinct ?w), sum(distinct ?w)`,
	`edge(?a, ?b), weight(?b, ?w), ?w > 2 => ?a, sum(?w), count(?b)`,
	`edge(?a, ?b) => ?a, ?b order by ?b desc, ?a limit 3 offset 1`,
	`weight(?n, ?w) => ?n, ?w order by ?w limit 2`,
	`edge(?a, ?b), node(?b) => ?a, ?b order by ?a limit 2`,
	`weight(?n, ?w) => ?n, min(?w), max(?w)`,
	`edge(?a, ?b) => ?a, ?b order by ?a desc, ?b after "v3", "v1" limit 2`,
	`edge(?a, ?b), weight(?b, ?w) => ?a, max(?w)`,
	`weight(?n, ?w) => min(?w), max(distinct ?w)`,
	`edge(?a, ?a) => ?a`,
	`weight(?n, ?w), ?w > 2 => ?n, ?w`,
}

// agreeGoals runs goalQuestions over a Source that pages and summarizes, on a fresh Base each (Naive
// reading a relation whole would leave nothing to push), and compares each with Naive reading whole:
// its rows, then under CanonicalCites its citations and witnesses.
func agreeGoals(b *Base, pushing func() (ns.Source, func() (int, int, int))) string {
	for _, text := range goalQuestions {
		q, err := Parse(text)
		if err != nil {
			return err.Error()
		}
		want, werr := Naive{}.Eval(bg, q, b)
		src, counts := pushing()
		got, gerr := SemiNaive{}.Eval(bg, q, MustBase(b.reg, src))
		if errText(gerr) != errText(werr) || !reflect.DeepEqual(binds(got), binds(want)) {
			return fmt.Sprintf("planned SemiNaive over a store paging and summarizing answers differently on %v\n whole:  %v %v\n pushed: %v %v", q, binds(want), werr, binds(got), gerr)
		}
		pages, sums, alone := counts()
		if pages > 0 {
			pagedPrograms++
		}
		if alone > 0 {
			alonePushedPrograms++
		}
		if sums > 0 {
			summarizedPrograms++
		}
		if werr != nil {
			continue
		}
		canon := []Option{CanonicalCites(), Witnesses()}
		w, _ := Naive{}.Eval(bg, q, b, canon...)
		fresh, _ := pushing()
		g, err := SemiNaive{}.Eval(bg, q, MustBase(b.reg, fresh), canon...)
		if err != nil || !reflect.DeepEqual(w, g) {
			return fmt.Sprintf("planned SemiNaive paging and summarizing cites differently under CanonicalCites on %v\n naive:  %v\n pushed: %v %v", q, w, g, err)
		}
	}
	return ""
}

// stepWalkedPrograms counts the walks agreeWalked made of a step of two literals (#250).
var stepWalkedPrograms int

// walkedPrograms counts the graphs agreeWalked walked (#239), and walkedClosurePrograms the bounded
// closures and paths calls among them (#396).
var walkedPrograms, walkedClosurePrograms int

// agreeWalked runs a reach from the graph's first edge's source over a Source that walks (#239), the
// shape a walk takes and the generated programs rarely do, and compares it with Naive reading whole,
// its bindings and, under CanonicalCites, its witnesses. walking makes a fresh Source and a count of
// the walks its stores made.
func agreeWalked(b *Base, walking func() (ns.Source, func() int)) string {
	edges := b.src.Tuples("edge")
	if len(edges) == 0 || edges[0].Vals[0].Num != nil {
		return ""
	}
	from := strconv.Quote(edges[0].Vals[0].S)
	for _, text := range []string{
		`reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c); reach(` + from + `, ?x) => ?x`,
		// two hops a step, a walk of a conjunction (#250)
		`hop2(?a, ?c) :- edge(?a, ?m), edge(?m, ?c); hop2(?a, ?c) :- hop2(?a, ?b), edge(?b, ?m), edge(?m, ?c); hop2(` + from + `, ?x) => ?x`,
		// a closure and a paths call with a max, walked to it (#396)
		`graph.closure("edge", ` + from + `, ?x, 1, 2, ?h) => ?x, ?h`,
		`graph.paths("edge", ` + from + `, ?x, "trail", 1, 2, ?p) => ?p`,
	} {
		if diff := agreeWalkedOn(b, text, walking); diff != "" {
			return diff
		}
	}
	return ""
}

// agreeWalkedOn is agreeWalked for one program.
func agreeWalkedOn(b *Base, text string, walking func() (ns.Source, func() int)) string {
	q, err := Parse(text)
	if err != nil {
		return err.Error()
	}
	src, _ := walking()
	want, werr := Naive{}.Eval(bg, q, b)
	if diff := agreeOver(q, b, MustBase(b.reg, src), "walked", want, werr, nil); diff != "" {
		return diff
	}
	canon := []Option{CanonicalCites(), Witnesses()}
	w, _ := Naive{}.Eval(bg, q, b, canon...)
	fresh, walks := walking() // agreeOver's Naive filled src's Base with lookups, so nothing was left to walk
	g, err := SemiNaive{}.Eval(bg, q, MustBase(b.reg, fresh), canon...)
	if walks() > 0 {
		walkedPrograms++
		if callsWalk(q) {
			walkedClosurePrograms++
		}
	}
	if err != nil || !reflect.DeepEqual(w, g) {
		return fmt.Sprintf("planned SemiNaive walking cites differently under CanonicalCites on %v\n naive:  %v\n walked: %v %v", q, w, g, err)
	}
	return ""
}

// partitionedPrograms counts those whose planned evaluation pushed a conjunction to more than one
// member of a partitioned federation (#238).
var partitionedPrograms int

// agreePartitioned is agreePushed over b's facts dealt across three pushing stores by their first
// argument, in a partitioned federation whose Holds routes that argument (#238), so a pushed join
// spans members and its prefixes continue across them. Each value is dealt as it reads, so a bound
// value finds the member holding it however it's spelled.
func agreePartitioned(q Query, b *Base, want []Row, werr error, opts []Option) string {
	const n = 3
	deal := func(rel string, v ns.Value) int {
		s, _ := b.src.Schema(rel)
		read := normalizeTuples(stdUnits, []ns.Tuple{{Vals: []ns.Value{v}}}, s.Types[:min(1, len(s.Types))])[0].Vals[0]
		h := 0
		for _, r := range valueKey(read) {
			h = h*31 + int(r)
		}
		return (h%n + n) % n
	}
	mems := make([]*ns.MemSource, n)
	for i := range mems {
		mems[i] = ns.NewMemSource()
		for _, rel := range b.src.Relations() {
			s, _ := b.src.Schema(rel)
			mems[i].DeclareSchema(rel, s)
		}
	}
	for _, rel := range b.src.Relations() {
		for k, t := range b.src.Tuples(rel) {
			i := k % n
			if len(t.Vals) > 0 {
				i = deal(rel, t.Vals[0])
			}
			mems[i].Add(rel, t)
		}
	}
	federate := func() ([]*pushing, *ns.Federation, error) {
		stores := make([]*pushing, n)
		members := make([]ns.Member, n)
		for i := range members {
			i := i
			stores[i] = pushingAt(mems[i])
			members[i] = ns.Member{Source: stores[i], Holds: func(rel string, bound map[int]ns.Value) bool {
				v, ok := bound[0]
				return !ok || deal(rel, v) == i
			}}
		}
		f, err := ns.NewPartitionedFederation(members...)
		return stores, f, err
	}
	stores, f, err := federate()
	if err != nil {
		return err.Error()
	}
	walking := func() (ns.Source, func() int) {
		stores, f, _ := federate()
		return f, func() int {
			n := 0
			for _, st := range stores {
				n += st.walks()
			}
			return n
		}
	}
	if diff := agreeWalked(b, walking); diff != "" {
		return diff
	}
	if diff := agreeGoals(b, func() (ns.Source, func() (int, int, int)) {
		stores, f, _ := federate()
		return f, func() (int, int, int) {
			pages, sums, alone := 0, 0, 0
			for _, st := range stores {
				pages += st.pages
				sums += st.summaries
				alone += st.alone()
			}
			return pages, sums, alone
		}
	}); diff != "" {
		return diff
	}
	lb := MustBase(b.reg, f)
	if diff := agreeOver(q, b, lb, "partitioned", want, werr, opts); diff != "" {
		return diff
	}
	asked, bound := 0, false
	for _, st := range stores {
		if st.pushes() > 0 {
			asked++
		}
		st.mu.Lock()
		bound = bound || slices.ContainsFunc(st.pushed, func(c ns.Conjunction) bool { return c.Partial && len(c.Bindings) > 0 })
		st.mu.Unlock()
	}
	if asked > 1 {
		partitionedPrograms++
	}
	if bound {
		partitionedBatchPrograms++
	}
	if werr != nil || anyRows(q) {
		return ""
	}
	canon := append(opts[:len(opts):len(opts)], CanonicalCites(), Witnesses())
	w, _ := Naive{}.Eval(bg, q, MustBase(b.reg, f), canon...)
	g, err := SemiNaive{}.Eval(bg, q, MustBase(b.reg, f), canon...)
	if err != nil || !reflect.DeepEqual(w, g) {
		return fmt.Sprintf("planned SemiNaive over a partitioned federation cites differently under CanonicalCites on %v\n naive:  %v\n pushed: %v %v", q, w, g, err)
	}
	return ""
}

// pushedSeed alternates agreePushed's store between taking comparisons and refusing them.
var pushedSeed int

// firstIndexed is a looking store whose index covers a relation's first argument only.
type firstIndexed struct{ *looking }

func (firstIndexed) Indexed(_ string, bound []int) bool { return slices.Contains(bound, 0) }

// streamingFirstIndexed is a firstIndexed store that streams a relation read whole (#233).
type streamingFirstIndexed struct{ firstIndexed }

func (s streamingFirstIndexed) TuplesSeq(_ context.Context, rel string) iter.Seq2[ns.Tuple, error] {
	return func(yield func(ns.Tuple, error) bool) {
		for _, t := range s.Tuples(rel) {
			if !yield(t, nil) {
				return
			}
		}
	}
}

// firstIndexedBatching is a firstIndexed store that also answers lookups in batches.
type firstIndexedBatching struct{ *batching }

func (firstIndexedBatching) Indexed(_ string, bound []int) bool { return slices.Contains(bound, 0) }

// agreeOver requires every evaluator over lb to answer q as Naive did over b, and Naive over lb to
// cite as over b under CanonicalCites. A limit without an order by may answer other rows over lb,
// whose tuples arrive in another order (#194), so its whole answer is compared instead, and its
// limited one checked as agreeOnSomeRows does.
func agreeOver(q Query, b, lb *Base, how string, want []Row, werr error, opts []Option) string {
	if anyRows(q) {
		whole := q
		whole.Limit = 0
		w, werr := Naive{}.Eval(bg, whole, b, opts...)
		if diff := agreeOver(whole, b, lb, how, w, werr, opts); diff != "" {
			return diff
		}
		_, diff, _ := agreeOnSomeRows(q, lb, opts...)
		return diff
	}
	for _, ev := range []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}, SemiNaive{}} {
		got, err := ev.Eval(bg, q, lb, opts...)
		if errText(err) != errText(werr) || !reflect.DeepEqual(binds(got), binds(want)) {
			return fmt.Sprintf("%T %s answers differently from Naive reading whole on %v\n whole:     %v %v\n %s: %v %v", ev, how, q, binds(want), werr, how, binds(got), err)
		}
	}
	if werr != nil {
		return ""
	}
	canon := append(opts[:len(opts):len(opts)], CanonicalCites())
	w, _ := Naive{}.Eval(bg, q, b, canon...)
	g, _ := Naive{}.Eval(bg, q, lb, canon...)
	if !reflect.DeepEqual(w, g) {
		return fmt.Sprintf("Naive %s cites differently under CanonicalCites on %v\n whole:     %v\n %s: %v", how, q, w, how, g)
	}
	return ""
}

// shrink drops rules, literals, goal clauses, edges and nodes one at a time while the case still
// disagrees, and returns the smallest case it reached with its disagreement.
func shrink(c genCase, diff string) (genCase, string) {
	return corpus.Shrink(c, diff, func(s genCase) (bool, string) {
		out, d := checkCase(s)
		return out == genDisagreed, d
	})
}

// knownDisagreements are disagreements filed and not yet fixed, recognised by their message, so the
// corpus counts them rather than failing on them. A program is classified by its first disagreement,
// before shrinking, so one known disagreement can hide another. Fixing one means deleting its line
// here.
var knownDisagreements = []struct {
	issue int
	match *regexp.Regexp
	repro genCase // a case that disagrees this way, so a wrong pattern or a fixed bug shows
}{}

func knownDisagreement(diff string) int {
	for _, k := range knownDisagreements {
		if k.match.MatchString(diff) {
			return k.issue
		}
	}
	return 0
}

func envInt(name string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return def
}

// Every evaluator must answer each generated program as Naive does, with the rules both enforces.
// The control is the shape count: each shape the generator aims at has to have been compared, both
// plain and under Witnesses, or the corpus is passing on programs that never reach the rewrites.
//
// It skips under -race unless JAALA_GEN_UNDER_RACE is set (#143): the corpus evaluates one program at
// a time and the engine starts no goroutines, so the detector can find nothing there and made it
// nine times slower. CI runs it in a step of its own without -race; the concurrent tests stay.
func TestGeneratedProgramsAgree(t *testing.T) {
	first, seeds := int64(1), envInt("JAALA_GEN_SEEDS", 300)
	if s := envInt("JAALA_GEN_SEED", 0); s > 0 {
		first, seeds = int64(s), 1
	}
	if raceDetector && seeds > 1 && os.Getenv("JAALA_GEN_UNDER_RACE") == "" {
		t.Skip("the corpus runs without -race (CI's corpus step); JAALA_GEN_UNDER_RACE=1 runs it here")
	}
	compared, known, skipped, failed := map[string]int{}, map[int]int{}, 0, 0
	lookedUpPrograms, federatedPrograms, batchedPrograms, pushedPrograms, partitionedPrograms, walkedPrograms, unitAnswers, mixedAnswers, foldedAnswers = 0, 0, 0, 0, 0, 0, 0, 0, 0
	pagedPrograms, summarizedPrograms, batchPushedPrograms, stepWalkedPrograms, streamedPrograms, partitionedBatchPrograms, alonePushedPrograms = 0, 0, 0, 0, 0, 0, 0
	atPrograms, atStoredPrograms, acrossPrograms, acrossAggregates, acrossLookedPrograms = 0, 0, 0, 0, 0
	overlaidPushed, overlaidBatched, overlaidLookedUp = 0, 0, 0
	fetchedWalkPrograms, federatedWalkPrograms, walkedClosurePrograms = 0, 0, 0
	withUnits := 0
	for seed := first; seed < first+int64(seeds); seed++ {
		rnd := rand.New(rand.NewSource(seed))
		facts := corpus.Graph(rnd)
		prog := corpus.New(rnd, facts).Program()
		if facts.HasUnits() {
			withUnits++
		}
		for _, witnessed := range []bool{false, true} {
			c := genCase{Prog: prog, Facts: facts, Witnessed: witnessed}
			switch out, diff := checkCase(c); out {
			case genSkipped:
				skipped++
			case genInvalid:
				t.Errorf("seed %d: the generator wrote a program the engine refuses: %v\n %s", seed, diff, prog)
			case genDisagreed:
				if issue := knownDisagreement(diff); issue != 0 {
					known[issue]++
					continue
				}
				failed++
				small, d := shrink(c, diff)
				t.Errorf("seed %d (witnessed %v) disagrees; replay with JAALA_GEN_SEED=%d\n%s\n %s", seed, witnessed, seed, small, d)
			default:
				mode := "plain"
				if witnessed {
					mode = "witnessed"
				}
				compared["program/"+mode]++
				for _, s := range prog.Shapes() {
					compared[s+"/"+mode]++
				}
			}
			if failed >= envInt("JAALA_GEN_MAX_FAILURES", 5) {
				t.Fatalf("stopping after %d disagreements", failed)
			}
		}
	}
	shapes := []string{"program", "recursion", "negation", "head aggregate", "goal aggregate", "body aggregate", "bound goal", "set-bound goal", "arithmetic", "bound fact", "avg", "closure", "paths", "optional", "list read"}
	var counts, issues []string
	for _, sh := range shapes {
		counts = append(counts, fmt.Sprintf("%s %d/%d", sh, compared[sh+"/plain"], compared[sh+"/witnessed"]))
	}
	for _, k := range knownDisagreements {
		if known[k.issue] > 0 {
			issues = append(issues, fmt.Sprintf("#%d %d", k.issue, known[k.issue]))
		}
	}
	t.Logf("%d seeds\n compared (plain/witnessed): %s\n skipped over budget: %d\n known disagreements, counted not failed: %s\n units: %d programs' facts carry them; %d answers hold a number with a unit, %d an aggregate absent over mixed units, %d folded from mV, kV or A*ohm",
		seeds, strings.Join(counts, ", "), skipped, strings.Join(issues, ", "), withUnits, unitAnswers, mixedAnswers, foldedAnswers)
	if seeds < 50 {
		return
	}
	if compared["arithmetic/plain"] == 0 || compared["arithmetic/witnessed"] == 0 {
		t.Errorf("control: no program computed with arithmetic (#209)")
	}
	if compared["closure/plain"] == 0 || compared["closure/witnessed"] == 0 {
		t.Errorf("control: no program walked a relation with graph.closure (#281)")
	}
	if compared["list read/plain"] == 0 || compared["list read/witnessed"] == 0 {
		t.Errorf("control: no program read back a list a rule collected (#283)")
	}
	if compared["optional/plain"] == 0 || compared["optional/witnessed"] == 0 {
		t.Errorf("control: no program matched optionally (#282)")
	}
	if compared["paths/plain"] == 0 || compared["paths/witnessed"] == 0 {
		t.Errorf("control: no program listed paths with graph.paths (#280)")
	}
	if compared["bound fact/plain"] == 0 || compared["bound fact/witnessed"] == 0 {
		t.Errorf("control: no program bound an edge's fact (#279)")
	}
	if compared["avg/plain"] == 0 || compared["avg/witnessed"] == 0 {
		t.Errorf("control: no program averaged (#284)")
	}
	if lookedUpPrograms == 0 {
		t.Errorf("control: no program made a lookup through a Source answering them")
	}
	if federatedPrograms == 0 {
		t.Errorf("control: no program made a lookup through a Federation's members")
	}
	if batchedPrograms == 0 {
		t.Errorf("control: no program asked a batching Source for a batch")
	}
	if pushedPrograms == 0 {
		t.Errorf("control: no program pushed a conjunction to the Source")
	}
	if stepWalkedPrograms == 0 {
		t.Errorf("control: no graph's walk of a two-literal step was walked by the store")
	}
	if batchPushedPrograms == 0 {
		t.Errorf("control: no program pushed a group for a batch of bindings")
	}
	if pagedPrograms == 0 {
		t.Errorf("control: no program's goal was paged by the store")
	}
	if summarizedPrograms == 0 {
		t.Errorf("control: no program's goal was summarized by the store")
	}
	if walkedPrograms == 0 {
		t.Errorf("control: no program asked a store to walk a relation")
	}
	t.Logf("closures and paths fetched ahead (#396): %d programs batched, %d through a federation, %d graphs walked by the store", fetchedWalkPrograms, federatedWalkPrograms, walkedClosurePrograms)
	if fetchedWalkPrograms == 0 || federatedWalkPrograms == 0 || walkedClosurePrograms == 0 {
		t.Errorf("control: %d programs batched a closure's or a paths call's steps, %d through a federation, and %d graphs' bounded closures were walked by the store (#396)", fetchedWalkPrograms, federatedWalkPrograms, walkedClosurePrograms)
	}
	if alonePushedPrograms == 0 {
		t.Errorf("control: no program pushed a literal alone for the store to filter")
	}
	if partitionedBatchPrograms == 0 {
		t.Errorf("control: no program pushed a batch of bindings across a partitioned federation's members")
	}
	if streamedPrograms == 0 {
		t.Errorf("control: no program's goal streamed a relation into its answer")
	}
	t.Logf("sessions: %d programs pushed to the store beneath one, %d batched, %d looked up a relation it touched", overlaidPushed, overlaidBatched, overlaidLookedUp)
	if overlaidPushed == 0 || overlaidBatched == 0 || overlaidLookedUp == 0 {
		t.Errorf("control: over a session, %d programs pushed to the store, %d batched and %d looked up a relation the session touched", overlaidPushed, overlaidBatched, overlaidLookedUp)
	}
	t.Logf("versions: %d programs answered differently between versions, %d asked a version's store", atPrograms, atStoredPrograms)
	t.Logf("across: %d programs answered rows holding in some versions only, %d of them aggregating, %d looked a store up", acrossPrograms, acrossAggregates, acrossLookedPrograms)
	if acrossAggregates < seeds/10 {
		t.Errorf("control: %d aggregating programs answered across versions with a row holding in some only, over %d seeds", acrossAggregates, seeds)
	}
	if acrossPrograms < seeds/10 {
		t.Errorf("control: %d programs answered across versions with a row holding in some only, over %d seeds", acrossPrograms, seeds)
	}
	if acrossLookedPrograms < seeds/10 {
		t.Errorf("control: %d programs looked a store up across versions over %d seeds", acrossLookedPrograms, seeds)
	}
	if atStoredPrograms < seeds/10 {
		t.Errorf("control: %d programs asked a version's store over %d seeds", atStoredPrograms, seeds)
	}
	if atPrograms < seeds/10 {
		t.Errorf("control: %d programs answered differently between versions over %d seeds", atPrograms, seeds)
	}
	if partitionedPrograms == 0 {
		t.Errorf("control: no program pushed a conjunction across a partitioned federation's members")
	}
	if unitAnswers < seeds/100 {
		t.Errorf("control: %d answers held a unit over %d seeds: units aren't reaching the goals", unitAnswers, seeds)
	}
	if foldedAnswers == 0 {
		t.Errorf("control: no answer over weights served in mV, kV or A*ohm held a number in V, over %d seeds", seeds)
	}
	// An aggregate over mixed units is rare (about 30 in 5000 seeds' two runs), so CI's 300 seeds
	// needn't meet one; ./selfcheck.sh's 5000 do, which is what catches a unit-blind aggregate.
	if seeds >= 1000 && mixedAnswers == 0 {
		t.Errorf("control: no answer held an aggregate over mixed units, over %d seeds", seeds)
	}
	for _, s := range shapes {
		for _, mode := range []string{"plain", "witnessed"} {
			if compared[s+"/"+mode] == 0 {
				t.Errorf("control: no %s program was compared %s", s, mode)
			}
		}
	}
}

// The shrinker has to reach the smallest case that still fails, or a reported disagreement buries
// its cause in a dozen rules. Failing here means "a valid program with a rule that negates", whose
// smallest form is one rule, a one-literal goal, and one node with no edges.
// The corpus writes the engine's names for closures and paths itself, since it can't import
// production datalog's unexported ones; a rename here must reach it.
func TestTheCorpusSpellsTheEnginesNames(t *testing.T) {
	if corpus.ClosureName != closureName || corpus.PathsName != pathsName || !slices.Equal(corpus.PathModes, pathModes) {
		t.Errorf("corpus writes %s, %s and modes %v; the engine reads %s, %s and %v",
			corpus.ClosureName, corpus.PathsName, corpus.PathModes, closureName, pathsName, pathModes)
	}
}

// A seed generates one program, so JAALA_GEN_SEED=n replays what a run reported (#226).
func TestASeedGeneratesOneProgram(t *testing.T) {
	gen := func(seed int64) string {
		rnd := rand.New(rand.NewSource(seed))
		facts := corpus.Graph(rnd)
		p := corpus.New(rnd, facts).Program()
		return fmt.Sprintf("%s\n bind: %v\n facts: %s", p, p.Bind(), facts)
	}
	aggregates := 0
	for seed := int64(1); seed <= int64(envInt("JAALA_GEN_SEEDS", 300)); seed++ {
		first := gen(seed)
		if strings.Contains(first, ": {") {
			aggregates++
		}
		for range 3 {
			if again := gen(seed); again != first {
				t.Fatalf("seed %d generated two programs:\n %s\n %s", seed, first, again)
			}
		}
	}
	if aggregates == 0 {
		t.Error("control: no seed's program has a body aggregate, whose shared variables the map walk chose")
	}
}

func TestShrinkReachesTheSmallestFailingCase(t *testing.T) {
	negates := func(c genCase) (bool, string) {
		if out, _ := checkCase(c); out == genInvalid {
			return false, ""
		}
		return c.Prog.Negates(), ""
	}
	for seed := int64(1); seed <= 40; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		facts := corpus.Graph(rnd)
		c := genCase{Prog: corpus.New(rnd, facts).Program(), Facts: facts, Witnessed: true}
		if ok, _ := negates(c); !ok || c.Prog.Rules() < 3 || c.Facts.Edges() == 0 {
			continue
		}
		small, _ := corpus.Shrink(c, "", negates)
		if small.Prog.Rules() != 1 || small.Prog.GoalLiterals() != 1 || small.Facts.Nodes() != 1 || small.Facts.Edges() != 0 || small.Witnessed {
			t.Errorf("seed %d shrank to\n%s\nwant one rule, one goal literal, one node, no edges, not witnessed", seed, small)
		}
		return
	}
	t.Fatal("control: no seed in 1..40 gave a program with a negating rule, three rules and an edge")
}

// Each known disagreement's repro must still disagree, and be recognised as that issue: a pattern
// that matches nothing would let the bug through as known, and a fixed bug should leave the list.
func TestKnownDisagreementsStillDisagree(t *testing.T) {
	for _, k := range knownDisagreements {
		out, diff := checkCase(k.repro)
		if out != genDisagreed {
			t.Errorf("#%d: the repro no longer disagrees (outcome %d %s); if #%d is fixed, drop it from knownDisagreements\n%s", k.issue, out, diff, k.issue, k.repro)
			continue
		}
		if got := knownDisagreement(diff); got != k.issue {
			t.Errorf("#%d: the repro's disagreement is recognised as #%d\n%q", k.issue, got, diff)
		}
	}
}

// errorOf runs an evaluator and returns its error, turning a panic into one so the test can say
// which evaluator panicked on which program.
func errorOf(ev Evaluator, q Query, b *Base) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("PANIC: %v", p)
		}
	}()
	_, err = ev.Eval(bg, q, b)
	return err
}

// Every evaluator refuses a broken program with the same query: error and never panics (the first
// step of #89). Each generated program is broken three ways, one literal at a time: an argument
// dropped, an argument added, and a relation renamed to one nothing defines. Each variant is wrong,
// so each has to fail. The planned evaluator used to panic on the first (#133).
func TestBrokenProgramsFailTheSameWay(t *testing.T) {
	seeds := envInt("JAALA_GEN_SEEDS", 300)
	checked, failed := 0, 0
	for seed := int64(1); seed <= int64(seeds); seed++ {
		rnd := rand.New(rand.NewSource(seed))
		facts := corpus.Graph(rnd)
		prog := corpus.New(rnd, facts).Program()
		for i, broken := range corpus.Break(prog, rnd) {
			text := broken.String()
			q, err := Parse(text)
			if err != nil {
				continue // refused by the parser, the same for every evaluator
			}
			b := baseFor(std(facts.Source()))
			var errs []string
			var first error
			for _, ev := range []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}, SemiNaive{}} {
				err := errorOf(ev, q, b)
				if first == nil {
					first = err
				}
				errs = append(errs, fmt.Sprint(err))
			}
			checked++
			ok := strings.HasPrefix(errs[0], "query: ") && errs[1] == errs[0] && errs[2] == errs[0]
			if !ok {
				failed++
				t.Errorf("seed %d: %s\n naive:    %s\n written:  %s\n planned:  %s", seed, broken, errs[0], errs[1], errs[2])
			}
			// The literal breakProgram changed is what the error is about, and Locate finds it (#321).
			// Dropping an argument can drop a negation's only anchor, which is checked first.
			want := []ErrorKind{KindArity, KindArity, KindUnknown}[i]
			var ce *CheckError
			if errors.As(first, &ce) && i == 0 && ce.Kind == KindUnbound {
				continue
			}
			if !errors.As(first, &ce) || ce.Kind != want {
				failed++
				t.Errorf("seed %d: %s\n gave %T %v, want a %s CheckError", seed, broken, first, first, want)
			} else if pos, found := Locate(text, ce); !found || !strings.HasPrefix(text[pos.Offset:], ce.Relation+"(") {
				failed++
				t.Errorf("seed %d: %s\n %v: Locate = %+v %v, not a call to %s", seed, broken, ce, pos, found, ce.Relation)
			}
			if failed >= 5 {
				t.Fatal("stopping after 5")
			}
		}
	}
	if checked < seeds {
		t.Fatalf("control: only %d broken programs reached the evaluators", checked)
	}
	t.Logf("%d broken programs, each refused alike by all three evaluators", checked)
}
