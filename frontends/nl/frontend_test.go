package nl_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/ns"
)

// shout is a toy front end: a draft is "SAY " and a jaala program. "WHISPER" is a construct it
// refuses as a gap, and anything else without SAY is a compile error. Its explanations say so, so a
// test can tell them from nl.Explain's.
type shout struct{ examples []nl.Example }

func (shout) Language() string { return "shout" }
func (shout) Card(*ns.Vocabulary) (string, error) {
	return "# SHOUT CARD: edge(from, to), node(name)\n", nil
}
func (shout) Instructions() string     { return "# Say SAY, then the program.\n" }
func (s shout) Examples() []nl.Example { return s.examples }
func (shout) Grammar(*ns.Vocabulary) (string, error) {
	return `root ::= "SAY " [a-z(", ?=>)]+` + "\n", nil
}
func (shout) Compile(_ *ns.Vocabulary, text string) (nl.Compiled, error) {
	c := nl.Compiled{Explain: func(err error) string { return "SHOUT ERROR: " + err.Error() }}
	switch {
	case strings.HasPrefix(text, "WHISPER"):
		c.Kind = "gap"
		return c, errors.New("whispering isn't supported yet")
	case !strings.HasPrefix(text, "SAY "):
		return c, errors.New("a draft starts with SAY")
	}
	c.Program = strings.TrimPrefix(text, "SAY ")
	q, err := datalog.Parse(c.Program)
	c.Query = q
	return c, err
}

// The prompt is the front end's card, instructions and examples, with the pack's About but none of its
// jaala examples; the grammar is the front end's. control: without a front end the prompt is jaala's.
func TestAFrontEndWritesThePrompt(t *testing.T) {
	v := vocabulary(t)
	pack := &nl.Pack{About: "Roads between towns.", Examples: []nl.Example{{Question: "jaala example", Program: `edge("a", ?b) => ?b`}}}
	fe := shout{examples: []nl.Example{{Question: "where does a lead?", Program: `SAY edge("a", ?b) => ?b`}}}
	m := model("small", `SAY edge("a", ?b) => ?b`)
	if _, _, err := nl.Translate(context.Background(), "q", v, nil, m, nl.Options{Pack: pack, FrontEnd: fe}); err != nil {
		t.Fatal(err)
	}
	p := m.asked[0]
	for _, want := range []string{"# SHOUT CARD", "# Say SAY", "Roads between towns.", "# where does a lead?\nSAY edge(\"a\", ?b) => ?b"} {
		if !strings.Contains(p.Prefix, want) {
			t.Errorf("prefix lacks %q:\n%s", want, p.Prefix)
		}
	}
	if strings.Contains(p.Prefix, "jaala example") || strings.Contains(p.Prefix, "A directed edge") {
		t.Errorf("prefix carries jaala's card or the pack's jaala examples:\n%s", p.Prefix)
	}
	if !strings.HasPrefix(p.Grammar, `root ::= "SAY "`) {
		t.Errorf("grammar %q, want the front end's", p.Grammar)
	}
	m2 := model("small", `edge("a", ?b) => ?b`)
	if _, _, err := nl.Translate(context.Background(), "q", v, nil, m2, nl.Options{Pack: pack}); err != nil {
		t.Fatal(err)
	}
	if p := m2.asked[0]; !strings.Contains(p.Prefix, "A directed edge") || !strings.Contains(p.Prefix, "jaala example") || strings.Contains(p.Prefix, "SHOUT") {
		t.Errorf("control: jaala's prefix:\n%s", p.Prefix)
	}
	m3 := model("small", `SAY edge("a", ?b) => ?b`)
	if _, _, err := nl.Translate(context.Background(), "q", v, nil, m3, nl.Options{FrontEnd: fe, Grammar: "root ::= \"x\"\n"}); err != nil || m3.asked[0].Grammar != "root ::= \"x\"\n" {
		t.Errorf("Options.Grammar didn't replace the front end's: %q, %v", m3.asked[0].Grammar, err)
	}
}

// A draft is compiled before it is checked, and every refusal (a compile error, a gap, a check error in
// what it compiled to) goes back in the front end's words; the accepted program carries its language
// and what it compiled to, and the report names the language.
func TestAFrontEndsDraftsAreCompiledAndRefusedInItsTerms(t *testing.T) {
	v := vocabulary(t)
	m := model("small", `edge("a", ?b) => ?b`, `WHISPER edge`, `SAY reach("a", ?b) => ?b`, `SAY graph.reach("a", ?b) => ?b`)
	o := nl.Options{FrontEnd: shout{}, MaxTries: 4, Base: graphBase(t, v)}
	p, tr, err := nl.Translate(context.Background(), "what does a reach?", v, nil, m, o)
	if err != nil {
		t.Fatalf("Translate: %v (%v)", err, kinds(tr))
	}
	if got := kinds(tr); !slices.Equal(got, []string{"compile", "gap", "unknown", ""}) {
		t.Errorf("kinds %v", got)
	}
	turns := m.asked[3].Turns
	for i, want := range map[int]string{2: "SHOUT ERROR: a draft starts with SAY", 4: "SHOUT ERROR: whispering", 6: `SHOUT ERROR: query: unknown relation "reach"`} {
		if !strings.HasPrefix(turns[i].Text, want) || !strings.HasSuffix(turns[i].Text, "\n\nWrite the whole query again, fixed.") || strings.Contains(turns[i].Text, "\n\n\n") {
			t.Errorf("turn %d fed back %q, want it to start %q and ask for the query again", i, turns[i].Text, want)
		}
	}
	if p.Text != `SAY graph.reach("a", ?b) => ?b` || p.Compiled != `graph.reach("a", ?b) => ?b` || p.Language != "shout" || len(p.Rows) != 2 {
		t.Errorf("program %+v", p)
	}
	for _, try := range tr.Tries {
		if try.Language != "shout" {
			t.Errorf("try %q has language %q", try.Text, try.Language)
		}
	}
	rep := nl.Eval(context.Background(), []nl.Case{{Name: "reach", Question: "q", Want: [][]string{{"b"}, {"c"}}}}, graphBase(t, v),
		func(ctx context.Context, q string) (nl.Program, nl.Trace, error) {
			return nl.Translate(ctx, q, v, nil, model("small", `SAY graph.reach("a", ?b) => ?b`), o)
		})
	if rep.Language() != "shout" || !strings.Contains(rep.String(), "passed 1/1 in shout,") {
		t.Errorf("report language %q:\n%s", rep.Language(), rep)
	}
	jaala := nl.Eval(context.Background(), []nl.Case{{Name: "reach", Question: "q", Want: [][]string{{"b"}, {"c"}}}}, graphBase(t, v),
		func(ctx context.Context, q string) (nl.Program, nl.Trace, error) {
			return nl.Translate(ctx, q, v, nil, model("small", `graph.reach("a", ?b) => ?b`), nl.Options{})
		})
	if jaala.Language() != "" || !strings.Contains(jaala.String(), "passed 1/1, ") {
		t.Errorf("control: jaala's report:\n%s", jaala)
	}
}

// A front end's example that doesn't compile and check fails Translate before any model is asked.
func TestAFrontEndsBrokenExampleFailsFirst(t *testing.T) {
	v := vocabulary(t)
	m := model("small", `SAY edge("a", ?b) => ?b`)
	for _, ex := range []string{`edge("a", ?b) => ?b`, `SAY nowhere(?b) => ?b`} {
		_, _, err := nl.Translate(context.Background(), "q", v, nil, m, nl.Options{FrontEnd: shout{examples: []nl.Example{{Question: "bad", Program: ex}}}})
		if err == nil || !strings.Contains(err.Error(), `example 1 ("bad")`) {
			t.Errorf("%s: err %v, want the example named", ex, err)
		}
	}
	if len(m.asked) != 0 {
		t.Errorf("the model was asked %d times", len(m.asked))
	}
}
