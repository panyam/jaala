package datalog

import (
	"reflect"
	"testing"

	"github.com/panyam/jaala/ns"
)

// trackWidths is agni's TestBoardQuery shape: a board serving its track widths in mm, SIG routed thin
// (0.08 mm) and PWR wide (0.5 mm).
func trackWidths() *ns.MemSource {
	src := ns.NewMemSource().DeclareSchema("board.track_width", ns.Schema{Arity: 2, Labels: []string{"net", "w"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}})
	src.Add("board.track_width", ns.Tuple{Vals: []ns.Value{ns.S("SIG"), ns.NU(0.08, "mm")}})
	src.Add("board.track_width", ns.Tuple{Vals: []ns.Value{ns.S("PWR"), ns.NU(0.5, "mm")}})
	return src
}

// A Source's unit is a tag compared by its text unless the vocabulary folds a Source's units (#303), so
// a host serving mm reads a bare threshold in mm, as it did before #205.
func TestASourceUnitIsATagUnlessTheVocabularyFoldsIt(t *testing.T) {
	for _, src := range []struct {
		name string
		src  func() ns.Source
	}{{"held", func() ns.Source { return trackWidths() }}, {"looked up", func() ns.Source { return lookingAt(trackWidths()) }}} {
		// A store compares by the values the engine asks with: folded when the vocabulary folds, else as stored.
		looks := func(s ns.Source, fold bool) ns.Source {
			if l, ok := s.(*looking); ok {
				l.asStored = !fold
			}
			return s
		}
		t.Run(src.name, func(t *testing.T) {
			for _, c := range []struct {
				fold  bool
				query string
				want  []string
			}{
				{false, `board.track_width(?net, ?w), ?w < 0.1 => ?net, ?w`, []string{"SIG 0.08[mm]"}},
				{false, `board.track_width(?net, 0.08) => ?net`, []string{"SIG"}},
				// A query's 0.1mm is folded to metres and the Source's mm isn't: unlike units, no order.
				{false, `board.track_width(?net, ?w), ?w < 0.1mm => ?net`, nil},
				// control: folded, the widths are metres, which the same bare 0.1 compares in.
				{true, `board.track_width(?net, ?w), ?w < 0.1 => ?net, ?w`, []string{"PWR 0.0005[m]", "SIG 8e-05[m]"}},
				{true, `board.track_width(?net, ?w), ?w < 0.1mm => ?net`, []string{"SIG"}},
			} {
				v := std(looks(src.src(), c.fold))
				if c.fold {
					v.FoldSourceUnits()
				}
				rows, err := both(mustParse(t, c.query), baseFor(v))
				if err != nil {
					t.Fatalf("%s: %v", c.query, err)
				}
				var got []string
				for _, r := range rows {
					got = append(got, rowText(r))
				}
				if !reflect.DeepEqual(got, c.want) {
					t.Errorf("fold %v: %s = %q, want %q", c.fold, c.query, got, c.want)
				}
			}
		})
	}
}

// A store is sent the unit table only when the engine folds by it, so a Federation folds what it
// carries between members exactly when the engine reads a Source's values folded (#303).
func TestAPushCarriesTheUnitTableOnlyWhenTheVocabularyFolds(t *testing.T) {
	for _, fold := range []bool{false, true} {
		src := ns.NewMemSource().
			DeclareSchema("probe", ns.Schema{Arity: 2, Labels: []string{"p", "net"}}).
			DeclareSchema("reading", ns.Schema{Arity: 2, Labels: []string{"net", "v"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}})
		src.Add("probe", ns.Tuple{Vals: []ns.Value{ns.S("TP1"), ns.S("VCC")}})
		src.Add("reading", ns.Tuple{Vals: []ns.Value{ns.S("VCC"), ns.NU(3300, "mV")}})
		p := pushingAt(src)
		p.asStored = !fold
		v := std(p)
		if fold {
			v.FoldSourceUnits()
		}
		rows, err := (SemiNaive{}).Eval(bg, mustParse(t, `probe(?p, ?n), reading(?n, ?v) => ?p, ?v`), baseFor(v))
		if err != nil {
			t.Fatal(err)
		}
		if len(p.pushed) == 0 {
			t.Fatalf("fold %v: control: the join was never pushed", fold)
		}
		for _, c := range p.pushed {
			if got := c.Units != nil; got != fold {
				t.Errorf("fold %v: a pushed conjunction carries a unit table: %v", fold, got)
			}
		}
		want := map[bool]string{false: "3300[mV]", true: "3.3[V]"}[fold]
		if len(rows) != 1 || rows[0].Bind["v"].S+"["+rows[0].Bind["v"].BaseUnit+"]" != want {
			t.Errorf("fold %v: answered %v, want TP1 %s", fold, rows, want)
		}
	}
}

// A host's bound values are a query's constants, folded whether or not the vocabulary folds a Source's
// values, so a set holding 1000 mV finds the 1 V a Source serves (#303).
func TestABoundSetIsFoldedLikeAConstant(t *testing.T) {
	src := ns.NewMemSource().DeclareSchema("reading", ns.Schema{Arity: 2, Labels: []string{"p", "v"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}})
	for _, r := range []struct {
		p string
		v float64
	}{{"TP1", 1}, {"TP2", 3.3}, {"TP3", 5}} {
		src.Add("reading", ns.Tuple{Vals: []ns.Value{ns.S(r.p), ns.NU(r.v, "V")}})
	}
	q := mustParse(t, `reading(?p, ?v) => ?p`)
	for _, set := range [][]ns.Value{{ns.NU(1000, "mV"), ns.NU(3300, "mV")}, {ns.NU(1000, "mV")}} {
		want := map[int]string{2: "TP1,TP2", 1: "TP1"}[len(set)]
		rows, err := both(q, baseFor(std(src)), Bind(map[Var][]ns.Value{"v": set}))
		if err != nil || col(rows, "p") != want {
			t.Errorf("bound %v: %q %v, want %q", set, col(rows, "p"), err, want)
		}
	}
}

// rowText is a row's values in column order, a number with its unit in brackets.
func rowText(r Row) string {
	out := ""
	for _, name := range []Var{"net", "w"} {
		v, ok := r.Bind[name]
		if !ok {
			continue
		}
		if out != "" {
			out += " "
		}
		out += v.S
		if v.BaseUnit != "" {
			out += "[" + v.BaseUnit + "]"
		}
	}
	return out
}

// A host names units whose scale isn't a power of ten (#303): durations over seconds and bytes under a
// binary prefix, read in a query's literals and, folded, from a Source.
func TestAHostNamesUnitsWithAnyExactScale(t *testing.T) {
	src := ns.NewMemSource().
		DeclareSchema("build", ns.Schema{Arity: 2, Labels: []string{"b", "d"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}}).
		DeclareSchema("artifact", ns.Schema{Arity: 2, Labels: []string{"a", "n"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}})
	src.Add("build", ns.Tuple{Vals: []ns.Value{ns.S("A"), ns.NU(90, "s")}})
	src.Add("build", ns.Tuple{Vals: []ns.Value{ns.S("B"), ns.NU(1.5, "min")}})
	src.Add("build", ns.Tuple{Vals: []ns.Value{ns.S("C"), ns.NU(30, "s")}})
	src.Add("build", ns.Tuple{Vals: []ns.Value{ns.S("D"), ns.NU(2, "h")}})
	src.Add("artifact", ns.Tuple{Vals: []ns.Value{ns.S("small"), ns.NU(2000, "B")}})
	src.Add("artifact", ns.Tuple{Vals: []ns.Value{ns.S("big"), ns.NU(3, "KiB")}})
	v := std(src)
	for _, u := range [][2]string{{"min", "60*s"}, {"h", "60*min"}, {"B", ""}, {"Ki", "1024"}} {
		if err := v.AddUnit(u[0], u[1]); err != nil {
			t.Fatal(err)
		}
	}
	v.FoldSourceUnits()
	b := baseFor(v)
	for _, c := range []struct{ query, col, want string }{
		{`build(?b, ?d), ?d = 1.5min => ?b`, "b", "A,B"},
		{`build(?b, ?d), ?d > 1min, ?d < 1h => ?b`, "b", "A,B"},
		{`build(?b, 7200) => ?b`, "b", "D"},
		{`build(?b, ?d), ?d >= 120min => ?b`, "b", "D"},
		{`artifact(?a, ?n), ?n >= 2KiB => ?a`, "a", "big"},
		{`artifact(?a, 3072) => ?a`, "a", "big"},
	} {
		rows, err := both(mustParse(t, c.query), b)
		if err != nil {
			t.Fatalf("%s: %v", c.query, err)
		}
		if got := col(rows, c.col); got != c.want {
			t.Errorf("%s = %q, want %q", c.query, got, c.want)
		}
	}
}
