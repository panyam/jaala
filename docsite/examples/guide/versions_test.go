package guide_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/ns"
)

// history serves three revisions of the imports. app imports api
// throughout, api imports log from revision 1 on, and api imported util
// only in revision 0. A fact is its values, so each one is served once with
// the revisions it holds in, and its citations can differ per revision.
func history() *ns.TimedMemSource {
	src := ns.NewTimedMemSource(ns.Span(0, 3))
	src.Declare("imports", "from", "to").Declare("pkg", "name", "license")
	src.Add("imports", ns.Since(0), []string{"app.go:3"}, ns.S("app"), ns.S("api"))
	src.Add("imports", ns.Since(1), []string{"api.go:5"}, ns.S("api"), ns.S("log"))
	src.Add("imports", ns.Bits(0), []string{"api.go:4"}, ns.S("api"), ns.S("util"))
	return src
}

func versionedBase() *datalog.Base {
	src := history()
	v, err := vocabulary(src)
	if err != nil {
		panic(err)
	}
	return datalog.MustBase(v, src)
}

// At answers a query at one revision, over the facts that hold there, as a
// Base over only those facts would. One Base serves every revision.
func Example_at() {
	b, ctx := versionedBase(), context.Background()
	q := datalog.MustParse(`deps.depends_on("app", ?d) => ?d`)
	for _, rev := range []ns.Version{0, 2} {
		rows, _ := datalog.SemiNaive{}.Eval(ctx, q, b, datalog.At(rev))
		fmt.Print(rev, ":")
		for _, r := range rows {
			fmt.Print(" ", r.Bind["d"].S)
		}
		fmt.Println()
	}
	// Output:
	// 0: api util
	// 2: api log
}

// Diff is what changed in a relation between two revisions, each fact with
// the citations of the revision it holds in.
func Example_diff() {
	b, ctx := versionedBase(), context.Background()
	d, _ := b.Diff(ctx, "imports", 0, 2)
	for _, t := range d.Added {
		fmt.Println("+", t.Vals[0].S, t.Vals[1].S, t.Cites)
	}
	for _, t := range d.Removed {
		fmt.Println("-", t.Vals[0].S, t.Vals[1].S, t.Cites)
	}
	// Output:
	// + api log [api.go:5]
	// - api util [api.go:4]
}

// A session's edits are a change set over the last revision, read through
// an overlay before anything is saved: api drops log for util. Effective
// is what the session changes, and a commit keeps it as revision 3, which
// the Base answers at as it does any other.
func Example_session() {
	src, ctx := history(), context.Background()
	v, err := vocabulary(src)
	if err != nil {
		panic(err)
	}
	var edit ns.Changes
	edit.Remove("imports", ns.Tuple{Vals: []ns.Value{ns.S("api"), ns.S("log")}}).
		Add("imports", ns.Tuple{Vals: []ns.Value{ns.S("api"), ns.S("util")}, Cites: []string{"session:1"}})
	session := ns.Overlay(src, edit)
	q := datalog.MustParse(`deps.depends_on("app", ?d) => ?d`)
	rows, _ := datalog.SemiNaive{}.Eval(ctx, q, datalog.MustBase(v, session))
	fmt.Println("session:", depsOf(rows))
	changed, _ := session.Effective(ctx)
	fmt.Println("changes:", len(changed.Added("imports")), "added,", len(changed.Removed("imports")), "removed")
	b := datalog.MustBase(v, src)
	if err := src.Commit(edit, 2, 3); err != nil {
		panic(err)
	}
	for _, rev := range []ns.Version{2, 3} {
		rows, _ := datalog.SemiNaive{}.Eval(ctx, q, b, datalog.At(rev))
		fmt.Println("revision", rev, depsOf(rows))
	}
	// Output:
	// session: [api util]
	// changes: 1 added, 1 removed
	// revision 2 [api log]
	// revision 3 [api util]
}

// A session travels as JSON: a browser keeps it beside its snapshot, and a
// server reads it through an overlay of its own store. Base names the
// revision it was made over, so a server can tell when that has moved.
func Example_sessionWire() {
	var edit ns.Changes
	edit.Remove("imports", ns.Tuple{Vals: []ns.Value{ns.S("api"), ns.S("log")}}).
		Add("imports", ns.Tuple{Vals: []ns.Value{ns.S("api"), ns.S("util")}, Cites: []string{"session:1"}})
	data, _ := json.Marshal(ns.Session{Base: "2", Changes: edit})
	fmt.Println(string(data))
	var back ns.Session
	if err := json.Unmarshal(data, &back); err != nil {
		panic(err)
	}
	fmt.Println(back.Base, len(back.Changes.Added("imports")), "added")
	// Output:
	// {"base":"2","changes":{"format":1,"ops":[{"rel":"imports","op":"remove","vals":["api","log"]},{"rel":"imports","op":"add","vals":["api","util"],"cites":["session:1"]}]}}
	// 2 1 added
}

func depsOf(rows []datalog.Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Bind["d"].S)
	}
	return out
}

// Across evaluates once over a range of revisions, and each row carries
// the revisions it holds in, so "since when does app depend on log" is one
// query rather than one per revision.
func Example_across() {
	b, ctx := versionedBase(), context.Background()
	q := datalog.MustParse(`deps.depends_on("app", ?d) => ?d`)
	rows, _ := datalog.SemiNaive{}.Eval(ctx, q, b, datalog.Across(ns.Span(0, 3)))
	for _, r := range rows {
		fmt.Println(r.Bind["d"].S, slices.Collect(r.Versions.Ranges()))
	}
	// Output:
	// api [{0 3}]
	// log [{1 3}]
	// util [{0 1}]
}

// An aggregate across revisions is cut where its inputs change, so a
// count per revision is one query. app has two dependencies in every
// revision, but not the same two, so the count is two rows citing
// different facts.
func Example_acrossCount() {
	b, ctx := versionedBase(), context.Background()
	q := datalog.MustParse(`deps.depends_on("app", ?d) => count(?d)`)
	rows, _ := datalog.SemiNaive{}.Eval(ctx, q, b, datalog.Across(ns.Span(0, 3)))
	for _, r := range rows {
		fmt.Println(r.Bind["count(d)"].S, slices.Collect(r.Versions.Ranges()))
	}
	// Output:
	// 2 [{0 1}]
	// 2 [{1 3}]
}

// revisions is history served a revision at a time as a store of its own
// (ns.SourceAt), the way a host keeps revisions in a database: a question
// at a revision asks that revision's store for what it binds.
type revisions struct{ *ns.TimedMemSource }

func (r revisions) SourceAt(v ns.Version) (ns.Source, error) {
	return revisionStore{r.TimedMemSource, v}, nil
}

// LookupTimed answers a bound call across every revision at once
// (ns.TimedLookupSource), each fact with the revisions it holds in, which
// is what Across asks. A real store would ask its database for the rows
// with their revision ranges.
func (r revisions) LookupTimed(ctx context.Context, rel string, bound map[int]ns.Value) ([]ns.TimedTuple, error) {
	fmt.Println("history looks up", rel, bound[0].S)
	all, err := r.TimedTuples(ctx, rel)
	var out []ns.TimedTuple
	for _, t := range all {
		if v, ok := bound[0]; !ok || t.Vals[0].S == v.S {
			out = append(out, t)
		}
	}
	return out, err
}

// revisionStore is one revision's facts, answering lookups. A real store
// would ask its database for the revision's rows.
type revisionStore struct {
	*ns.TimedMemSource
	v ns.Version
}

func (s revisionStore) Tuples(rel string) []ns.Tuple {
	all, _ := s.TimedTuples(context.Background(), rel)
	var out []ns.Tuple
	for _, t := range all {
		if t.Versions.Contains(s.v) {
			out = append(out, ns.Tuple{Vals: t.Vals, Cites: t.CitesAt(s.v)})
		}
	}
	return out
}

func (s revisionStore) Lookup(_ context.Context, rel string, bound map[int]ns.Value) ([]ns.Tuple, error) {
	fmt.Println("revision", s.v, "looks up", rel, bound[0].S)
	var out []ns.Tuple
	for _, t := range s.Tuples(rel) {
		if v, ok := bound[0]; !ok || t.Vals[0].S == v.S {
			out = append(out, t)
		}
	}
	return out, nil
}

// A timed source that serves each revision as a store is asked at a
// revision as any store is, and a Federation of it with a source that
// doesn't change between revisions (licenses here) answers At over both.
func Example_atFromStores() {
	licenses := ns.NewMemSource()
	licenses.Declare("pkg", "name", "license")
	licenses.Add("pkg", ns.Tuple{Vals: []ns.Value{ns.S("log"), ns.S("MIT")}})
	f, _ := ns.NewFederation(ns.Member{Source: revisions{history()}}, ns.Member{Source: licenses})
	tf, _ := f.Timed()
	v, _ := vocabulary(tf)
	b, ctx := datalog.MustBase(v, tf), context.Background()
	q := datalog.MustParse(`imports("api", ?d), pkg(?d, ?l) => ?d, ?l`)
	rows, _ := datalog.SemiNaive{}.Eval(ctx, q, b, datalog.At(2))
	for _, r := range rows {
		fmt.Println(r.Bind["d"].S, r.Bind["l"].S)
	}
	// Output:
	// revision 2 looks up imports api
	// log MIT
}

// Across over a timed source that looks up across revisions asks it for
// what the query binds, rather than reading every revision's facts whole.
// The rows hold in the revisions they do when read whole.
func Example_acrossFromStores() {
	src := revisions{history()}
	v, _ := vocabulary(src)
	b, ctx := datalog.MustBase(v, src), context.Background()
	q := datalog.MustParse(`deps.depends_on("app", ?d) => ?d`)
	rows, _ := datalog.SemiNaive{}.Eval(ctx, q, b, datalog.Across(ns.Span(0, 3)))
	for _, r := range rows {
		fmt.Println(r.Bind["d"].S, slices.Collect(r.Versions.Ranges()))
	}
	// Output:
	// history looks up imports app
	// history looks up imports api
	// history looks up imports log
	// history looks up imports util
	// api [{0 3}]
	// log [{1 3}]
	// util [{0 1}]
}
