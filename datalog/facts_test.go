package datalog

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// routes is a small graph whose roads carry ids, as a store keeping relationships would: two roads
// from a to b (r1, r3), b to c (r2), a loop at c (r4), and d to a with no id, whose fact the engine
// names itself. road_attr is what a store keeps about a road, keyed by its id.
func routes() *ns.MemSource {
	src := ns.NewMemSource().
		Declare("road", "from", "to").
		Declare("road_attr", "road", "key", "value").
		Declare("town", "name").
		DeclareSchema("dist", ns.Schema{Arity: 2, Labels: []string{"road", "km"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}})
	for _, r := range []struct{ id, from, to string }{{"r1", "a", "b"}, {"r2", "b", "c"}, {"r3", "a", "b"}, {"r4", "c", "c"}, {"", "d", "a"}} {
		src.Add("road", ns.Tuple{Vals: []ns.Value{ns.S(r.from), ns.S(r.to)}, Cites: []string{"map:" + r.from + r.to}, ID: r.id})
	}
	for _, a := range [][3]string{{"r1", "toll", "yes"}, {"r2", "toll", "no"}, {"r3", "toll", "no"}} {
		src.Add("road_attr", ns.Tuple{Vals: []ns.Value{ns.S(a[0]), ns.S(a[1]), ns.S(a[2])}})
	}
	for _, n := range []string{"a", "b", "c", "d"} {
		src.Add("town", ns.Tuple{Vals: []ns.Value{ns.S(n)}})
	}
	// A distance stored as text in a number column, which the base reads as the number 7.
	src.Add("dist", ns.Tuple{Vals: []ns.Value{ns.S("r1"), ns.S("07")}, ID: "d1"})
	return src
}

// factRows is an answer's rows as text, each its columns' values joined by spaces, in answer order.
func factRows(rows []Row, cols ...Var) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		vals := make([]string, len(cols))
		for j, c := range cols {
			vals[j] = r.Bind[c].S
		}
		out[i] = strings.Join(vals, " ")
	}
	return out
}

func answerOn(t *testing.T, b *Base, text string, opts ...Option) []Row {
	t.Helper()
	rows, err := both(mustParse(t, text), b, opts...)
	if err != nil {
		t.Fatalf("Eval(%q): %v", text, err)
	}
	return rows
}

// A bound fact is a column of the answer: the Source's id, or the engine's name for a fact without
// one, so two roads between the same towns are two rows. control: without the fact, they are one.
func TestABoundFactAnswersItsID(t *testing.T) {
	b := baseFor(std(routes()))
	got := factRows(answerOn(t, b, `road(?a, ?b) as ?r => ?a, ?b, ?r`), "a", "b", "r")
	want := []string{"a b r1", "a b r3", "b c r2", "c c r4", `d a road("d", "a")`}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := factRows(answerOn(t, b, `road(?a, ?b) => ?a, ?b`), "a", "b"); len(got) != 4 {
		t.Errorf("control: without the fact %q, want the two a-b roads as one row", got)
	}
	if got := factRows(answerOn(t, b, `road("a", ?b) as ?r => ?r`), "r"); !slices.Equal(got, []string{"r1", "r3"}) {
		t.Errorf("a constant beside a fact: %q", got)
	}
	if got := factRows(answerOn(t, b, `road(?a, ?b) as ?r, ?r = "r2" => ?a, ?b`), "a", "b"); !slices.Equal(got, []string{"b c"}) {
		t.Errorf("a fact found by its id: %q", got)
	}
	// A tuple whose value the base rewrites as it reads it (07 as 7) keeps its id.
	if got := factRows(answerOn(t, b, `dist(?r, ?k) as ?d => ?d, ?k`), "d", "k"); !slices.Equal(got, []string{"d1 7"}) {
		t.Errorf("a normalized fact: %q", got)
	}
	// A host binding the goal's other variables leaves the fact bound (Bind rewrites the goal).
	got = factRows(answerOn(t, b, `road(?a, ?b) as ?r => ?a, ?r`, Bind(map[Var][]ns.Value{"a": {ns.S("a")}})), "a", "r")
	if !slices.Equal(got, []string{"a r1", "a r3"}) {
		t.Errorf("a host-bound goal: %q", got)
	}
}

// Two facts with the same values and citations are two derivations CanonicalCites tells apart by id,
// so every evaluator keeps the same one.
func TestCanonicalCitesTellFactsApartByID(t *testing.T) {
	leaf := func(id string) []*Witness {
		return []*Witness{{Relation: "road", Values: []ns.Value{ns.S("a"), ns.S("b")}, Cites: []string{"map:ab"}, Fact: id}}
	}
	if c := compareNodes(leaf("r1"), leaf("r3")); c >= 0 {
		t.Errorf("compareNodes(r1, r3) = %d, want r1 first", c)
	}
	if c := compareNodes(leaf("r1"), leaf("r1")); c != 0 {
		t.Errorf("control: one fact compares %d with itself", c)
	}
}

// Two facts compare by id, so a path can require two different roads, as a Cypher match does.
// control: without the comparison the loop at c follows itself.
func TestTwoBoundFactsCompareByID(t *testing.T) {
	b := baseFor(std(routes()))
	q := `road(?a, ?b) as ?r, road(?b, ?c) as ?s%s => ?r, ?s`
	got := factRows(answerOn(t, b, strings.Replace(q, "%s", ", ?r != ?s", 1)), "r", "s")
	if want := []string{"r1 r2", "r2 r4", "r3 r2", `road("d", "a") r1`, `road("d", "a") r3`}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := factRows(answerOn(t, b, strings.Replace(q, "%s", "", 1)), "r", "s"); !slices.Contains(got, "r4 r4") {
		t.Errorf("control: without != %q lacks the loop twice", got)
	}
}

// A fact's attributes are an ordinary relation of the host's, joined on its id, in a rule as in a
// goal.
func TestABoundFactsAttributesJoinOnItsID(t *testing.T) {
	b := baseFor(std(routes()))
	got := factRows(answerOn(t, b, `toll(?a, ?b) :- road(?a, ?b) as ?r, road_attr(?r, "toll", "yes"); toll(?a, ?b) => ?a, ?b`), "a", "b")
	if !slices.Equal(got, []string{"a b"}) {
		t.Errorf("toll roads %q", got)
	}
	got = factRows(answerOn(t, b, `road(?a, ?b) as ?r, road_attr(?r, "toll", ?t) => ?r, ?t`), "r", "t")
	if want := []string{"r1 yes", "r2 no", "r3 no"}; !slices.Equal(got, want) {
		t.Errorf("tolls %q, want %q", got, want)
	}
}

// A bound fact anchors a negation (#441: Cypher's r.prop IS NULL is a negated attribute of r's fact),
// in the goal and in a rule. control: a negation over a variable nothing positive binds is still
// refused.
func TestABoundFactAnchorsANegation(t *testing.T) {
	b := baseFor(std(routes()))
	got := factRows(answerOn(t, b, `road(?a, ?b) as ?r, not road_attr(?r, "toll", _) => ?r`), "r")
	if want := []string{"r4", `road("d", "a")`}; !slices.Equal(got, want) {
		t.Errorf("roads without a toll %q, want %q (r4, then d to a's engine-named fact)", got, want)
	}
	got = factRows(answerOn(t, b, `free(?r) :- road(_, _) as ?r, not road_attr(?r, "toll", "yes"); free(?r) => ?r`), "r")
	if want := []string{"r2", "r3", "r4", `road("d", "a")`}; !slices.Equal(got, want) {
		t.Errorf("roads without a toll of yes %q, want %q", got, want)
	}
	if _, err := both(mustParse(t, `road(?a, ?b) as ?r, not road_attr(?s, "toll", _) => ?r`), b); err == nil || !strings.Contains(err.Error(), "shares no variable") {
		t.Errorf("control: %v, want the negation over ?s refused", err)
	}
}

// A witness shows a bound fact as the relation the program names, its id beside its values.
func TestABoundFactsWitnessCarriesItsID(t *testing.T) {
	rows := answerOn(t, baseFor(std(routes())), `road("b", ?c) as ?r => ?r`, Witnesses())
	if len(rows) != 1 || len(rows[0].Witness) != 1 {
		t.Fatalf("rows %v", rows)
	}
	w := rows[0].Witness[0]
	if w.Relation != "road" || w.Fact != "r2" || len(w.Values) != 2 || w.Values[1].S != "c" || !slices.Equal(w.Cites, []string{"map:bc"}) {
		t.Errorf("witness %+v", w)
	}
}

var factQueries = []string{
	`road(?a, ?b) as ?r => ?a, ?b, ?r`,
	`road("a", ?b) as ?r => ?r`,
	`town(?t), road(?t, ?b) as ?r => ?t, ?r`,
	`road(?a, ?b) as ?r, road(?b, ?c) as ?s, ?r != ?s => ?r, ?s`,
	`road(?a, ?b) as ?r, road_attr(?r, "toll", ?t) => ?r, ?t`,
	`road(?a, ?b) as ?r, ?r = "r2" => ?a`,
	`road(?a, ?b), road(?b, ?c), road(?c, ?d) as ?r => ?a, ?r`,
	`two(?a, ?r) :- road(?a, ?b), road(?b, ?c) as ?r; two(?a, ?r) :- road(?a, ?a) as ?r; town(?t), two(?t, ?r) => ?t, ?r`,
	`road(?a, ?b) as ?r => count(?r)`,
}

// Every store answers a bound fact as memory does, through lookups, batches, a stream, a push of the
// literals beside it, and a Federation dealing the roads between two members. controls: the lookups
// and batches are made and something is pushed, but never a fact relation, which no store knows.
func TestBoundFactsAnswerAlikeOverEveryStore(t *testing.T) {
	plain := baseFor(std(routes()))
	looked, batched, pushed, streamed := lookingAt(routes()), batchingAt(routes()), pushingAt(routes()), streamingOver(routes())
	stores := map[string]ns.Source{"looking": looked, "batching": batched, "pushing": pushed, "streaming": streamed, "federated": dealtRoutes(t)}
	for name, src := range stores {
		b := MustBase(std(routes()), src)
		for _, text := range factQueries {
			q := mustParse(t, text)
			want, err := both(q, plain)
			if err != nil {
				t.Fatalf("%s: %v", text, err)
			}
			got, err := both(q, b)
			if err != nil {
				t.Fatalf("%s over %s: %v", text, name, err)
			}
			if !reflect.DeepEqual(binds(got), binds(want)) {
				t.Errorf("%s over %s:\n got %v\nwant %v", text, name, binds(got), binds(want))
			}
		}
	}
	// The controls run the planned SemiNaive alone on a fresh Base: both() runs Naive first, whose
	// reads leave the planned run nothing to look up.
	fresh := func(src ns.Source, text string) {
		if _, err := (SemiNaive{}).Eval(bg, mustParse(t, text), MustBase(std(routes()), src)); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
	}
	looked, batched, pushed, streamed = lookingAt(routes()), batchingAt(routes()), pushingAt(routes()), streamingOver(routes())
	fresh(looked, `road("a", ?b) as ?r => ?r`)
	fresh(batched, `town(?t), road(?t, ?b) as ?r => ?t, ?r`)
	fresh(pushed, `road(?a, ?b), road(?b, ?c), road(?c, ?d) as ?r => ?a, ?r`)
	if looked.looked("road") == 0 || len(batched.batches) == 0 {
		t.Errorf("control: road looked up %d times, %d batches", looked.looked("road"), len(batched.batches))
	}
	for _, batch := range batched.batches {
		if !strings.HasPrefix(batch, "road×") {
			t.Errorf("a batch asked for %q, where the store knows only road", batch)
		}
	}
	// A fact relation is never streamed: the goal reads road whole and the answer still holds it.
	rows, err := (SemiNaive{}).Eval(bg, mustParse(t, `road(?a, ?b) as ?r => ?r`), MustBase(std(routes()), streamed))
	if err != nil || len(rows) != 5 {
		t.Errorf("a streamed source's facts: %d rows, %v", len(rows), err)
	}
	if len(pushed.pushed) == 0 {
		t.Errorf("control: nothing pushed")
	}
	for _, c := range pushed.pushed {
		for _, a := range c.Atoms {
			if isFact(a.Relation) {
				t.Errorf("a fact relation pushed: %+v", c)
			}
		}
	}
	if streamed.count("road") == 0 {
		t.Errorf("control: road never streamed")
	}
}

// dealtRoutes is routes() dealt between two members by the road's first town, the ids kept.
func dealtRoutes(t *testing.T) *ns.Federation {
	all := routes()
	var halves [2]*ns.MemSource
	for i := range halves {
		s, _ := all.Schema("dist")
		halves[i] = ns.NewMemSource().Declare("road", "from", "to").Declare("road_attr", "road", "key", "value").Declare("town", "name").DeclareSchema("dist", s)
	}
	for _, r := range all.Tuples("road") {
		i := 0
		if r.Vals[0].S > "b" {
			i = 1
		}
		halves[i].Add("road", r)
	}
	for _, rel := range []string{"road_attr", "town", "dist"} {
		for k, r := range all.Tuples(rel) {
			halves[k%2].Add(rel, r)
		}
	}
	f, err := ns.NewFederation(ns.Member{Source: lookingAt(halves[0])}, ns.Member{Source: halves[1]})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// A version's facts keep their ids, read at one version and across them.
func TestBoundFactsKeepTheirIDsAcrossVersions(t *testing.T) {
	src := ns.NewTimedMemSource(ns.Span(0, 2))
	src.Declare("road", "from", "to")
	src.AddTimed("road", ns.TimedTuple{Vals: []ns.Value{ns.S("a"), ns.S("b")}, Versions: ns.Since(0), ID: "r1"})
	src.AddTimed("road", ns.TimedTuple{Vals: []ns.Value{ns.S("b"), ns.S("c")}, Versions: ns.Since(1), ID: "r2"})
	b := baseFor(std(src))
	if got := factRows(answerOn(t, b, `road(?a, ?b) as ?r => ?r`), "r"); !slices.Equal(got, []string{"r1", "r2"}) {
		t.Errorf("read as a plain source, its last version: %q", got)
	}
	for v, want := range [][]string{{"r1"}, {"r1", "r2"}} {
		if got := factRows(answerOn(t, b, `road(?a, ?b) as ?r => ?r`, At(ns.Version(v))), "r"); !slices.Equal(got, want) {
			t.Errorf("At(%d) = %q, want %q", v, got, want)
		}
	}
	if got := factRows(answerOn(t, b, `road(?a, ?b) as ?r => ?r`, Across(ns.Span(0, 2))), "r"); !slices.Equal(got, []string{"r1", "r2"}) {
		t.Errorf("Across = %q", got)
	}
}

// `as` names a fact only on a positive atom over a base relation; anything else is refused with a
// CheckError a host can place in the text, and a malformed `as` doesn't parse.
func TestAFactWhereNoneIsStoredIsRefused(t *testing.T) {
	src := routes()
	for _, c := range []struct{ text, want, call string }{
		{`town(?a), not road(?a, "b") as ?r => ?a`, `not road as ?r names no fact`, `road(?a, "b")`},
		{`road(?a, ?b), str.prefix(?a, "a") as ?r => ?r`, `str.prefix is a predicate`, `str.prefix(?a, "a")`},
		{`two(?a, ?c) :- road(?a, ?b), road(?b, ?c); two(?a, ?c) as ?r => ?r`, `two is derived`, `two(?a, ?c)`},
	} {
		err := evalErr(src, c.text)
		var ce *CheckError
		if !errors.As(err, &ce) || ce.Kind != KindFact || !strings.Contains(err.Error(), c.want) || ce.Var != "r" {
			t.Errorf("%s: %v (%+v)", c.text, err, ce)
			continue
		}
		if loc, ok := Locate(c.text, err); !ok || c.text[loc.Offset:loc.Offset+len(c.call)] != c.call {
			t.Errorf("%s: Locate = %+v, %v", c.text, loc, ok)
		}
		if err := Validate(mustParse(t, c.text), std(src)); err == nil || err.Error() != evalErr(src, c.text).Error() {
			t.Errorf("%s: Validate says %v", c.text, err)
		}
	}
	for _, text := range []string{`road(?a, ?b) as => ?a`, `road(?a, ?b) as "r" => ?a`, `road(?a, ?b) as ?r ?s => ?a`, `road(?a, ?b) as _ => ?a`} {
		if _, err := Parse(text); err == nil || !strings.Contains(err.Error(), `"as" names the fact`) {
			t.Errorf("Parse(%q) = %v", text, err)
		}
	}
	// control: a fact over a relation a module defines is derived too, and one at the wrong arity is
	// refused as the call would be without its `as`.
	if err := evalErr(src, `road(?a) as ?r => ?r`); err == nil || !strings.Contains(err.Error(), "takes 2 args") {
		t.Errorf("wrong arity: %v", err)
	}
	if err := evalErr(src, `roads(?a, ?b) as ?r => ?r`); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("unknown relation: %v", err)
	}
}

// A module's rules bind facts as a query's do, and a fact over another module's relation is derived.
func TestAModuleBindsFacts(t *testing.T) {
	src := routes()
	v := std(src)
	if err := v.AddModule("trip", LanguageName, `tolled(?a, ?b) :- road(?a, ?b) as ?r, road_attr(?r, "toll", "yes");`, ""); err != nil {
		t.Fatal(err)
	}
	if got := factRows(answerOn(t, baseFor(v), `trip.tolled(?a, ?b) => ?a, ?b`), "a", "b"); !slices.Equal(got, []string{"a b"}) {
		t.Errorf("module answer %q", got)
	}
	w := std(routes())
	if err := w.AddModule("trip", LanguageName, `hop(?a, ?b) :- road(?a, ?b);`, ""); err != nil {
		t.Fatal(err)
	}
	if err := w.AddModule("bad", LanguageName, `x(?r) :- trip.hop(?a, ?b) as ?r;`, ""); err != nil {
		t.Fatal(err)
	}
	if err := w.Check(); err == nil || !strings.Contains(err.Error(), "trip.hop is derived") {
		t.Errorf("a module binding a derived fact: %v", err)
	}
}

// A program binding facts prints back as text that parses to it.
func TestABoundFactPrintsBack(t *testing.T) {
	for _, text := range []string{`road(?a, ?b) as ?r => ?r`, `x(?r) :- road(?a, ?b) as ?r, not road(?b, ?a); x(?r) => ?r`} {
		q := mustParse(t, text)
		again, err := Parse(queryString(q))
		if err != nil || !reflect.DeepEqual(q, again) {
			t.Errorf("%s printed %q: %v", text, queryString(q), err)
		}
	}
}
