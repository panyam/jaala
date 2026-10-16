package guide_test

import (
	"context"
	"fmt"
	"slices"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

// lines is how big each package is, which the host knows and doesn't serve
// as facts.
var lines = map[string]int{"app": 120, "api": 900, "log": 40, "util": 300}

// predicates registers a filter and a generator beside the guide's
// vocabulary.
func predicates() (*ns.Vocabulary, error) {
	src := source()
	v, err := ns.NewVocabulary(src)
	if err != nil {
		return nil, err
	}
	if err := v.AddLanguage(datalog.Language); err != nil {
		return nil, err
	}
	if err := stdlib.Register(v); err != nil {
		return nil, err
	}
	// A filter tests arguments that are all bound.
	err = v.AddPredicate("lic.permissive", ns.Builtin{
		Arity: 1,
		Doc:   "the license lets anyone reuse the code",
		Holds: func(args []ns.Value) (bool, error) {
			return slices.Contains([]string{"MIT", "Apache-2.0"}, args[0].S), nil
		},
	})
	if err != nil {
		return nil, err
	}
	// A generator produces values. It runs with the package bound, or with
	// nothing bound, listing every package; its Types read the count as a
	// number, so ?n > 100 compares numbers.
	err = v.AddPredicate("size.lines", ns.Builtin{
		Arity:  2,
		Labels: []string{"pkg", "lines"},
		Types:  []ns.ArgType{{}, {Type: ns.TypeNumber}},
		Modes:  [][]bool{{true, false}, {false, false}},
		Doc:    "how many lines a package has",
		Gen: func(ctx context.Context, _ ns.Source, args []ns.Arg, emit func([]ns.Value, []string) error) error {
			for _, pkg := range []string{"app", "api", "log", "util"} {
				if args[0].Bound && args[0].Value.S != pkg {
					continue
				}
				if err := emit([]ns.Value{ns.S(pkg), ns.N(float64(lines[pkg]))}, []string{"sloc:" + pkg}); err != nil {
					return err
				}
			}
			return ctx.Err()
		},
	})
	if err != nil {
		return nil, err
	}
	if err := v.AddModule("deps", datalog.LanguageName, deps, "deps.dl"); err != nil {
		return nil, err
	}
	return v, v.Check()
}

func predicateRows(query string) {
	v, err := predicates()
	if err != nil {
		panic(err)
	}
	q := datalog.MustParse(query)
	rows, err := datalog.SemiNaive{}.Eval(context.Background(), q, datalog.MustBase(v, source()))
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, r := range rows {
		var vals []string
		for _, col := range q.Select {
			vals = append(vals, r.Bind[col.Var].S)
		}
		fmt.Println(vals, r.Cites)
	}
}

// A filter keeps a binding when Holds says so, and `not` keeps it when Holds
// says no, so the two can't disagree.
func Example_filter() {
	predicateRows(`pkg(?p, ?l), lic.permissive(?l) => ?p`)
	predicateRows(`pkg(?p, ?l), not lic.permissive(?l) => ?p`)
	// Output:
	// [app] []
	// [util] []
	// [api] []
	// [log] []
}

// A generator runs once one of its modes is satisfied, wherever it's
// written: here depends_on binds ?d first, so size.lines is asked about each
// dependency rather than listing every package. Each answer cites what the
// generator emitted.
func Example_generator() {
	predicateRows(`size.lines(?d, ?n), deps.depends_on("app", ?d), ?n > 100 => ?d, ?n`)
	// Output:
	// [api 900] [sloc:api]
	// [util 300] [sloc:util]
}

// A generator must say which bindings it runs under, so a scan of
// everything is a declaration rather than an accident.
func Example_generatorNeedsModes() {
	v, _ := ns.NewVocabulary(source())
	err := v.AddPredicate("size.lines", ns.Builtin{
		Arity: 2,
		Gen: func(context.Context, ns.Source, []ns.Arg, func([]ns.Value, []string) error) error {
			return nil
		},
	})
	fmt.Println(err)
	// Output:
	// query: generator "size.lines" declares no Modes; list the binding patterns it accepts (an all-false mode if it may enumerate with nothing bound)
}
