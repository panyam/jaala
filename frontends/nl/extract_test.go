package nl_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

// owned is the guide's packages with a closed license and an owner each: app imports api, app is
// MIT and alice's, and the deps module derives depends_on.
func owned(t *testing.T) (*ns.Vocabulary, *datalog.Base) {
	t.Helper()
	license := ns.ArgType{Type: ns.TypeString, Domain: []string{"MIT", "Apache-2.0", "BSD-3-Clause"}}
	src := ns.NewMemSource().
		DeclareSchema("imports", ns.Schema{Arity: 2, Labels: []string{"from", "to"}, Doc: "A package importing another."}).
		DeclareSchema("pkg", ns.Schema{Arity: 2, Labels: []string{"name", "license"}, Types: []ns.ArgType{{}, license}, Doc: "A package and its license."}).
		DeclareSchema("owner", ns.Schema{Arity: 2, Labels: []string{"pkg", "person"}, Doc: "Who owns a package."})
	src.Add("imports", ns.Tuple{Vals: []ns.Value{ns.S("app"), ns.S("api")}})
	src.Add("pkg", ns.Tuple{Vals: []ns.Value{ns.S("app"), ns.S("MIT")}})
	src.Add("owner", ns.Tuple{Vals: []ns.Value{ns.S("app"), ns.S("alice")}})
	v, err := ns.NewVocabulary(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.AddLanguage(datalog.Language); err != nil {
		t.Fatal(err)
	}
	if err := stdlib.Register(v); err != nil {
		t.Fatal(err)
	}
	if err := v.AddModule("deps", datalog.LanguageName, "depends_on(?a, ?b) :- imports(?a, ?b);\ndepends_on(?a, ?c) :- depends_on(?a, ?b), imports(?b, ?c);", ""); err != nil {
		t.Fatal(err)
	}
	b, err := datalog.NewBase(v, src)
	if err != nil {
		t.Fatal(err)
	}
	return v, b
}

// ownedPack keys a package's license and owner by the package.
func ownedPack() *nl.Pack {
	return &nl.Pack{
		About: "Go packages, what they import, their licenses and owners.",
		Keys:  map[string][]int{"pkg": {0}, "owner": {0}},
		ExtractExamples: []nl.ExtractExample{{
			Text:  "util is BSD licensed (BSD-3-Clause) and imports nothing.",
			Lines: `+ pkg("util", "BSD-3-Clause") from "util is BSD licensed (BSD-3-Clause)"`,
		}},
	}
}

// changesText writes a change set as lines: each relation's removals then additions, an addition
// with its citations.
func changesText(c ns.Changes) string {
	var b strings.Builder
	val := func(vals []ns.Value) string {
		out := make([]string, len(vals))
		for i, v := range vals {
			if v.Num != nil {
				out[i] = v.S + v.BaseUnit
			} else {
				out[i] = fmt.Sprintf("%q", v.S)
			}
		}
		return strings.Join(out, ", ")
	}
	for _, rel := range c.Relations() {
		for _, t := range c.Removed(rel) {
			fmt.Fprintf(&b, "- %s(%s)\n", rel, val(t.Vals))
		}
		for _, t := range c.Added(rel) {
			fmt.Fprintf(&b, "+ %s(%s) %v\n", rel, val(t.Vals), t.Cites)
		}
	}
	return b.String()
}

// TestExtract reads three texts with a scripted model: a license outside the closed domain refused
// and repaired, a new owner replacing the base's under the pack's key, and an import the text says
// ended. The prefix, every try and each change set are in testdata/extract.golden (-update).
func TestExtract(t *testing.T) {
	v, b := owned(t)
	cases := []struct {
		name, text string
		replies    []string
	}{
		{"license", "log imports util, and log is released under the Apache 2.0 license.", []string{
			"+ imports(\"log\", \"util\") from \"log imports util\"\n+ pkg(\"log\", \"Apache 2.0\") from \"released under the Apache 2.0 license\"",
			"+ imports(\"log\", \"util\") from \"log imports util\"\n+ pkg(\"log\", \"Apache-2.0\") from \"released under the Apache 2.0 license\"",
		}},
		{"owner", "Bob owns app now; alice handed it over last week.", []string{
			"+ owner(\"app\", \"bob\") from \"Bob owns app now\"",
		}},
		{"removed", "As of this release app no longer imports api.", []string{
			"- imports(\"app\", \"api\") from \"app no longer imports api\"",
		}},
	}
	var out strings.Builder
	for i, c := range cases {
		m := model("small", c.replies...)
		x, tr, err := nl.Extract(context.Background(), c.text, v, m, nl.ExtractOptions{Base: b, Pack: ownedPack()})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if i == 0 {
			fmt.Fprintf(&out, "== prefix\n%s\n", m.asked[0].Prefix)
			if m.asked[0].Grammar == "" {
				t.Errorf("%s: no grammar sent", c.name)
			}
		}
		fmt.Fprintf(&out, "== %s\ntext: %s\ntries: %v\n", c.name, c.text, kinds(tr))
		for k, p := range m.asked[1:] {
			fmt.Fprintf(&out, "feedback %d:\n%s\n", k+1, p.Turns[len(p.Turns)-1].Text)
		}
		out.WriteString("changes:\n" + changesText(x.Changes))
	}
	path := filepath.Join("testdata", "extract.golden")
	if *update {
		if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != string(want) {
		t.Errorf("extract output differs from %s (-update to rewrite):\n%s", path, out.String())
	}
}

// An added fact under a pack's key replaces what the Base holds under it, and what an earlier line
// added; without a key, or with the same values, nothing is removed. control: with no Base, the
// base's alice isn't removed.
func TestAnExtractedFactReplacesTheOneHeldUnderItsKey(t *testing.T) {
	v, b := owned(t)
	const text = "Bob owns app now. Then carol took app over. util is MIT."
	cases := []struct {
		name, lines string
		base        *datalog.Base
		want        string
	}{
		{"base", `+ owner("app", "bob") from "Bob owns app now"`, b,
			"- owner(\"app\", \"alice\")\n+ owner(\"app\", \"bob\") [text:0-16]\n"},
		{"earlier line", "+ owner(\"app\", \"bob\") from \"Bob owns app now\"\n+ owner(\"app\", \"carol\") from \"carol took app over\"", b,
			"- owner(\"app\", \"alice\")\n- owner(\"app\", \"bob\")\n+ owner(\"app\", \"carol\") [text:23-42]\n"},
		{"same values", `+ owner("app", "alice") from "Bob owns app now"`, b,
			"+ owner(\"app\", \"alice\") [text:0-16]\n"},
		{"another key", `+ pkg("util", "MIT") from "util is MIT"`, b,
			"+ pkg(\"util\", \"MIT\") [text:44-55]\n"},
		{"control: no base", `+ owner("app", "bob") from "Bob owns app now"`, nil,
			"+ owner(\"app\", \"bob\") [text:0-16]\n"},
	}
	for _, c := range cases {
		x, _, err := nl.Extract(context.Background(), text, v, model("m", c.lines), nl.ExtractOptions{Base: c.base, Pack: ownedPack()})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := changesText(x.Changes); got != c.want {
			t.Errorf("%s: changes\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
	x, _, err := nl.Extract(context.Background(), text, v, model("m", `+ owner("app", "bob") from "Bob owns app now"`), nl.ExtractOptions{Base: b, Pack: &nl.Pack{}})
	if err != nil || changesText(x.Changes) != "+ owner(\"app\", \"bob\") [text:0-16]\n" {
		t.Errorf("no keys: %q, %v; want only the addition", changesText(x.Changes), err)
	}
}

// A key holding a quote or a backslash, which a draft writes escaped, finds the fact the host stored
// unescaped and replaces it (#444); an earlier line under that key is replaced the same way.
func TestAQuotedKeyReplacesTheFactHeldUnderIt(t *testing.T) {
	src := ns.NewMemSource().DeclareSchema("owner", ns.Schema{Arity: 2, Labels: []string{"pkg", "person"}})
	src.Add("owner", ns.Tuple{Vals: []ns.Value{ns.S(`say "hi"`), ns.S("alice")}})
	src.Add("owner", ns.Tuple{Vals: []ns.Value{ns.S(`C:\dir`), ns.S("alice")}})
	v, err := ns.NewVocabulary(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.AddLanguage(datalog.Language); err != nil {
		t.Fatal(err)
	}
	b, err := datalog.NewBase(v, src)
	if err != nil {
		t.Fatal(err)
	}
	pack := &nl.Pack{Keys: map[string][]int{"owner": {0}}}
	const text = `Bob owns say "hi" now. Carol took it over. Dan owns C:\dir.`
	cases := []struct{ name, lines, want string }{
		{"quote", `+ owner("say \"hi\"", "bob") from "Bob owns say \"hi\" now"`,
			"- owner(\"say \\\"hi\\\"\", \"alice\")\n+ owner(\"say \\\"hi\\\"\", \"bob\") [text:0-21]\n"},
		{"backslash", `+ owner("C:\\dir", "dan") from "Dan owns C:\\dir"`,
			"- owner(\"C:\\\\dir\", \"alice\")\n+ owner(\"C:\\\\dir\", \"dan\") [text:43-58]\n"},
		{"earlier line", `+ owner("say \"hi\"", "bob") from "Bob owns say \"hi\" now"` + "\n" + `+ owner("say \"hi\"", "carol") from "Carol took it over"`,
			"- owner(\"say \\\"hi\\\"\", \"alice\")\n- owner(\"say \\\"hi\\\"\", \"bob\")\n+ owner(\"say \\\"hi\\\"\", \"carol\") [text:23-41]\n"},
	}
	for _, c := range cases {
		x, _, err := nl.Extract(context.Background(), text, v, model("m", c.lines), nl.ExtractOptions{Base: b, Pack: pack})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := changesText(x.Changes); got != c.want {
			t.Errorf("%s: changes\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
}

// A refused line goes back to the model naming its line, what was wrong and, for a value, where.
func TestExtractRefusesALineItCantHold(t *testing.T) {
	v, _ := owned(t)
	const text = "app imports api. app is MIT licensed."
	cases := []struct {
		name, line, kind string
		feedback         []string
	}{
		{"span not in text", `+ imports("app", "api") from "app depends on api"`, nl.KindSpan,
			[]string{"line 1:", `the text doesn't say "app depends on api"`}},
		{"no span", `+ imports("app", "api")`, nl.KindSpan,
			[]string{"line 1:", "an added fact says where the text states it"}},
		{"derived", `+ deps.depends_on("app", "api") from "app imports api"`, nl.KindDerived,
			[]string{"line 1:", "deps.depends_on is a derived relation"}},
		{"arity", `+ imports("app", "api", "x") from "app imports api"`, "arity",
			[]string{"line 1:", "takes 2 args", `+ imports("app", "api", "x") from "app imports api"`, "^"}},
		{"closed domain", "+ imports(\"app\", \"api\") from \"app imports api\"\n+ pkg(\"app\", \"mit\") from \"app is MIT licensed\"", "type",
			[]string{"line 2:", `"mit"`, `+ pkg("app", "mit") from "app is MIT licensed"`, "^"}},
		{"variable", `+ imports("app", ?x) from "app imports api"`, nl.KindParse,
			[]string{"line 1:", "?x is a variable"}},
		{"no sign", `imports("app", "api") from "app imports api"`, nl.KindParse,
			[]string{"line 1:", "a line is + and a fact"}},
		{"unknown", `+ import("app", "api") from "app imports api"`, "unknown",
			[]string{"line 1:", `unknown relation "import"`}},
	}
	for _, c := range cases {
		m := model("m", c.line)
		_, tr, err := nl.Extract(context.Background(), text, v, m, nl.ExtractOptions{MaxTries: 1})
		if err == nil || len(tr.Tries) != 1 {
			t.Fatalf("%s: %v, %d tries; want a refusal", c.name, err, len(tr.Tries))
		}
		if tr.Tries[0].Kind != c.kind {
			t.Errorf("%s: kind %q, want %q (%v)", c.name, tr.Tries[0].Kind, c.kind, tr.Tries[0].Err)
		}
		m2 := model("m", c.line, `+ imports("app", "api") from "app imports api"`)
		if _, _, err := nl.Extract(context.Background(), text, v, m2, nl.ExtractOptions{}); err != nil {
			t.Fatalf("%s: the fixed draft: %v", c.name, err)
		}
		fb := m2.asked[1].Turns[len(m2.asked[1].Turns)-1].Text
		for _, want := range c.feedback {
			if !strings.Contains(fb, want) {
				t.Errorf("%s: feedback lacks %q:\n%s", c.name, want, fb)
			}
		}
	}
}

// A draft the model already wrote is refused unchecked and moves to the fallback, which starts over.
func TestExtractMovesARepeatedDraftToTheFallback(t *testing.T) {
	v, _ := owned(t)
	bad := `+ imports("app", "api") from "nowhere"`
	m := model("small", bad, bad)
	fb := model("big", `+ imports("app", "api") from "app imports api"`)
	x, tr, err := nl.Extract(context.Background(), "app imports api.", v, m, nl.ExtractOptions{Fallback: fb})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(kinds(tr), ","); got != "span,repeat," {
		t.Errorf("kinds %s, want span,repeat, then accepted", got)
	}
	if x.Model != "big" || len(fb.asked[0].Turns) != 1 {
		t.Errorf("answered by %s with %d turns; want the fallback, fresh", x.Model, len(fb.asked[0].Turns))
	}
}

// A pack whose keys or extraction examples don't check fails Extract before any model is asked.
func TestAPackWhoseExtractionDoesntCheckFailsFirst(t *testing.T) {
	v, _ := owned(t)
	for _, c := range []struct {
		name string
		pack nl.Pack
		want string
	}{
		{"key relation", nl.Pack{Keys: map[string][]int{"deps.depends_on": {0}}}, "isn't a base relation"},
		{"key position", nl.Pack{Keys: map[string][]int{"owner": {2}}}, "name argument 2, and it takes 2"},
		{"example", nl.Pack{ExtractExamples: []nl.ExtractExample{{Text: "app is GPL.", Lines: `+ pkg("app", "GPL") from "app is GPL"`}}}, "extraction example 1: line 1:"},
	} {
		m := model("m")
		_, _, err := nl.Extract(context.Background(), "x", v, m, nl.ExtractOptions{Pack: &c.pack})
		if err == nil || !strings.Contains(err.Error(), c.want) || len(m.asked) != 0 {
			t.Errorf("%s: %v after %d asks; want %q before any", c.name, err, len(m.asked), c.want)
		}
	}
	if err := ownedPack().Validate(v); err != nil {
		t.Errorf("control: the good pack: %v", err)
	}
}

// Extract stops at its Budget, with the Trace of what it spent.
func TestExtractStopsAtItsBudget(t *testing.T) {
	v, _ := owned(t)
	bad := func(where string) string { return `+ imports("app", "api") from "` + where + `"` }
	_, tr, err := nl.Extract(context.Background(), "app imports api.", v, model("m", bad("nowhere"), bad("elsewhere"), bad("anywhere")), nl.ExtractOptions{Budget: nl.Budget{Tokens: 150}})
	var spent *nl.BudgetSpent
	if !errors.As(err, &spent) || len(tr.Tries) != 2 {
		t.Errorf("%v after %d tries; want a BudgetSpent after 2", err, len(tr.Tries))
	}
}
