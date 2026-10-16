package datalog

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/panyam/jaala/ns"
)

// historyOf deals src's facts across n linear versions as a host's revisions would change them: most
// facts hold throughout, and about churn of them each start at a later version or stop at an earlier
// one.
func historyOf(src *ns.MemSource, n int, churn float64) *ns.TimedMemSource {
	rnd := rand.New(rand.NewSource(1))
	timed := ns.NewTimedMemSource(ns.Span(0, ns.Version(n)))
	for _, rel := range src.Relations() {
		s, _ := src.Schema(rel)
		timed.DeclareSchema(rel, s)
		for _, t := range src.Tuples(rel) {
			vs := ns.Since(0)
			switch x := rnd.Float64(); {
			case x < churn/2:
				vs = ns.Since(ns.Version(1 + rnd.Intn(n-1)))
			case x < churn:
				vs = ns.Span(0, ns.Version(1+rnd.Intn(n-1)))
			}
			timed.Add(rel, vs, t.Cites, t.Vals...)
		}
	}
	return timed
}

// The cost of one Across evaluation over 50 versions against one At evaluation per version (#180), as
// work and time, over points-to with 4% of its facts changing somewhere in the history.
func BenchmarkVersions(b *testing.B) {
	const n = 50
	q := MustParse(pointsTo + `pt(?v, ?o) => ?v, ?o`)
	src := historyOf(pointerProgram(1), n, 0.04)
	v := std(src)
	b.Run("across", func(b *testing.B) {
		var work int64
		for b.Loop() {
			base := MustBase(v, src)
			if _, err := (SemiNaive{}).Eval(bg, q, base, Across(ns.Span(0, n))); err != nil {
				b.Fatal(err)
			}
			work = base.Work()
		}
		b.ReportMetric(float64(work), "work")
	})
	b.Run(fmt.Sprintf("at-each-of-%d", n), func(b *testing.B) {
		var work int64
		for b.Loop() {
			base := MustBase(v, src)
			for at := range ns.Version(n) {
				if _, err := (SemiNaive{}).Eval(bg, q, base, At(at)); err != nil {
					b.Fatal(err)
				}
			}
			work = base.Work()
		}
		b.ReportMetric(float64(work), "work")
	})
}
