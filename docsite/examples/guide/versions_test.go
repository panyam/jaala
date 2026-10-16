package guide_test

import (
	"context"
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
