package nl_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

// The pack's part of the prefix follows the card and the instructions, the same for every question.
func TestTranslateHandsTheModelThePack(t *testing.T) {
	v := vocabulary(t)
	pack := &nl.Pack{
		About:    "Roads between towns.\nEach edge is a road one way.",
		Card:     `# Towns are named in lower case: "a", "b".`,
		Examples: []nl.Example{{Question: "which towns can a reach?", Program: `graph.reach("a", ?t) => ?t`}},
	}
	a, b := model("small", `edge("a", ?b) => ?b`), model("small", `node(?n) => ?n`)
	if _, _, err := nl.Translate(context.Background(), "one", v, nil, a, nl.Options{Pack: pack}); err != nil {
		t.Fatal(err)
	}
	nl.Translate(context.Background(), "two", v, nil, b, nl.Options{Pack: pack})
	plain := model("small", `node(?n) => ?n`)
	nl.Translate(context.Background(), "two", v, nil, plain, nl.Options{})
	want := `
# What the data is about:
# Roads between towns.
# Each edge is a road one way.

# Towns are named in lower case: "a", "b".

# Examples:
# which towns can a reach?
graph.reach("a", ?t) => ?t
`
	got := a.asked[0].Prefix
	if got != plain.asked[0].Prefix+want || pack.Prefix() != want {
		t.Errorf("prefix %q, want the plain one followed by %q", got, want)
	}
	if b.asked[0].Prefix != got {
		t.Errorf("the prefix changed with the question")
	}
}

// A pack whose example doesn't check fails Translate, naming the example, before a model is asked.
func TestTranslateRefusesAPackWhoseExampleDoesntCheck(t *testing.T) {
	v := vocabulary(t)
	pack := &nl.Pack{Examples: []nl.Example{
		{Question: "where does a lead?", Program: `edge("a", ?b) => ?b`},
		{Question: "what does a reach?", Program: `reach("a", ?b) => ?b`},
	}}
	m := model("small", `edge("a", ?b) => ?b`)
	_, tr, err := nl.Translate(context.Background(), "q", v, nil, m, nl.Options{Pack: pack})
	var ce *datalog.CheckError
	if err == nil || !errors.As(err, &ce) || !strings.Contains(err.Error(), `example 2 ("what does a reach?")`) || len(m.asked) != 0 || len(tr.Tries) != 0 {
		t.Fatalf("Translate = %v, model asked %d times", err, len(m.asked))
	}
	if err := pack.Validate(v); err == nil || !strings.Contains(err.Error(), `unknown relation "reach"`) {
		t.Errorf("Validate = %v", err)
	}
	pack.Examples[1].Program = `graph.reach("a", ?b) => ?b`
	if err := pack.Validate(v); err != nil {
		t.Errorf("control: the fixed pack: %v", err)
	}
}

// The pack's Check refuses an answer as "answer" and its text goes back to the same model, whose
// next draft is accepted; without a Check the first draft is.
func TestTranslateFeedsAPacksAnswerCheckBack(t *testing.T) {
	v := vocabulary(t)
	b := graphBase(t, v)
	pack := &nl.Pack{Check: func(_ context.Context, p nl.Program) error {
		if len(p.Rows) > 1 {
			return fmt.Errorf("%d rows, but the question asks for one town", len(p.Rows))
		}
		return nil
	}}
	m := model("small", `edge(?a, ?b) => ?b`, `edge("a", ?b) => ?b`)
	big := model("big", `edge("a", ?b) => ?b`)
	p, tr, err := nl.Translate(context.Background(), "where does a lead?", v, nil, m, nl.Options{Base: b, Fallback: big, Pack: pack})
	if err != nil || p.Text != `edge("a", ?b) => ?b` || p.Model != "small" || len(p.Rows) != 1 {
		t.Fatalf("Translate = %+v, %v", p, err)
	}
	if got := kinds(tr); !slices.Equal(got, []string{nl.KindAnswer, ""}) || len(big.asked) != 0 {
		t.Errorf("kinds %v, fallback asked %d times", got, len(big.asked))
	}
	if fed := m.asked[1].Turns[2].Text; !strings.Contains(fed, "2 rows, but the question asks for one town") {
		t.Errorf("fed back %q", fed)
	}
	control := model("small", `edge(?a, ?b) => ?b`)
	if p, _, err := nl.Translate(context.Background(), "where does a lead?", v, nil, control, nl.Options{Base: b}); err != nil || len(p.Rows) != 2 {
		t.Errorf("control: without the pack, %+v, %v", p, err)
	}
}

// The pack's Grounder grounds the question when Translate is given none, and one Translate is given
// wins.
func TestTranslateGroundsThroughThePack(t *testing.T) {
	v := vocabulary(t)
	packs := &grounder{cands: []nl.Candidate{{Name: "a", Values: []string{"pkg/a"}}}}
	pack := &nl.Pack{Grounder: packs}
	m := model("small", `edge("pkg/a", ?b) => ?b`)
	if _, _, err := nl.Translate(context.Background(), "where does a lead?", v, nil, m, nl.Options{Pack: pack}); err != nil {
		t.Fatal(err)
	}
	if q := m.asked[0].Turns[0].Text; !strings.Contains(q, `a: "pkg/a"`) || len(packs.asked) != 1 {
		t.Errorf("asked %q, pack's grounder asked %d times", q, len(packs.asked))
	}
	own := &grounder{cands: []nl.Candidate{{Name: "a", Values: []string{"own/a"}}}}
	m = model("small", `edge("own/a", ?b) => ?b`)
	nl.Translate(context.Background(), "where does a lead?", v, own, m, nl.Options{Pack: pack})
	if q := m.asked[0].Turns[0].Text; !strings.Contains(q, `a: "own/a"`) || len(packs.asked) != 1 || len(own.asked) != 1 {
		t.Errorf("control: asked %q, pack's grounder asked %d times", q, len(packs.asked))
	}
}

// partsBase holds thirteen parts of two kinds, ratings whose first argument repeats, a number with a
// unit, an absent value and a long one, and a relation with no facts.
func partsBase(t *testing.T) *datalog.Base {
	t.Helper()
	src := ns.NewMemSource().
		DeclareSchema("part", ns.Schema{Arity: 2, Labels: []string{"ref", "kind"}}).
		DeclareSchema("rating", ns.Schema{Arity: 2, Labels: []string{"ref"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}}).
		Declare("note", "text")
	for i := 1; i <= 13; i++ {
		kind := "resistor"
		if i > 9 {
			kind = "capacitor"
		}
		src.Add("part", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("R%d", i)), ns.S(kind)}})
	}
	src.Add("rating", ns.Tuple{Vals: []ns.Value{ns.S("R1"), ns.NU(5, "V")}})
	src.Add("rating", ns.Tuple{Vals: []ns.Value{ns.S("R1"), ns.Absent()}})
	src.Add("rating", ns.Tuple{Vals: []ns.Value{ns.S(strings.Repeat("x", 45)), ns.NU(5, "V")}})
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
	b, err := datalog.NewBase(v, src)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The data card lists each relation's facts, in the Source's order, and each argument's values, commonest first, summarized
// past twelve.
func TestTheDataCardSaysWhatTheDataHolds(t *testing.T) {
	got, err := nl.DataCard(context.Background(), partsBase(t))
	if err != nil {
		t.Fatal(err)
	}
	want := `# The data: each relation's facts, and each argument's values with how many facts hold each (or how many values, and the commonest, when there are many).
# note(text): 0 facts
# part(ref, kind): 13 facts
#   ref: 13 values, commonest "R1" (1)
#   kind: "resistor" (9), "capacitor" (4)
# rating(ref, arg2): 3 facts
#   ref: "R1" (2), "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"… (1)
#   arg2: 5V (2), absent (1)
`
	if got != want {
		t.Errorf("card:\n%s\nwant:\n%s", got, want)
	}
	if _, err := nl.DataCard(context.Background(), partsBase(t), datalog.Budget(5)); err == nil || !strings.Contains(err.Error(), "counting part's ref") {
		t.Errorf("a budget too small: %v", err)
	}
}
