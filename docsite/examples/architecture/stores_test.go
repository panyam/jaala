package architecture_test

import (
	"context"
	"fmt"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/ns"
)

// looking is chain() behind a store that answers lookups. A sloppy one
// returns the whole relation for every lookup, which the contract allows:
// a store may send more than matches, never fewer.
type looking struct {
	*ns.MemSource
	sloppy bool
}

func (s looking) Lookup(_ context.Context, rel string, bound map[int]ns.Value) ([]ns.Tuple, error) {
	var out []ns.Tuple
	for _, t := range s.Tuples(rel) {
		if v, ok := bound[0]; s.sloppy || !ok || t.Vals[0].S == v.S {
			out = append(out, t)
		}
	}
	return out, nil
}

// A store's precision costs what it ships, never the answer: the sloppy
// store answers the same nine rows, having sent every fact for every
// lookup, since jaala checks each fact it gets back.
func Example_aSloppyStoreCostsShippingNotAnswers() {
	q := datalog.MustParse(dependsOn + `depends_on("p190", ?d) => ?d`)
	for _, sloppy := range []bool{false, true} {
		src := looking{chain(), sloppy}
		v, _ := ns.NewVocabulary(src)
		b, _ := datalog.NewBase(v, src)
		var r datalog.Report
		rows, err := datalog.SemiNaive{}.Eval(context.Background(), q, b, datalog.Explain(&r))
		if err != nil {
			panic(err)
		}
		fmt.Printf("sloppy %v: %d rows, %d lookups fetching %d facts\n", sloppy, len(rows), r.Lookups, r.Fetched)
	}
	// Output:
	// sloppy false: 9 rows, 10 lookups fetching 9 facts
	// sloppy true: 9 rows, 10 lookups fetching 1990 facts
}
