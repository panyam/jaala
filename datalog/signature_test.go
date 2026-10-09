package datalog

import (
	"github.com/panyam/jaala/ns"
	"reflect"
	"strings"
	"testing"
	"time"
)

// circuit is a registry shaped like agni's, under the paths agni#751 moves them to: enough relations
// to carry every kind of declaration its column typing reads.
func circuit() *ns.Vocabulary {
	src := ns.NewMemSource().
		DeclareSchema("component.class", ns.Schema{Arity: 2, Labels: []string{"ref_des", "class"}, Types: []ns.ArgType{{Kind: "component"}, {Type: ns.TypeString}}}).
		DeclareSchema("component.net", ns.Schema{Arity: 2, Labels: []string{"ref_des", "net"}, Types: []ns.ArgType{{Kind: "component"}, {Kind: "net"}}, Doc: "a component is on a net"}).
		DeclareSchema("component.pin", ns.Schema{Arity: 2, Labels: []string{"ref_des", "pin"}, Types: []ns.ArgType{{Kind: "component"}, {Kind: "pin", Owner: "ref_des"}}}).
		DeclareSchema("component.mpn", ns.Schema{Arity: 2, Labels: []string{"ref_des", "mpn"}, Types: []ns.ArgType{{Kind: "component"}}}).
		DeclareSchema("net.ground", ns.Schema{Arity: 1, Labels: []string{"net"}, Types: []ns.ArgType{{Kind: "net"}}}).
		DeclareSchema("net.max_voltage", ns.Schema{Arity: 2, Labels: []string{"net", "volts"}, Types: []ns.ArgType{{Kind: "net"}, {Type: ns.TypeNumber, Unit: "V"}}}).
		DeclareSchema("entity", ns.Schema{Arity: 2, Labels: []string{"name", "kind"}, Types: []ns.ArgType{{KindFrom: "kind"}, {Domain: []string{"component", "net", "bus"}}}})
	src.Add("component.class", ns.Tuple{Vals: []ns.Value{ns.S("L1"), ns.S("ferrite")}}).
		Add("component.net", ns.Tuple{Vals: []ns.Value{ns.S("L1"), ns.S("VBUS")}}).
		Add("entity", ns.Tuple{Vals: []ns.Value{ns.S("VBUS"), ns.S("net")}})
	return std(src)
}

func kinds(t *testing.T, r *ns.Vocabulary, q string) []ColumnKind {
	t.Helper()
	got, err := ColumnKinds(mustParse(t, q), r)
	if err != nil {
		t.Fatalf("ColumnKinds(%q): %v", q, err)
	}
	return got
}

// agni's column-typing cases (service/query_test.go, agni issue 654), paths renamed per agni#751.
func TestColumnKindsFollowDerivedRelations(t *testing.T) {
	for _, c := range []struct{ name, query, want string }{
		{"a declared atom", `component.class(?p, "ferrite") => ?p`, "component"},
		{"one hop through a rule", `ferr(?p) :- component.class(?p, "ferrite"); ferr(?p) => ?p`, "component"},
		{"two hops", `a(?p) :- component.class(?p, "ferrite"); b(?p) :- a(?p); b(?p) => ?p`, "component"},
		{"a bucket through negation",
			`p(?c) :- component.class(?c, "capacitor"); cov(?c) :- p(?c), component.net(?c, ?n); ` +
				`nocov(?c) :- p(?c), not cov(?c); nocov(?c) => ?c`, "component"},
		// The control: without it a first-wins implementation passes every other case.
		{"rules that disagree stay untyped", `x(?v) :- component.class(?v, "ferrite"); x(?v) :- net.ground(?v); x(?v) => ?v`, ""},
		{"a per-row kind the head drops stays untyped", `e(?n) :- entity(?n, ?k); e(?n) => ?n`, ""},
		{"an aggregate is a number whatever it reduces", `ferr(?p) :- component.class(?p, "ferrite"); ferr(?p) => count(?p)`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := kinds(t, circuit(), c.query); len(got) != 1 || got[0].Kind != c.want || got[0].KindFrom != "" {
				t.Errorf("ColumnKinds = %+v, want kind %q", got, c.want)
			}
		})
	}
}

func TestColumnKindsSurvivesARecursiveRule(t *testing.T) {
	done := make(chan []ColumnKind, 1)
	go func() {
		got, _ := ColumnKinds(MustParse(`r(?a, ?b) :- component.net(?a, ?b); r(?a, ?b) :- r(?a, ?c), r(?c, ?b); r(?a, ?b) => ?a`), circuit())
		done <- got
	}()
	select {
	case got := <-done:
		if len(got) != 1 || got[0].Kind != "component" {
			t.Errorf("ColumnKinds = %+v, want component, typed by the base case", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ColumnKinds did not terminate on a recursive rule set")
	}
}

func TestAPerRowKindStaysPerRowThroughAHeadThatCarriesIt(t *testing.T) {
	if got := kinds(t, circuit(), `entity(?n, ?k) => ?n`); got[0].KindFrom != "k" || got[0].Kind != "" {
		t.Errorf("entity column = %+v, want its kind from ?k", got[0])
	}
	if got := kinds(t, circuit(), `e(?n, ?k) :- entity(?n, ?k); e(?name, ?what) => ?name`); got[0].KindFrom != "what" {
		t.Errorf("through a rule = %+v, want its kind from ?what", got[0])
	}
}

func TestAConstantKindTypesTheColumnOnlyWhenTheVocabularyHoldsIt(t *testing.T) {
	if got := kinds(t, circuit(), `entity(?n, "net") => ?n`); got[0].Kind != "net" {
		t.Errorf("entity(?n, \"net\") = %+v, want net", got[0])
	}
	if got := kinds(t, circuit(), `entity(?n, "pin") => ?n`); got[0] != (ColumnKind{}) {
		t.Errorf("entity(?n, \"pin\") = %+v, want untyped", got[0])
	}
}

func TestAPinIsLocatedThroughItsOwner(t *testing.T) {
	if got := kinds(t, circuit(), `component.pin(?r, ?p) => ?p`); got[0].Kind != "pin" || got[0].Owner.Var != "r" {
		t.Errorf("pin column = %+v, want pin owned by ?r", got[0])
	}
	if got := kinds(t, circuit(), `pp(?c, ?q) :- component.pin(?c, ?q); pp(?r, ?p) => ?p`); got[0].Kind != "pin" || got[0].Owner.Var != "r" {
		t.Errorf("through a rule = %+v, want pin owned by ?r", got[0])
	}
	if got := kinds(t, circuit(), `pp(?q) :- component.pin(?c, ?q); pp(?p) => ?p`); got[0] != (ColumnKind{}) {
		t.Errorf("owner dropped = %+v, want untyped: a pin nothing locates", got[0])
	}
}

func TestAScalarColumnKeepsItsTypeAndUnit(t *testing.T) {
	got := kinds(t, circuit(), `net.max_voltage(?n, ?v) => ?n, ?v`)
	if got[0].Kind != "net" || got[1].Type != ns.TypeNumber || got[1].Unit != "V" {
		t.Errorf("ColumnKinds = %+v, want net then number[V]", got)
	}
}

func TestParseHeadDeclarations(t *testing.T) {
	q := mustParse(t, `x(?n: net, ?v: number[V], ?k, ?e: ?k, ?p: pin(?n), ?r: {"source", "sink"}, ?s: string) :- `+
		`component.net(?n, ?n), net.max_voltage(?n, ?v), entity(?e, ?k), component.pin(?n, ?p), component.class(?r, ?s), component.mpn(?s, ?s); x(?n, ?v, ?k, ?e, ?p, ?r, ?s)`)
	var got []string
	for _, ty := range q.Rules[0].HeadTypes {
		got = append(got, ty.String())
	}
	want := []string{"net", "number[V]", "", "?k", "pin(?n)", `{"source", "sink"}`, "string"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("declarations = %q, want %q", got, want)
	}
	for text, frag := range map[string]string{
		`x(?n: ?n) :- node(?n); x(?n)`:        "must name another variable of its head",
		`x(?n: pin(?r)) :- node(?n); x(?n)`:   "must name another variable of its head",
		`x("a": net) :- node(?n); x(?n)`:      "only a ?variable can declare a type",
		`x(?_: net) :- node(?n); x(?n)`:       "only a ?variable can declare a type", // ?_ is "_", printed back as _: net (#89)
		`x(?n: {source}) :- node(?n); x(?n)`:  "must be a \"string\"",
		`x(?n: net thing) :- node(?n); x(?n)`: "bad type declaration",
	} {
		if _, err := Parse(text); err == nil || !strings.Contains(err.Error(), frag) {
			t.Errorf("%s: err = %v, want %q", text, err, frag)
		}
	}
}

// power is a module whose members show each way a signature is arrived at.
const power = `
# Nets that carry a test point.
has_test_point(?n) :- component.net(?tp, ?n), component.class(?tp, "test_point");

# A net's role: declared closed, both values from constant heads.
role(?n, "source") :- net.ground(?n);
role(?n, "sink") :- has_test_point(?n);

# Declares a kind its body cannot contradict, since mpn is untyped.
part(?m: part) :- component.mpn(_, ?m);

_helper(?x) :- net.ground(?x);
grounded(?x) :- _helper(?x);
`

func TestSignaturesAreDeclaredOrInferredAndMarked(t *testing.T) {
	r := circuit()
	if err := r.AddModule("net", LanguageName, power, ""); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"net.has_test_point": "net.has_test_point(n: net)",
		"net.role":           `net.role(n: net, arg1: {"sink", "source"})`,
		"net.part":           "net.part(m: part)",
		"net.grounded":       "net.grounded(x: net)",
	} {
		e, err := r.Lookup(path)
		if err != nil {
			t.Fatal(err)
		}
		if e.Signature() != want {
			t.Errorf("%s = %s, want %s", path, e.Signature(), want)
		}
	}
	e, _ := r.Lookup("net.part")
	if e.Args[0].Inferred {
		t.Error("net.part's declared argument is marked inferred")
	}
	e, _ = r.Lookup("net.has_test_point")
	if !e.Args[0].Inferred {
		t.Error("net.has_test_point's argument is not marked inferred")
	}
}

const numericHeadCounts = `
_test_points(?n: net, count(distinct ?tp)) :- component.net(?tp, ?n), component.class(?tp, "test_point");
test_point_count(?n: net, ?c) :- _test_points(?n, ?c);
test_point_count(?n: net, 0) :- entity(?n, "net"), not _test_points(?n, _);
`

func TestNumericHeadSignatures(t *testing.T) {
	for _, c := range []struct {
		name, program, member string
		want                  ns.ArgSig
	}{
		{
			name: "default count", program: numericHeadCounts, member: "test_point_count",
			want: ns.ArgSig{Name: "c", ArgType: ns.ArgType{Type: ns.TypeNumber}},
		},
		{
			name: "control: declared default count", member: "test_point_count",
			program: strings.Replace(numericHeadCounts, "test_point_count(?n: net, ?c)", "test_point_count(?n: net, ?c: number)", 1),
			want:    ns.ArgSig{Name: "c", ArgType: ns.ArgType{Type: ns.TypeNumber}},
		},
		{
			name: "numeric domain", member: "bit",
			program: `bit(0) :- net.ground(?n); bit(1) :- net.ground(?n);`,
			want:    ns.ArgSig{Name: "arg0", ArgType: ns.ArgType{Type: ns.TypeNumber, Domain: []string{"0", "1"}}},
		},
		{
			name: "control: recursive constant heads stay numeric", member: "bit",
			program: `bit(0) :- net.ground(?n); bit(1) :- bit(0);`,
			want:    ns.ArgSig{Name: "arg0", ArgType: ns.ArgType{Type: ns.TypeNumber, Domain: []string{"0", "1"}}},
		},
		{
			name: "control: quoted numeric domain", member: "bit",
			program: `bit("0") :- net.ground(?n); bit("1") :- net.ground(?n);`,
			want:    ns.ArgSig{Name: "arg0", ArgType: ns.ArgType{Domain: []string{"0", "1"}}, Inferred: true},
		},
		{
			name: "control: text domain", member: "role",
			program: `role("source") :- net.ground(?n); role("sink") :- net.ground(?n);`,
			want:    ns.ArgSig{Name: "arg0", ArgType: ns.ArgType{Domain: []string{"sink", "source"}}, Inferred: true},
		},
		{
			name: "control: mixed numeric and text domain", member: "bit",
			program: `bit(0) :- net.ground(?n); bit("1") :- net.ground(?n);`,
			want:    ns.ArgSig{Name: "arg0", ArgType: ns.ArgType{Domain: []string{"0", "1"}}, Inferred: true},
		},
		{
			name: "control: mixed text and numeric domain", member: "bit",
			program: `bit("1") :- net.ground(?n); bit(0) :- net.ground(?n);`,
			want:    ns.ArgSig{Name: "arg0", ArgType: ns.ArgType{Domain: []string{"0", "1"}}, Inferred: true},
		},
		{
			name: "control: an untyped clause stays untyped", member: "value",
			program: `value(?v) :- component.mpn(_, ?v); value(0) :- net.ground(?n);`,
			want:    ns.ArgSig{Name: "v", Inferred: true},
		},
		{
			name: "control: a numeric clause does not type an untyped one", member: "value",
			program: `value(0) :- net.ground(?n); value(?v) :- component.mpn(_, ?v);`,
			want:    ns.ArgSig{Name: "arg0", Inferred: true},
		},
		{
			name: "control: a text clause stays untyped with a number", member: "value",
			program: `value(?v) :- component.class(_, ?v); value(0) :- net.ground(?n);`,
			want:    ns.ArgSig{Name: "v", Inferred: true},
		},
		{
			name: "control: an entity clause stays untyped with a number", member: "value",
			program: `value(?v) :- entity(?v, "net"); value(0) :- net.ground(?n);`,
			want:    ns.ArgSig{Name: "v", Inferred: true},
		},
		{
			name: "control: a typed base still types recursion", member: "value",
			program: `value(?v) :- net.max_voltage(_, ?v); value(?v) :- value(?v);`,
			want:    ns.ArgSig{Name: "v", ArgType: ns.ArgType{Type: ns.TypeNumber, Unit: "V"}, Inferred: true},
		},
		{
			name: "control: a bare number does not adopt a unit", member: "value",
			program: `value(?v) :- net.max_voltage(_, ?v); value(0) :- net.ground(?n);`,
			want:    ns.ArgSig{Name: "v", Inferred: true},
		},
		{
			name: "control: a unit does not adopt a bare number", member: "value",
			program: `value(0) :- net.ground(?n); value(?v) :- net.max_voltage(_, ?v);`,
			want:    ns.ArgSig{Name: "arg0", Inferred: true},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := circuit()
			if err := r.AddModule("net", LanguageName, c.program, ""); err != nil {
				t.Fatal(err)
			}
			e, err := r.Lookup("net." + c.member)
			if err != nil {
				t.Fatal(err)
			}
			if got := e.Args[len(e.Args)-1]; !reflect.DeepEqual(got, c.want) {
				t.Errorf("%s's last argument = %#v, want %#v", e.Signature(), got, c.want)
			}
		})
	}
}

func TestNumericHeadValuesInQueries(t *testing.T) {
	r := circuit()
	if err := r.AddModule("net", LanguageName, numericHeadCounts+`
bit(0) :- net.ground(?n); bit(1) :- net.ground(?n);
mixed(1) :- net.ground(?n); mixed("1") :- net.ground(?n);
`, ""); err != nil {
		t.Fatal(err)
	}
	src := sources[r].(*ns.MemSource)
	for _, tp := range []string{"TP1", "TP2"} {
		src.Add("component.net", ns.Tuple{Vals: []ns.Value{ns.S(tp), ns.S("GND")}})
		src.Add("component.class", ns.Tuple{Vals: []ns.Value{ns.S(tp), ns.S("test_point")}})
	}
	src.Add("entity", ns.Tuple{Vals: []ns.Value{ns.S("GND"), ns.S("net")}})
	src.Add("net.ground", ns.Tuple{Vals: []ns.Value{ns.S("GND")}})
	base := baseFor(r)
	answer := func(query string, opts ...Option) []Row {
		t.Helper()
		rows, err := both(mustParse(t, query), base, opts...)
		if err != nil {
			t.Fatalf("Eval(%q): %v", query, err)
		}
		return rows
	}
	want := []map[Var]ns.Value{
		{"n": ns.S("GND"), "c": ns.N(2)},
		{"n": ns.S("VBUS"), "c": ns.N(0)},
	}
	if got := binds(answer(`net.test_point_count(?n, ?c) => ?n, ?c`)); !reflect.DeepEqual(got, want) {
		t.Errorf("counts = %v, want %v", got, want)
	}
	if got := col(answer(`net.test_point_count(?n, "0") => ?n`), "n"); got != "VBUS" {
		t.Errorf("a text constant in the numeric count column answers %q, want VBUS", got)
	}
	if got := col(answer(`net.test_point_count(?n, ?c) => ?n`, Bind(map[Var][]ns.Value{"c": {ns.S("0")}})), "n"); got != "VBUS" {
		t.Errorf("a bound text count answers %q, want VBUS", got)
	}
	if rows := answer(`net.bit(?b) => ?b`); len(rows) != 2 || rows[0].Bind["b"].Num == nil || rows[1].Bind["b"].Num == nil || col(rows, "b") != "0,1" {
		t.Errorf("numeric constant heads answer %v, want the numbers 0 and 1", binds(rows))
	}
	if rows := answer(`net.mixed(?x) => ?x`); len(rows) != 2 || rows[0].Bind["x"].Num == nil || rows[1].Bind["x"].Num != nil {
		t.Errorf("mixed constant heads answer %v, want 1 then \"1\"", binds(rows))
	}
	if got := col(answer(`net.mixed(?x) => count(distinct ?x)`), "count(distinct x)"); got != "2" {
		t.Errorf("mixed count(distinct) = %s, want 2", got)
	}
	if rows := answer(`net.mixed(?x) => ?x, count(?x)`); len(rows) != 2 {
		t.Errorf("mixed groups = %v, want two", binds(rows))
	}
}

func TestNumericHeadSeedDoesNotTypeARecursiveValue(t *testing.T) {
	for _, c := range []struct{ name, program, label string }{
		{"direct", `r(0) :- seed(_); r(?s) :- r(?n), bridge(?n, ?s);`, "arg0"},
		{"direct reversed", `r(?s) :- r(?n), bridge(?n, ?s); r(0) :- seed(_);`, "s"},
		{"through aliases", `seed_number(0) :- seed(_); alias(?n) :- seed_number(?n);
r(?n) :- alias(?n); r(?s) :- r(?n), bridge(?n, ?s);`, "n"},
		{"through aliases reversed", `seed_number(0) :- seed(_); alias(?n) :- seed_number(?n);
r(?s) :- r(?n), bridge(?n, ?s); r(?n) :- alias(?n);`, "s"},
		{"mutual recursion", `n(0) :- seed(_); r(?x) :- n(?x); r(?x) :- q(?x);
q(?x) :- numbers(?x); q(?s) :- r(?n), bridge(?n, ?s);`, "x"},
		{"mutual recursion reversed", `q(?s) :- r(?n), bridge(?n, ?s); q(?x) :- numbers(?x);
r(?x) :- q(?x); r(?x) :- n(?x); n(0) :- seed(_);`, "x"},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := ns.NewMemSource().Declare("seed", "value").
				DeclareSchema("numbers", ns.Schema{Arity: 1, Labels: []string{"n"}, Types: []ns.ArgType{{Type: ns.TypeNumber}}}).
				DeclareSchema("bridge", ns.Schema{Arity: 2, Labels: []string{"n", "s"}, Types: []ns.ArgType{{Type: ns.TypeNumber}, {Type: ns.TypeString}}})
			src.Add("seed", ns.Tuple{Vals: []ns.Value{ns.S("start")}}).
				Add("numbers", ns.Tuple{Vals: []ns.Value{ns.N(0)}}).
				Add("bridge", ns.Tuple{Vals: []ns.Value{ns.N(0), ns.S("x")}})
			r := std(src)
			if err := r.AddModule("rec", LanguageName, c.program, ""); err != nil {
				t.Fatal(err)
			}
			e, err := r.Lookup("rec.r")
			if err != nil {
				t.Fatal(err)
			}
			if got, want := e.Args[0], (ns.ArgSig{Name: c.label, Inferred: true}); !reflect.DeepEqual(got, want) {
				t.Errorf("recursive argument = %#v, want %#v", got, want)
			}
			b := baseFor(r)
			rows, err := both(mustParse(t, `rec.r(?x) => ?x`), b)
			want := []map[Var]ns.Value{{"x": ns.N(0)}, {"x": ns.S("x")}}
			if err != nil || !reflect.DeepEqual(binds(rows), want) {
				t.Errorf("recursive values = %v, %v, want %v", binds(rows), err, want)
			}
			rows, err = both(mustParse(t, `rec.r("x")`), b)
			if err != nil || len(rows) != 1 {
				t.Errorf("a recursive text value answers %v, %v, want one row", binds(rows), err)
			}
			rows, err = both(mustParse(t, `rec.r(?x) => ?x`), b, Bind(map[Var][]ns.Value{"x": {ns.S("x")}}))
			if err != nil || col(rows, "x") != "x" {
				t.Errorf("a bound recursive text value answers %v, %v, want x", binds(rows), err)
			}
		})
	}
}

func TestNumericHeadRecursionKeepsThePreviousScalarChoice(t *testing.T) {
	src := ns.NewMemSource().Declare("seed", "value").
		DeclareSchema("numbers", ns.Schema{Arity: 1, Types: []ns.ArgType{{Type: ns.TypeNumber}}}).
		DeclareSchema("strings", ns.Schema{Arity: 1, Types: []ns.ArgType{{Type: ns.TypeString}}})
	src.Add("seed", ns.Tuple{Vals: []ns.Value{ns.S("start")}}).
		Add("numbers", ns.Tuple{Vals: []ns.Value{ns.N(0)}}).
		Add("strings", ns.Tuple{Vals: []ns.Value{ns.S("0")}})
	r := std(src)
	if err := r.AddModule("rec", LanguageName, `
n(?v) :- numbers(?v); n(0) :- seed(_); a(?v) :- strings(?v);
r(?v) :- n(?v), a(?v); r(?v) :- a(?v); r(?v) :- r(?v);
`, ""); err != nil {
		t.Fatal(err)
	}
	e, err := r.Lookup("rec.r")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := e.Args[0], (ns.ArgSig{Name: "v", ArgType: ns.ArgType{Type: ns.TypeString}, Inferred: true}); !reflect.DeepEqual(got, want) {
		t.Errorf("recursive scalar choice = %#v, want %#v", got, want)
	}
	b := baseFor(r)
	rows, err := both(mustParse(t, `rec.r(?x) => ?x`), b)
	want := []map[Var]ns.Value{{"x": ns.S("0")}}
	if err != nil || !reflect.DeepEqual(binds(rows), want) {
		t.Errorf("recursive text values = %v, %v, want %v", binds(rows), err, want)
	}
	rows, err = both(mustParse(t, `rec.r(0)`), b)
	if err != nil || len(rows) != 1 {
		t.Errorf("a number in the recursive string column answers %v, %v, want one row", binds(rows), err)
	}
	rows, err = both(mustParse(t, `rec.r(?x) => ?x`), b, Bind(map[Var][]ns.Value{"x": {ns.N(0)}}))
	if err != nil || col(rows, "x") != "0" {
		t.Errorf("a bound number in the recursive string column answers %v, %v, want 0", binds(rows), err)
	}
}

func TestADeclarationItsRulesContradictIsRefused(t *testing.T) {
	for text, frag := range map[string]string{
		`x(?n: component) :- net.ground(?n);`:                               `net.x declares ?n: component, but its rules make it net`,
		`x(?v: number[A]) :- net.max_voltage(_, ?v);`:                       `declares ?v: number[A], but its rules make it number[V]`,
		`x(?r: {"a", "b"}) :- entity(_, ?r);`:                               `able to hold "bus"`,
		`x(?n: net) :- net.ground(?n); x(?n: component) :- net.ground(?n);`: `declares its "n" argument as both "net" and "component"`,
	} {
		r := circuit()
		if err := r.AddModule("net", LanguageName, text, ""); err != nil {
			t.Fatal(err)
		}
		if err := r.Check(); err == nil || !strings.Contains(err.Error(), frag) {
			t.Errorf("%s\n err = %v\n want %q", text, err, frag)
		}
	}
}

func TestADerivedVocabularyIsEnforcedInQueries(t *testing.T) {
	r := circuit()
	if err := r.AddModule("net", LanguageName, power, ""); err != nil {
		t.Fatal(err)
	}
	_, err := Naive{}.Eval(bg, mustParse(t, `net.role(?n, "sorce")`), baseFor(r))
	if err == nil || !strings.Contains(err.Error(), `net.role's "arg1" argument cannot be "sorce", did you mean "source"?`) {
		t.Errorf("err = %v, want the vocabulary enforced with a suggestion", err)
	}
}

func TestLookupDrillsFromModuleToMemberToDefinition(t *testing.T) {
	r := circuit()
	if err := r.AddModule("net", LanguageName, power, ""); err != nil {
		t.Fatal(err)
	}
	root, err := r.Lookup("")
	if err != nil || root.Kind != ns.EntryModule || !reflect.DeepEqual(root.Members, []string{"absent", "component", "entity", "net", "str"}) {
		t.Errorf("root = %+v, %v", root, err)
	}
	members, err := r.Members("net")
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, m := range members {
		listed = append(listed, string(m.Kind)+" "+m.Signature())
	}
	want := []string{
		"base net.ground(net: net)",
		"derived net.grounded(x: net)",
		"derived net.has_test_point(n: net)",
		"base net.max_voltage(net: net, volts: number[V])",
		"derived net.part(m: part)",
		`derived net.role(n: net, arg1: {"sink", "source"})`,
	}
	if !reflect.DeepEqual(listed, want) {
		t.Errorf("Members(net) =\n %s\nwant\n %s", strings.Join(listed, "\n "), strings.Join(want, "\n "))
	}
	e, _ := r.Lookup("net.role")
	wantDef := []string{`role(?n, "source") :- net.ground(?n)`, `role(?n, "sink") :- has_test_point(?n)`}
	if e.Doc != "A net's role: declared closed, both values from constant heads." || e.Module != "net" || !reflect.DeepEqual(e.Definition, wantDef) {
		t.Errorf("net.role = %+v, want its doc and its two rules as written", e)
	}
	if e, _ := r.Lookup("component.net"); e.Kind != ns.EntryBase || e.Doc != "a component is on a net" {
		t.Errorf("component.net = %+v", e)
	}
	if e, _ := r.Lookup("str.contains"); e.Kind != ns.EntryPredicate || e.Signature() != "str.contains(string: string, substring: string)" {
		t.Errorf("str.contains = %+v", e)
	}
	if _, err := r.Lookup("net._helper"); err == nil || !strings.Contains(err.Error(), `unknown relation "net._helper"`) {
		t.Errorf("private: err = %v, want it unknown", err)
	}
	if _, err := r.Lookup("net.has_tst_point"); err == nil || !strings.Contains(err.Error(), `did you mean "net.has_test_point"?`) {
		t.Errorf("typo: err = %v, want a suggestion", err)
	}
	if _, err := r.Members("net.role"); err == nil || !strings.Contains(err.Error(), "is a derived, not a module") {
		t.Errorf("Members of a member: err = %v", err)
	}
}
