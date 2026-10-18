package stdlib

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// The docs a host shows for the standard predicates make claims a reader writes patterns by, so the
// claims are held to the behaviour (#17).
func TestStandardPredicateDocsSayWhatTheyDo(t *testing.T) {
	v := ns.MustVocabulary(nil)
	if err := Register(v); err != nil {
		t.Fatal(err)
	}
	holds := func(path string, args ...ns.Value) bool {
		t.Helper()
		b, _ := v.Predicate(path)
		ok, err := b.Holds(args)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	for _, c := range []struct {
		path, doc string
		cases     map[bool][][]ns.Value
	}{
		{"str.glob", "the whole string matches a SQLite-style glob (`*` any run, `?` one character, `[a-z]` or `[^a-z]` one of a class, `[[]` a literal `[`)", map[bool][][]ns.Value{
			true:  {{ns.S("abc"), ns.S("a?c")}, {ns.S("abbbc"), ns.S("a*c")}, {ns.S("Bx"), ns.S("[A-C]x")}, {ns.S("Dx"), ns.S("[^A-C]x")}, {ns.S("D[1]"), ns.S("D[[]1]")}},
			false: {{ns.S("xabc"), ns.S("a*")}, {ns.S("abcx"), ns.S("a?c")}, {ns.S("abbc"), ns.S("a?c")}},
		}},
		{"str.match", "an unanchored regular expression", map[bool][][]ns.Value{
			true: {{ns.S("xabcx"), ns.S("ab")}},
		}},
		{"absent", "different from an empty string and from zero", map[bool][][]ns.Value{
			true:  {{ns.Absent()}},
			false: {{ns.S("")}, {ns.N(0)}},
		}},
	} {
		b, _ := v.Predicate(c.path)
		if !strings.Contains(b.Doc, c.doc) {
			t.Errorf("%s doc = %q, want it to say %q", c.path, b.Doc, c.doc)
		}
		for want, argsets := range c.cases {
			for _, args := range argsets {
				if got := holds(c.path, args...); got != want {
					t.Errorf("%s%v = %v, want %v", c.path, args, got, want)
				}
			}
		}
	}
}

func TestLevenshteinCountsCharacterEdits(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"GND", "GND", 0},
		{"", "abc", 3},
		{"PMIC_EN", "PMIC_EM", 1},
		{"SDA0", "SDAO", 1},
		{"kitten", "sitting", 3},
		{"flaw", "lawn", 2},
		{"R1Ω", "R2Ω", 1},
		{"Ω", "O", 1},
	} {
		for _, p := range [][2]string{{c.a, c.b}, {c.b, c.a}} {
			if got := levenshtein(p[0], p[1]); got != c.want {
				t.Errorf("levenshtein(%q, %q) = %d, want %d", p[0], p[1], got, c.want)
			}
		}
	}
}

func TestDistanceNeedsBothStringsAndBindsTheThird(t *testing.T) {
	v := ns.MustVocabulary(nil)
	if err := Register(v); err != nil {
		t.Fatal(err)
	}
	b, ok := v.Predicate("str.distance")
	if !ok || !b.IsGenerator() {
		t.Fatalf("str.distance = %+v, %v, want a generator", b, ok)
	}
	if !b.Satisfied([]bool{true, true, false}) || b.Satisfied([]bool{true, false, true}) || b.Satisfied([]bool{false, true, true}) {
		t.Errorf("str.distance modes = %v, want both strings bound and the distance free", b.Modes)
	}
	var got []ns.Value
	err := b.Gen(context.Background(), nil, []ns.Arg{{Value: ns.S("PMIC_EN"), Bound: true}, {Value: ns.S("PMIC_EM"), Bound: true}, {}}, func(vals []ns.Value, _ []string) error {
		got = append(got, vals...)
		return nil
	})
	if err != nil || len(got) != 3 || got[0].S != "PMIC_EN" || got[1].S != "PMIC_EM" || got[2].Num == nil || *got[2].Num != 1 {
		t.Errorf("str.distance(PMIC_EN, PMIC_EM, ?d) emitted %v (%v), want one row with d = 1", got, err)
	}
	if err := b.Gen(context.Background(), nil, []ns.Arg{{Value: ns.S("PMIC_EN"), Bound: true}, {}, {}}, func([]ns.Value, []string) error {
		t.Error("str.distance emitted with its second string unbound")
		return nil
	}); err == nil || !strings.HasPrefix(err.Error(), "query: ") {
		t.Errorf("str.distance with an unbound string: err = %v, want a query: error", err)
	}
}

// The text functions (#284), in each mode, against the Go each replaces in Declaire's linkers.
func TestTextFunctionsBuildAndCutText(t *testing.T) {
	v := ns.MustVocabulary(nil)
	if err := Register(v); err != nil {
		t.Fatal(err)
	}
	free := ns.Value{S: "\x00free"} // the zero Value is "", which these functions take
	run := func(path string, args ...ns.Value) string {
		t.Helper()
		b, ok := v.Predicate(path)
		if !ok {
			t.Fatalf("%s isn't registered", path)
		}
		in := make([]ns.Arg, len(args))
		for i, a := range args {
			if a != free {
				in[i] = ns.Arg{Value: a, Bound: true}
			}
		}
		var rows []string
		err := b.Gen(context.Background(), nil, in, func(vals []ns.Value, _ []string) error {
			row := make([]string, len(vals))
			for i, x := range vals {
				row[i] = x.S
				if x.Absent {
					row[i] = "absent"
				}
			}
			rows = append(rows, strings.Join(row, " "))
			return nil
		})
		if err != nil {
			return "error: " + err.Error()
		}
		return strings.Join(rows, " | ")
	}
	for _, c := range []struct{ got, want string }{
		{run("str.concat", ns.S("Greeter"), ns.S(".Hello"), free), "Greeter .Hello Greeter.Hello"},
		{run("str.concat", ns.S("add_"), free, ns.S("add_Greeter")), "add_ Greeter add_Greeter"},
		{run("str.concat", ns.S("add_"), free, ns.S("Greeter")), ""},
		{run("str.concat", free, ns.S("_to_server"), ns.S("Greeter_to_server")), "Greeter _to_server Greeter_to_server"},
		{run("str.concat", free, ns.S("_to_server"), ns.S("Greeter")), ""},
		{run("str.concat", ns.S(""), free, ns.S("x")), " x x"},
		{run("str.concat", ns.Absent(), ns.S("x"), free), ""},
		{run("str.replace", ns.S("pkg_v1_Greeter"), ns.S("_"), ns.S("."), free), "pkg_v1_Greeter _ . pkg.v1.Greeter"},
		{run("str.replace", ns.S("abc"), ns.S("z"), ns.S("y"), free), "abc z y abc"},
		{run("str.lower", ns.S("PMIC_En Ω"), free), "PMIC_En Ω pmic_en ω"},
		{run("str.upper", ns.S("sayHello ω"), free), "sayHello ω SAYHELLO Ω"},
		{run("str.capitalize", ns.S("sayHello"), free), "sayHello SayHello"},
		{run("str.capitalize", ns.S("ωx"), free), "ωx Ωx"},
		{run("str.capitalize", ns.S(""), free), " "},
		{run("str.lower", ns.Absent(), free), ""},
		{run("str.split", ns.S("a.b.c"), ns.S("."), free, free), "a.b.c . 0 a | a.b.c . 1 b | a.b.c . 2 c"},
		{run("str.split", ns.S("abc"), ns.S("."), free, free), "abc . 0 abc"},
		{run("str.split", ns.S("a.b.c"), ns.S("."), ns.N(-1), free), "a.b.c . -1 c"},
		{run("str.split", ns.S("a.b.c"), ns.S("."), ns.N(-2), free), "a.b.c . -2 b"},
		{run("str.split", ns.S("a.b.c"), ns.S("."), ns.N(1), free), "a.b.c . 1 b"},
		{run("str.split", ns.S("a.b.c"), ns.S("."), ns.N(3), free), ""},
		{run("str.split", ns.S("a.b.c"), ns.S("."), ns.N(-4), free), ""},
		{run("str.split", ns.S("a.b.c"), ns.S("."), ns.N(0.5), free), ""},
		{run("str.split", ns.S("aΩ"), ns.S(""), free, free), "aΩ  0 a | aΩ  1 Ω"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
	for _, c := range []struct {
		path string
		args []ns.Value
	}{
		{"str.concat", []ns.Value{ns.S("a"), free, free}},
		{"str.concat", []ns.Value{free, free, ns.S("a")}},
		{"str.replace", []ns.Value{ns.S("a"), free, ns.S("b"), free}},
		{"str.lower", []ns.Value{free, ns.S("a")}},
		{"str.split", []ns.Value{ns.S("a"), free, free, free}},
	} {
		if got := run(c.path, c.args...); !strings.HasPrefix(got, "error: query: "+c.path+" needs ") {
			t.Errorf("%s%v = %q, want a query: error saying what it needs bound", c.path, c.args, got)
		}
	}
}
