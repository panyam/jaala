package datalog

import (
	"context"
	"fmt"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
	"strings"
	"testing"
)

// vocabulary is graph()'s relations, the standard predicates, a generator and a module, registered
// with no Source: what a host composes once and binds per dataset.
func vocabulary(t *testing.T) *ns.Vocabulary {
	t.Helper()
	r := ns.MustVocabulary(nil)
	if err := r.AddLanguage(Language); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []struct {
		path   string
		labels []string
	}{{"edge", []string{"from", "to"}}, {"node", []string{"name"}}, {"weight", []string{"node", "w"}}} {
		if err := r.AddRelation(rel.path, ns.Schema{Arity: len(rel.labels), Labels: rel.labels}); err != nil {
			t.Fatal(err)
		}
	}
	if err := stdlib.Register(r); err != nil {
		t.Fatal(err)
	}
	if err := r.AddPredicate("succ", ns.Builtin{Arity: 2, Modes: [][]bool{{true, false}}, Gen: func(_ context.Context, src ns.Source, args []ns.Arg, emit func([]ns.Value, []string) error) error {
		for _, tu := range src.Tuples("edge") {
			if args[0].Bound && tu.Vals[0].S != args[0].Value.S {
				continue
			}
			if err := emit(tu.Vals, nil); err != nil {
				return err
			}
		}
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	if err := r.AddModule("hop", LanguageName, reachModule, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.Check(); err != nil {
		t.Fatal(err)
	}
	return r
}

// another is graph()'s shape with different facts: x -> y -> z.
func another() *ns.MemSource {
	src := ns.NewMemSource().Declare("edge", "from", "to").Declare("node", "name").Declare("weight", "node", "w")
	for _, e := range [][2]string{{"x", "y"}, {"y", "z"}} {
		src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(e[0]), ns.S(e[1])}})
	}
	return src
}

func over(t *testing.T, r *ns.Vocabulary, src ns.Source) *Base {
	t.Helper()
	b, err := NewBase(r, src)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOneVocabularyAnswersOverManySourcesAndResolvesOnce(t *testing.T) {
	before := resolutions.Load()
	r := vocabulary(t)
	first, second := over(t, r, graph()), over(t, r, another())
	for _, c := range []struct {
		b         *Base
		from, got string
	}{{first, "a", "b,c,d"}, {second, "x", "y,z"}, {second, "a", ""}} {
		rows, err := Naive{}.Eval(bg, mustParse(t, `hop.reach("`+c.from+`", ?n) => ?n`), c.b)
		if err != nil || col(rows, "n") != c.got {
			t.Errorf("reach from %s = %v, %v; want %q", c.from, rows, err, c.got)
		}
	}
	if _, err := r.Lookup("hop.reach"); err != nil {
		t.Fatal(err)
	}
	if n := resolutions.Load() - before; n != 1 {
		t.Errorf("modules resolved %d times across Check, two Bases, three queries and a Lookup, want once", n)
	}
}

func TestAGeneratorReadsItsBasesSource(t *testing.T) {
	r := vocabulary(t)
	for _, c := range []struct {
		src  ns.Source
		want string
	}{{graph(), ""}, {another(), "y"}} {
		rows, err := Naive{}.Eval(bg, mustParse(t, `succ("x", ?n) => ?n`), over(t, r, c.src))
		if err != nil || col(rows, "n") != c.want {
			t.Errorf("succ from x = %v, %v; want %q", rows, err, c.want)
		}
	}
}

func TestNewBaseRefusesASourceThatDisagrees(t *testing.T) {
	r := vocabulary(t)
	for name, c := range map[string]struct {
		src  ns.Source
		want string
	}{
		"missing": {ns.NewMemSource().Declare("edge", "from", "to"), `query: the source does not serve node, weight`},
		"arity":   {another().Declare("edge", "from", "to", "label"), `edge takes 2 args in the vocabulary and 3 in the source`},
		"nil":     {nil, `the source does not serve edge, node, weight`},
	} {
		if _, err := NewBase(r, c.src); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if _, err := NewBase(r, another().Declare("extra", "x")); err != nil {
		t.Errorf("a relation beyond the registry's: err = %v, want it ignored", err)
	}
}

func TestBasesOverDifferentSourcesEvaluateConcurrently(t *testing.T) {
	r := vocabulary(t)
	bases := []*Base{over(t, r, graph()), over(t, r, another())}
	want := []string{"b,c,d", ""}
	q := mustParse(t, `hop.reach("a", ?n) => ?n`)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func(i int) {
			rows, err := Naive{}.Eval(bg, q, bases[i%2])
			if err == nil && col(rows, "n") != want[i%2] {
				err = fmt.Errorf("base %d answered %v", i%2, rows)
			}
			errs <- err
		}(i)
	}
	for i := 0; i < 8; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}
