package nl_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/nl"
	"github.com/panyam/jaala/ns"
)

// scripted is a Model replying with its replies in turn, an error for "!fail", recording each Prompt.
type scripted struct {
	name    string
	replies []string
	asked   []nl.Prompt
}

func (s *scripted) Name() string { return s.name }

func (s *scripted) Complete(_ context.Context, p nl.Prompt) (nl.Completion, error) {
	s.asked = append(s.asked, p)
	if len(s.asked) > len(s.replies) {
		return nl.Completion{}, errors.New("out of replies")
	}
	r := s.replies[len(s.asked)-1]
	if r == "!fail" {
		return nl.Completion{}, errors.New("server down")
	}
	return nl.Completion{Text: r, TokensIn: 100 * len(s.asked), TokensOut: len(r), TokensCached: 90}, nil
}

func model(name string, replies ...string) *scripted { return &scripted{name: name, replies: replies} }

func kinds(tr nl.Trace) []string {
	var out []string
	for _, t := range tr.Tries {
		out = append(out, t.Kind)
	}
	return out
}

// graphBase is a Base over the test vocabulary with a three-node chain, a->b->c.
func graphBase(t *testing.T, v *ns.Vocabulary) *datalog.Base {
	t.Helper()
	src := ns.NewMemSource().Declare("edge", "from", "to").Declare("node", "name").Declare("weight", "node", "w")
	src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S("a"), ns.S("b")}})
	src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S("b"), ns.S("c")}})
	b, err := datalog.NewBase(v, src)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTranslateAcceptsAProgramThatChecks(t *testing.T) {
	v := vocabulary(t)
	m := model("small", `edge("a", ?b) => ?b`)
	p, tr, err := nl.Translate(context.Background(), "where does a lead?", v, nil, m, nl.Options{})
	if err != nil || p.Text != `edge("a", ?b) => ?b` || p.Model != "small" || len(p.Query.Goal.Literals) != 1 {
		t.Fatalf("Translate = %+v, %v", p, err)
	}
	if len(tr.Tries) != 1 || tr.Tries[0].Kind != "" || tr.Tries[0].Err != nil || tr.Tries[0].TokensIn != 100 || tr.Tries[0].TokensCached != 90 || tr.Tries[0].Model != "small" {
		t.Errorf("trace %+v", tr)
	}
	if got := m.asked[0].Turns; len(got) != 1 || got[0].Role != nl.RoleUser || got[0].Text != "where does a lead?" {
		t.Errorf("asked %+v", got)
	}
}

// Each refused draft goes back to the model with what Explain says about it, and the next prompt
// carries the whole conversation so far.
func TestTranslateFeedsEachErrorBack(t *testing.T) {
	v := vocabulary(t)
	m := model("small", `reach("a", ?b) => ?b`, `graph.reach("a") => ?b`, `graph.reach("a", ?b) => ?b`)
	p, tr, err := nl.Translate(context.Background(), "what does a reach?", v, nil, m, nl.Options{})
	if err != nil || p.Text != `graph.reach("a", ?b) => ?b` {
		t.Fatalf("Translate = %+v, %v", p, err)
	}
	if got := kinds(tr); !slices.Equal(got, []string{"unknown", "arity", ""}) {
		t.Errorf("kinds %v", got)
	}
	if len(m.asked) != 3 {
		t.Fatalf("asked %d times", len(m.asked))
	}
	turns := m.asked[2].Turns
	if len(turns) != 5 || turns[1].Role != nl.RoleModel || turns[1].Text != `reach("a", ?b) => ?b` || turns[3].Text != `graph.reach("a") => ?b` {
		t.Fatalf("third prompt's turns %+v", turns)
	}
	want := nl.Explain(`reach("a", ?b) => ?b`, tr.Tries[0].Err)
	if turns[2].Role != nl.RoleUser || !strings.HasPrefix(turns[2].Text, want) || !strings.Contains(want, `did you mean "graph.reach"`) || !strings.Contains(want, "^") {
		t.Errorf("fed back %q, want it to start with %q", turns[2].Text, want)
	}
	if !strings.Contains(turns[4].Text, "fix: graph.reach(") {
		t.Errorf("the arity error fed back %q, want its fix", turns[4].Text)
	}
	if m.asked[0].Prefix != m.asked[2].Prefix {
		t.Errorf("the prefix changed between tries")
	}
}

// A model that runs out of tries hands the question to the fallback, which starts afresh.
func TestTranslateFallsBackAfterTheLastTry(t *testing.T) {
	v := vocabulary(t)
	small := model("small", `edge("a", ?b => ?b`, `edge("a" ?b) => ?b`)
	big := model("big", `graph.reach("a", ?b) => ?b`)
	p, tr, err := nl.Translate(context.Background(), "what does a reach?", v, nil, small, nl.Options{MaxTries: 2, Fallback: big})
	if err != nil || p.Model != "big" {
		t.Fatalf("Translate = %+v, %v", p, err)
	}
	if got := kinds(tr); !slices.Equal(got, []string{"parse", "parse", ""}) || tr.Tries[2].Model != "big" {
		t.Errorf("kinds %v, last try by %s", got, tr.Tries[2].Model)
	}
	if len(big.asked) != 1 || len(big.asked[0].Turns) != 1 {
		t.Errorf("the fallback was asked %+v, want the question alone", big.asked)
	}
}

// With a Base, a program that checks is run, and one answering nothing goes straight to the
// fallback, with a note. control: without a Base the same program is accepted.
func TestTranslateSendsAnEmptyAnswerToTheFallback(t *testing.T) {
	v := vocabulary(t)
	b := graphBase(t, v)
	empty := `edge("c", ?b) => ?b`
	small := model("small", empty, `edge("a", ?b) => ?b`)
	big := model("big", `edge("b", ?b) => ?b`)
	p, tr, err := nl.Translate(context.Background(), "where does c lead?", v, nil, small, nl.Options{Base: b, Fallback: big})
	if err != nil || p.Model != "big" || len(p.Rows) != 1 || p.Rows[0].Bind["b"].S != "c" {
		t.Fatalf("Translate = %+v, %v", p, err)
	}
	if got := kinds(tr); !slices.Equal(got, []string{nl.KindEmpty, ""}) || len(small.asked) != 1 {
		t.Errorf("kinds %v, small asked %d times", got, len(small.asked))
	}
	if !strings.Contains(big.asked[0].Turns[0].Text, "answered nothing") {
		t.Errorf("the fallback wasn't told: %q", big.asked[0].Turns[0].Text)
	}
	p, _, err = nl.Translate(context.Background(), "where does c lead?", v, nil, model("small", empty), nl.Options{})
	if err != nil || p.Text != empty || p.Rows != nil {
		t.Errorf("control: without a Base, %+v %v", p, err)
	}
}

// An evaluation error over a Base is fed back as a check error is.
func TestTranslateFeedsAnEvaluationErrorBack(t *testing.T) {
	v := vocabulary(t)
	m := model("small", `graph.reach(?a, ?b) => ?a, ?b`, `edge("a", ?b) => ?b`)
	p, tr, err := nl.Translate(context.Background(), "q", v, nil, m, nl.Options{Base: graphBase(t, v), Eval: []datalog.Option{datalog.Budget(2)}})
	if err != nil || p.Text != `edge("a", ?b) => ?b` {
		t.Fatalf("Translate = %+v, %v", p, err)
	}
	if got := kinds(tr); !slices.Equal(got, []string{nl.KindEval, ""}) {
		t.Errorf("kinds %v (%v)", got, tr.Tries[0].Err)
	}
}

// When every try fails, the error wraps the last one and the trace holds them all.
func TestTranslateFailsWithTheLastError(t *testing.T) {
	v := vocabulary(t)
	_, tr, err := nl.Translate(context.Background(), "q", v, nil, model("small", `nope(?a) => ?a`, `nope(?a) => ?a`, `edge(?a) => ?a`), nl.Options{})
	var ce *datalog.CheckError
	if err == nil || !errors.As(err, &ce) || ce.Kind != datalog.KindArity || !strings.Contains(err.Error(), "3 tries") {
		t.Fatalf("err %v", err)
	}
	if got := kinds(tr); !slices.Equal(got, []string{"unknown", "unknown", "arity"}) {
		t.Errorf("kinds %v", got)
	}
}

// A model that fails is one try, of kind model, and the fallback takes over rather than the same
// model being asked again.
func TestTranslateMovesOnFromAFailingModel(t *testing.T) {
	v := vocabulary(t)
	small := model("small", "!fail", `edge("a", ?b) => ?b`)
	big := model("big", `edge("a", ?b) => ?b`)
	p, tr, err := nl.Translate(context.Background(), "q", v, nil, small, nl.Options{Fallback: big})
	if err != nil || p.Model != "big" || len(small.asked) != 1 {
		t.Fatalf("Translate = %+v, %v, small asked %d", p, err, len(small.asked))
	}
	if got := kinds(tr); !slices.Equal(got, []string{nl.KindModel, ""}) {
		t.Errorf("kinds %v", got)
	}
}

// A reply in a Markdown fence is the program inside it.
func TestTranslateTakesTheProgramOutOfAFence(t *testing.T) {
	v := vocabulary(t)
	for _, reply := range []string{
		"Here it is:\n```datalog\nedge(\"a\", ?b) => ?b\n```\nThat lists them.",
		"```\nedge(\"a\", ?b) => ?b\n```",
		"```edge(\"a\", ?b) => ?b```",
		"  edge(\"a\", ?b) => ?b\n",
	} {
		p, _, err := nl.Translate(context.Background(), "q", v, nil, model("small", reply), nl.Options{})
		if err != nil || p.Text != `edge("a", ?b) => ?b` {
			t.Errorf("%q: %q %v", reply, p.Text, err)
		}
	}
}

type grounder struct {
	cands []nl.Candidate
	err   error
	asked []string
}

func (g *grounder) Ground(_ context.Context, q string) ([]nl.Candidate, error) {
	g.asked = append(g.asked, q)
	return g.cands, g.err
}

// Grounded names go after the question with their values quoted, and a grounding error fails
// Translate before any model is asked.
func TestTranslateHandsTheModelGroundedNames(t *testing.T) {
	v := vocabulary(t)
	g := &grounder{cands: []nl.Candidate{{Name: "a", Values: []string{"pkg/a", "pkg/aa"}}}}
	m := model("small", `edge("pkg/a", ?b) => ?b`)
	if _, _, err := nl.Translate(context.Background(), "where does a lead?", v, g, m, nl.Options{}); err != nil {
		t.Fatal(err)
	}
	q := m.asked[0].Turns[0].Text
	if !strings.HasPrefix(q, "where does a lead?") || !strings.Contains(q, `a: "pkg/a", "pkg/aa"`) || !slices.Equal(g.asked, []string{"where does a lead?"}) {
		t.Errorf("asked %q", q)
	}
	failing := &grounder{err: errors.New("index down")}
	untouched := model("small")
	if _, _, err := nl.Translate(context.Background(), "q", v, failing, untouched, nl.Options{}); err == nil || !errors.Is(err, failing.err) || len(untouched.asked) != 0 {
		t.Errorf("a failing grounder: %v, model asked %d times", err, len(untouched.asked))
	}
}

// The prefix is the card and the instructions, the same for every question, so it caches.
func TestTranslatesPrefixIsTheSameForEveryQuestion(t *testing.T) {
	v := vocabulary(t)
	a, b := model("small", `edge("a", ?b) => ?b`), model("small", `node(?n) => ?n`)
	nl.Translate(context.Background(), "one", v, nil, a, nl.Options{})
	nl.Translate(context.Background(), "two", v, nil, b, nl.Options{})
	card, _ := nl.Card(v)
	if a.asked[0].Prefix != b.asked[0].Prefix || !strings.HasPrefix(a.asked[0].Prefix, card) {
		t.Errorf("prefixes differ or don't start with the card")
	}
}

// A cancelled context stops Translate before it asks again.
func TestTranslateStopsWhenCancelled(t *testing.T) {
	v := vocabulary(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := model("small", `edge("a", ?b) => ?b`)
	if _, _, err := nl.Translate(ctx, "q", v, nil, m, nl.Options{}); !errors.Is(err, context.Canceled) || len(m.asked) != 0 {
		t.Errorf("cancelled: %v, asked %d times", err, len(m.asked))
	}
}

// Every prompt carries the vocabulary's grammar, for a model whose decoder takes one, unless the host
// picks another or none.
func TestTranslateHandsTheModelTheVocabularysGrammar(t *testing.T) {
	v := vocabulary(t)
	want, err := nl.GrammarFor(v)
	if err != nil {
		t.Fatal(err)
	}
	strict, _ := nl.GrammarFor(v, nl.GoalOnly())
	for _, c := range []struct {
		name string
		o    nl.Options
		want string
	}{
		{"default", nl.Options{}, want},
		{"chosen", nl.Options{Grammar: strict}, strict},
		{"none", nl.Options{NoGrammar: true, Grammar: strict}, ""},
	} {
		m := model("small", `egde("a", ?b) => ?b`, `edge("a", ?b) => ?b`)
		if _, _, err := nl.Translate(context.Background(), "q", v, nil, m, c.o); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(m.asked) != 2 || m.asked[0].Grammar != c.want || m.asked[1].Grammar != c.want {
			t.Errorf("%s: the prompts carried another grammar", c.name)
		}
	}
	if want == strict || !strings.HasPrefix(want, "root ::= ") {
		t.Fatal("control: the default and goal-only grammars can't be told apart")
	}
}
