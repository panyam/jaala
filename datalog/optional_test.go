package datalog

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// randomEdges is a graph of up to six nodes, n0 to n5, with up to eight edges drawn from seed, and a
// node relation holding every node, so some have no edge out.
func randomEdges(seed int64) (*ns.MemSource, [][2]string) {
	rnd := rand.New(rand.NewSource(seed))
	src := ns.NewMemSource().Declare("edge", "from", "to").Declare("node", "name")
	n := 2 + rnd.Intn(5)
	for i := 0; i < n; i++ {
		src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprint("n", i))}})
	}
	seen := map[[2]string]bool{}
	var edges [][2]string
	for i := rnd.Intn(9); i > 0; i-- {
		e := [2]string{fmt.Sprint("n", rnd.Intn(n)), fmt.Sprint("n", rnd.Intn(n))}
		if !seen[e] {
			seen[e] = true
			edges = append(edges, e)
			src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(e[0]), ns.S(e[1])}, Cites: []string{e[0] + "-" + e[1]}})
		}
	}
	return src, edges
}

// optRows is rows as text, a value absent written "-".
func optRows(rows []Row, vars ...Var) []string {
	var out []string
	for _, r := range rows {
		var vals []string
		for _, v := range vars {
			val := r.Bind[v]
			if val.Absent {
				vals = append(vals, "-")
			} else {
				vals = append(vals, val.S)
			}
		}
		out = append(out, strings.Join(vals, " "))
	}
	sort.Strings(out)
	return out
}

func sorted(rows map[string]bool) []string {
	var out []string
	for r := range rows {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// Each optional answers what a left outer join written in Go answers, over random graphs, in every
// evaluator and under CanonicalCites: a row per match, and one with what only the braces bind absent
// when nothing matches, in a goal, a rule, a module, with a filter in the braces, and two in turn.
func TestAnOptionalIsALeftOuterJoin(t *testing.T) {
	isolated := 0
	for seed := int64(1); seed <= 40; seed++ {
		src, edges := randomEdges(seed)
		var nodes []string
		for _, tu := range src.Tuples("node") {
			nodes = append(nodes, tu.Vals[0].S)
		}
		out := func(n string) []string {
			var to []string
			for _, e := range edges {
				if e[0] == n {
					to = append(to, e[1])
				}
			}
			return to
		}
		in := func(n string) []string {
			var from []string
			for _, e := range edges {
				if e[1] == n {
					from = append(from, e[0])
				}
			}
			return from
		}
		one, two, filtered, both2 := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, n := range nodes {
			ms := out(n)
			if len(ms) == 0 {
				one[n+" -"] = true
				isolated++
			}
			var twoStep, notSelf []string
			for _, m := range ms {
				one[n+" "+m] = true
				if len(out(m)) > 0 {
					twoStep = append(twoStep, m)
				}
				if m != n {
					notSelf = append(notSelf, m)
				}
			}
			for _, rows := range []struct {
				set map[string]bool
				ms  []string
			}{{two, twoStep}, {filtered, notSelf}} {
				if len(rows.ms) == 0 {
					rows.set[n+" -"] = true
				}
				for _, m := range rows.ms {
					rows.set[n+" "+m] = true
				}
			}
			ks := in(n)
			for _, m := range append(ms, "") {
				if m == "" && len(ms) > 0 {
					continue
				}
				if m == "" {
					m = "-"
				}
				if len(ks) == 0 {
					both2[n+" "+m+" -"] = true
				}
				for _, k := range ks {
					both2[n+" "+m+" "+k] = true
				}
			}
		}
		b := baseFor(std(src))
		for _, c := range []struct {
			q    string
			vars []Var
			want map[string]bool
		}{
			{`node(?n), optional { edge(?n, ?m) } => ?n, ?m`, []Var{"n", "m"}, one},
			{`r(?n, ?m) :- node(?n), optional { edge(?n, ?m) }; r(?n, ?m) => ?n, ?m`, []Var{"n", "m"}, one},
			{`node(?n), optional { edge(?n, ?m), edge(?m, _) } => ?n, ?m`, []Var{"n", "m"}, two},
			{`node(?n), optional { edge(?n, ?m), ?m != ?n } => ?n, ?m`, []Var{"n", "m"}, filtered},
			{`node(?n), optional { edge(?n, ?m) }, optional { edge(?k, ?n) } => ?n, ?m, ?k`, []Var{"n", "m", "k"}, both2},
		} {
			rows, err := both(mustParse(t, c.q), b, Witnesses())
			if err != nil {
				t.Fatalf("seed %d: %s: %v", seed, c.q, err)
			}
			if got, want := optRows(rows, c.vars...), sorted(c.want); !reflect.DeepEqual(got, want) {
				t.Errorf("seed %d: %s:\n got  %v\n want %v (edges %v)", seed, c.q, got, want, edges)
			}
		}
		v := std(src)
		if err := v.AddModule("m", LanguageName, `out(?n, ?m) :- node(?n), optional { edge(?n, ?m) };`, ""); err != nil {
			t.Fatal(err)
		}
		rows, err := both(mustParse(t, `m.out(?n, ?m) => ?n, ?m`), baseFor(v))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := optRows(rows, "n", "m"), sorted(one); !reflect.DeepEqual(got, want) {
			t.Errorf("seed %d: in a module:\n got  %v\n want %v", seed, got, want)
		}
	}
	if isolated == 0 {
		t.Error("control: no graph had a node with no edge out, so no row was absent")
	}
}

// Declaire's x10 in one rule (#282): a line outside every function keeps its row, with the function
// absent, where the program it replaces wrote a second rule, a not and "" for it. The braces call a
// generator, which reads what the domain binds.
func TestAnOptionalWritesX10InOneRule(t *testing.T) {
	src := ns.NewMemSource().Declare("hit", "file", "line").Declare("fn", "name", "file", "from", "to")
	for _, h := range [][2]any{{"a.go", 3}, {"a.go", 12}, {"b.go", 7}} {
		src.Add("hit", ns.Tuple{Vals: []ns.Value{ns.S(h[0].(string)), ns.N(float64(h[1].(int)))}})
	}
	for _, f := range []struct {
		name, file string
		from, to   int
	}{{"main", "a.go", 1, 10}, {"run", "b.go", 5, 9}} {
		src.Add("fn", ns.Tuple{Vals: []ns.Value{ns.S(f.name), ns.S(f.file), ns.N(float64(f.from)), ns.N(float64(f.to))}})
	}
	one := `place(?u, ?l, ?f) :- hit(?u, ?l), optional { fn(?f, ?u, ?a, ?z), ?a <= ?l, ?l <= ?z }; place(?u, ?l, ?f) => ?u, ?l, ?f`
	two := `place(?u, ?l, ?f) :- hit(?u, ?l), fn(?f, ?u, ?a, ?z), ?a <= ?l, ?l <= ?z;
	        place(?u, ?l, "") :- hit(?u, ?l), not inside(?u, ?l);
	        inside(?u, ?l) :- hit(?u, ?l), fn(_, ?u, ?a, ?z), ?a <= ?l, ?l <= ?z;
	        place(?u, ?l, ?f) => ?u, ?l, ?f`
	got := optRows(eval(t, src, one), "u", "l", "f")
	want := optRows(eval(t, src, two), "u", "l", "f")
	for i := range want {
		want[i] = strings.TrimSuffix(want[i], " ") + map[bool]string{true: " -", false: ""}[strings.HasSuffix(want[i], " ")]
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) || len(got) != 3 {
		t.Errorf("one rule %v, two rules %v", got, want)
	}
}

// A matched row cites the facts its braces matched, and an empty one cites none of them.
func TestAnOptionalsRowCitesWhatItMatched(t *testing.T) {
	rows, err := both(mustParse(t, `node(?n), optional { edge(?n, ?m) }, ?n > "b" => ?n, ?m`), baseFor(std(graph())), Witnesses(), CanonicalCites())
	if err != nil {
		t.Fatal(err)
	}
	cites := map[string][]string{}
	for _, r := range rows {
		cites[r.Bind["n"].S] = r.Cites
	}
	if want := []string{"edge:cd", "node:c"}; !reflect.DeepEqual(cites["c"], want) {
		t.Errorf("c cites %v, want %v", cites["c"], want)
	}
	if want := []string{"node:x"}; !reflect.DeepEqual(cites["x"], want) {
		t.Errorf("x, with no edge, cites %v, want %v", cites["x"], want)
	}
}

// What an optional binds is typed as its braces bind it, the absent of its empty clause aside.
func TestAnOptionalsVariableIsTypedByItsBraces(t *testing.T) {
	src := ns.NewMemSource().Declare("node", "name").
		DeclareSchema("size", ns.Schema{Arity: 2, Labels: []string{"node", "n"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}})
	for i, n := range []string{"a", "b", "c"} {
		src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S(n)}})
		if i > 0 {
			src.Add("size", ns.Tuple{Vals: []ns.Value{ns.S(n), ns.N(float64(10 * i))}})
		}
	}
	got, err := ColumnKinds(mustParse(t, `node(?n), optional { size(?n, ?s) } => ?s`), std(src))
	if err != nil || len(got) != 1 || got[0].Type != ns.TypeNumber {
		t.Errorf("%+v, %v; want a number", got, err)
	}
	// and a rule's: the number 20 is the constant 020, read as the column's number
	if got := col(eval(t, src, `r(?n, ?s) :- node(?n), optional { size(?n, ?s) }; r(?n, ?s), ?s = "020" => ?n`), "n"); got != "c" {
		t.Errorf(`?s = "020" over r: %s, want c`, got)
	}
}

// Count reads absent as jaala does anywhere: count(?m) counts bindings, an empty optional's one too,
// and count(distinct ?m) skips it (#282 left jaala's own rules alone; Cypher's compiler writes its).
func TestCountingAnOptionalsVariableCountsAsAnywhere(t *testing.T) {
	rows := eval(t, graph(), `node(?n), optional { edge(?n, ?m) }, ?n = "x" => ?n, count(?m), count(distinct ?m)`)
	if len(rows) != 1 || rows[0].Bind["count(m)"].S != "1" || rows[0].Bind["count(distinct m)"].S != "0" {
		t.Errorf("rows %v", rows)
	}
}

func TestAnOptionalIsRefusedWhereItCantHold(t *testing.T) {
	src := graph()
	for _, c := range []struct{ q, want string }{
		{`optional { edge(?a, ?b) } => ?a`, `query: optional { edge(?a, ?b) } needs a relation outside its braces to match first`},
		{`node(?n), optional { edge(?n, ?m), optional { edge(?m, ?k) } } => ?n`, `holds another optional in its braces`},
		{`node(?n), optional { ?m > 1 } => ?n, ?m`, `query: optional { ?m > 1 } reads ?m, which no relation binds`},
		{`r(?n) :- node(?n), optional { r(?m), edge(?n, ?m) }; r(?n)`, `not stratifiable`},
		{`node(?n), optional { edge(?n, ?m) } => ?n, ?k`, `?k`},
	} {
		err := evalErr(src, c.q)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s:\n err  %v\n want %s", c.q, err, c.want)
			continue
		}
		if verr := Validate(mustParse(t, c.q), std(src)); verr == nil || verr.Error() != err.Error() {
			t.Errorf("%s: Validate says %v, Eval %v", c.q, verr, err)
		}
		var ce *CheckError
		if !errors.As(err, &ce) {
			t.Errorf("%s: %T is no CheckError", c.q, err)
		}
	}
}

func TestAnOptionalParsesAndPrintsBack(t *testing.T) {
	q := mustParse(t, `r(?n, ?m) :- node(?n), optional {edge(?n, ?m), ?m != "a"}; r(?n, ?m)`)
	if got, want := q.Rules[0].String(), `r(?n, ?m) :- node(?n), optional { edge(?n, ?m), ?m != "a" }`; got != want {
		t.Errorf("prints %s, want %s", got, want)
	}
	back := mustParse(t, q.Rules[0].String()+"; r(?n, ?m)")
	if !reflect.DeepEqual(back.Rules[0].Body, q.Rules[0].Body) {
		t.Errorf("reads back as %v", back.Rules[0].Body)
	}
	for _, s := range []string{`node(?n), optional { edge(?n, ?m)`, `node(?n), optional { }`} {
		if _, err := Parse(s); err == nil || !strings.HasPrefix(err.Error(), "query: ") {
			t.Errorf("%s: %v", s, err)
		}
	}
	// a relation named optional is still a relation
	if _, err := Parse(`optional(?a)`); err != nil {
		t.Errorf("optional(?a): %v", err)
	}
}

// Arithmetic after an optional reads what its braces bind, and computes nothing from absent, as num.*
// computes nothing from absent anywhere (the corpus found the rewrite missing the expression's
// variable, and refusing the call as unbound).
func TestArithmeticReadsAnOptionalsVariable(t *testing.T) {
	rows := eval(t, graph(), `node(?n), optional { weight(?n, ?w), ?w > 3 }, ?d = ?w + 1 => ?n, ?d`)
	if got := optRows(rows, "n", "d"); !reflect.DeepEqual(got, []string{"d 5", "x 6"}) {
		t.Errorf("rows %v", got)
	}
}

// Over a store that evaluates conjunctions, the optional's domain rule and its clause push the same
// two atoms at different written places; each keeps its own, so the clause's witness lists its
// children as written (the corpus found the clause splicing weight in at the domain's place, before
// the optional).
func TestAnOptionalsWitnessIsInWrittenOrderOverAPushingStore(t *testing.T) {
	src := ns.NewMemSource().Declare("edge", "from", "to").Declare("node", "name").Declare("weight", "node", "w")
	src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S("v0")}, Cites: []string{"node:0"}})
	src.Add("weight", ns.Tuple{Vals: []ns.Value{ns.S("v0"), ns.N(5)}})
	q := mustParse(t, `r0(?x7, ?x7, ?x7) :- node(?x7), optional { weight(_, ?x8) }, weight(?x7, ?x8); r0(?x9, _, ?x10) => ?x9`)
	b := MustBase(std(src), src)
	order := func(rows []Row) string {
		var rels []string
		for _, c := range rows[0].Witness[0].Children {
			rels = append(rels, c.Relation)
		}
		return strings.Join(rels, ",")
	}
	canon := []Option{CanonicalCites(), Witnesses()}
	want, err := Naive{}.Eval(bg, q, b, canon...)
	if err != nil || order(want) != "node,optional,weight" {
		t.Fatalf("control: Naive %v, %v", want, err)
	}
	pushing := pushingAt(src)
	got, err := SemiNaive{}.Eval(bg, q, MustBase(b.reg, pushing), canon...)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("pushed: %s, want %s (%v)", order(got), order(want), err)
	}
	if !slices.ContainsFunc(pushing.pushed, func(c ns.Conjunction) bool { return len(c.Atoms) > 1 }) {
		t.Error("control: nothing pushed two atoms")
	}
}

// A second optional computing from what the first binds reads it, and is absent with it: the first's
// rewrite has to see the variable inside the second's expression.
func TestAnOptionalComputesFromAnEarlierOne(t *testing.T) {
	rows := eval(t, graph(), `node(?n), optional { edge(?n, ?m) }, optional { ?u = str.upper(?m) } => ?n, ?u`)
	if got := optRows(rows, "n", "u"); !reflect.DeepEqual(got, []string{"a B", "b C", "c D", "d -", "x -"}) {
		t.Errorf("rows %v", got)
	}
}

// An optional hands on everything its braces bind, so a count of the clause's bindings counts each
// match, as it does over a join: node a has two edges out once c->a is added, x none.
func TestAnOptionalCountsEachMatch(t *testing.T) {
	src := graph()
	src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S("a"), ns.S("c")}})
	rows := eval(t, src, `node(?n), optional { edge(?n, ?m) } => ?n, count(?n)`)
	join := eval(t, src, `node(?n), edge(?n, ?m) => ?n, count(?n)`)
	if got := optRows(rows, "n", "count(n)"); !reflect.DeepEqual(got, []string{"a 2", "b 1", "c 1", "d 1", "x 1"}) {
		t.Errorf("rows %v", got)
	}
	if got := optRows(join, "n", "count(n)"); !reflect.DeepEqual(got, []string{"a 2", "b 1", "c 1"}) {
		t.Errorf("control: the join counts %v", got)
	}
}

// A recursive clause can match optionally: when the braces bind what they share themselves, the
// test for a match reads only them, so the recursion doesn't run through its negation (the corpus
// found has reading the domain, and the domain the clause's own relation).
func TestARecursiveClauseMatchesOptionally(t *testing.T) {
	rows := eval(t, graph(), `r(?n, ?w) :- node(?n), ?n = "a", weight(?n, ?w); r(?m, ?w) :- r(?n, _), edge(?n, ?m), optional { weight(?m, ?w), ?w > 2 }; r(?n, ?w) => ?n, ?w`)
	if got := optRows(rows, "n", "w"); !reflect.DeepEqual(got, []string{"a 1", "b -", "c 3", "d 4"}) {
		t.Errorf("rows %v", got)
	}
}

// What an optional leaves absent reads as absent does anywhere in jaala (#282's null rules, which a
// front end with other rules writes into its program): no order, so an ordering comparison drops the
// row; equal only to absent; and each aggregate skips it but count, which counts bindings.
func TestAnOptionalsAbsentReadsAsAbsentAnywhere(t *testing.T) {
	src := graph()
	opt := `node(?n), optional { weight(?n, ?w), ?w > 3 }`
	for _, c := range []struct {
		q, col, want string
	}{
		{opt + ` => count(?w)`, "count(w)", "5"},
		{opt + ` => count(distinct ?w)`, "count(distinct w)", "2"},
		{opt + ` => sum(?w)`, "sum(w)", "9"},
		{opt + ` => min(?w)`, "min(w)", "4"},
		{opt + ` => max(?w)`, "max(w)", "5"},
		{opt + ` => avg(?w)`, "avg(w)", "4.5"},
		{opt + ` => list(?w)`, "list(w)", "4 5"},
		{opt + `, ?w < 10 => ?n`, "n", "d,x"},
		{opt + `, ?w != 4 => ?n`, "n", "a,b,c,x"},
		{opt + `, absent(?w) => ?n`, "n", "a,b,c"},
	} {
		rows, err := both(mustParse(t, c.q), baseFor(std(src)))
		if err != nil {
			t.Fatalf("%s: %v", c.q, err)
		}
		if got := col(rows, c.col); got != c.want {
			t.Errorf("%s = %s, want %s", c.q, got, c.want)
		}
	}
}
