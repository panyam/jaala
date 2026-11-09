package nl_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

func reply(text string) nl.Try { return nl.Try{Model: "small", Text: text} }

// verdict names which of a Verdict's outcomes is set, failing on a Verdict setting none or several.
func verdict(t *testing.T, vd nl.Verdict) string {
	t.Helper()
	var set []string
	if vd.Program != nil {
		set = append(set, "accept")
	}
	if vd.Continue != "" {
		set = append(set, "continue")
	}
	if vd.Stop != nil {
		set = append(set, "stop")
	}
	if len(set) != 1 {
		t.Fatalf("verdict sets %v: %+v", set, vd)
	}
	return set[0]
}

// A refused answer continues with what Explain says about it and the fix line, and an answer that
// checks is accepted, the fence taken off.
func TestAConversationContinuesWithWhatExplainSays(t *testing.T) {
	v := vocabulary(t)
	s, err := nl.NewSession(context.Background(), "q", v, nil, nl.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := s.NewConversation(nil, true)
	bad := `reach("a", ?b) => ?b`
	vd := c.Judge(context.Background(), reply(bad))
	if verdict(t, vd) != "continue" || len(vd.Tries) != 1 || vd.Tries[0].Kind != string(datalog.KindUnknown) {
		t.Fatalf("verdict %+v", vd)
	}
	if want := strings.TrimRight(nl.Explain(bad, vd.Tries[0].Err), "\n"); !strings.HasPrefix(vd.Continue, want+"\n\n") || !strings.HasSuffix(vd.Continue, "Write the whole program again, fixed.") {
		t.Errorf("continue %q, want %q and the fix line", vd.Continue, want)
	}
	vd = c.Judge(context.Background(), reply("```\ngraph.reach(\"a\", ?b) => ?b\n```"))
	if verdict(t, vd) != "accept" || vd.Program.Text != `graph.reach("a", ?b) => ?b` || vd.Program.Model != "small" {
		t.Errorf("verdict %+v", vd)
	}
}

// A repeated draft stops the turn, wrapping the first one's error. control: a different draft
// continues.
func TestAConversationStopsOnARepeat(t *testing.T) {
	v := vocabulary(t)
	s, _ := nl.NewSession(context.Background(), "q", v, nil, nl.Options{})
	c := s.NewConversation(nil, true)
	c.Judge(context.Background(), reply(`nope(?a) => ?a`))
	vd := c.Judge(context.Background(), reply("```\nnope(?a) => ?a\n```"))
	var ce *datalog.CheckError
	if verdict(t, vd) != "stop" || vd.Tries[0].Kind != nl.KindRepeat || !errors.As(vd.Stop, &ce) || ce.Kind != datalog.KindUnknown {
		t.Errorf("verdict %+v", vd)
	}
	c = s.NewConversation(nil, true)
	c.Judge(context.Background(), reply(`nope(?a) => ?a`))
	if vd := c.Judge(context.Background(), reply(`nope(?b) => ?b`)); verdict(t, vd) != "continue" {
		t.Errorf("control: a different draft %+v", vd)
	}
}

// An empty answer stops the turn while another model is left, and that model's first message says
// so; with none left it continues with FixEmpty.
func TestAConversationStopsOnAnEmptyAnswerOnlyWhileAFallbackIsLeft(t *testing.T) {
	v := vocabulary(t)
	b := graphBase(t, v)
	s, _ := nl.NewSession(context.Background(), "where does c lead?", v, nil, nl.Options{Base: b})
	empty := `edge("c", ?b) => ?b`
	vd := s.NewConversation(nil, false).Judge(context.Background(), reply(empty))
	var ea *nl.EmptyAnswer
	if verdict(t, vd) != "stop" || !errors.As(vd.Stop, &ea) {
		t.Fatalf("verdict %+v", vd)
	}
	if ask := s.NewConversation(vd.Stop, true).Ask(); !strings.HasPrefix(ask, "where does c lead?\n\n") || !strings.Contains(ask, "answered nothing") {
		t.Errorf("the fallback's first message %q", ask)
	}
	if ask := s.NewConversation(errors.New("x"), true).Ask(); ask != "where does c lead?" {
		t.Errorf("control: after another error the first message is %q", ask)
	}
	vd = s.NewConversation(nil, true).Judge(context.Background(), reply(empty))
	if verdict(t, vd) != "continue" || !strings.Contains(vd.Continue, "other members or values") {
		t.Errorf("control: with no fallback left %+v", vd)
	}
}

// The first repeat of an empty draft continues, asked again and marked Reasked (#483); the second
// stops.
func TestAConversationAsksARepeatedEmptyDraftAgainOnce(t *testing.T) {
	v := vocabulary(t)
	s, _ := nl.NewSession(context.Background(), "q", v, nil, nl.Options{Base: graphBase(t, v), MaxTries: 5})
	c := s.NewConversation(nil, true)
	empty := `edge("c", ?b) => ?b`
	c.Judge(context.Background(), reply(empty))
	vd := c.Judge(context.Background(), reply(empty))
	if verdict(t, vd) != "continue" || !vd.Tries[0].Reasked || vd.Tries[0].Kind != nl.KindRepeat || !strings.Contains(vd.Continue, "same program you sent before") {
		t.Fatalf("verdict %+v", vd)
	}
	if vd := c.Judge(context.Background(), reply(empty)); verdict(t, vd) != "stop" || vd.Tries[0].Reasked {
		t.Errorf("the second repeat %+v", vd)
	}
}

// A model's last try stops rather than continuing, with its refusal. control: the try before it
// continues.
func TestAConversationStopsWhenTheTriesRunOut(t *testing.T) {
	v := vocabulary(t)
	s, _ := nl.NewSession(context.Background(), "q", v, nil, nl.Options{MaxTries: 2})
	c := s.NewConversation(nil, true)
	if vd := c.Judge(context.Background(), reply(`a(`)); verdict(t, vd) != "continue" {
		t.Fatalf("control: the first try %+v", vd)
	}
	vd := c.Judge(context.Background(), reply(`b(`))
	if verdict(t, vd) != "stop" || vd.Tries[0].Kind != nl.KindParse || vd.Stop != vd.Tries[0].Err {
		t.Errorf("the last try %+v", vd)
	}
	if s.MaxTries() != 2 {
		t.Errorf("MaxTries %d", s.MaxTries())
	}
}

// An answer the pack's Check refuses continues, as kind answer.
func TestAConversationContinuesWhenThePackRefusesTheAnswer(t *testing.T) {
	v := vocabulary(t)
	pack := &nl.Pack{Check: func(context.Context, nl.Program) error { return errors.New("not a list of nodes") }}
	s, _ := nl.NewSession(context.Background(), "q", v, nil, nl.Options{Pack: pack})
	vd := s.NewConversation(nil, true).Judge(context.Background(), reply(`edge("a", ?b) => ?b`))
	if verdict(t, vd) != "continue" || vd.Tries[0].Kind != nl.KindAnswer || !strings.Contains(vd.Continue, "not a list of nodes") {
		t.Errorf("verdict %+v", vd)
	}
}

// A value an action showed repairs a later guess at its spelling: the verdict accepts the repair and
// records the model's own try before it. control: with no action taken, the guess goes back.
func TestAConversationAcceptsARepairOfWhatAnActionShowed(t *testing.T) {
	v, b := guide.Deps(t)
	s, _ := nl.NewSession(context.Background(), mitQ, v, nil, nl.Options{Base: b, Actions: &nl.Actions{}})
	guess := `pkg(?p, "mit") => ?p`
	if verdict(t, s.NewConversation(nil, true).Judge(context.Background(), reply(guess))) != "continue" {
		t.Fatalf("control: an unseen spelling is repaired")
	}
	if got := s.Act(context.Background(), nl.ActionValues, "pkg.license"); !strings.Contains(got, `"MIT"`) {
		t.Fatalf("values answered %q", got)
	}
	vd := s.NewConversation(nil, true).Judge(context.Background(), reply(guess))
	if verdict(t, vd) != "accept" || vd.Program.Text != mit || len(vd.Tries) != 2 || vd.Tries[0].Text != guess || vd.Tries[1].Repair != `"mit" to "MIT"` {
		t.Errorf("verdict %+v", vd)
	}
}

// Compact holds each action taken and each refusal; a new conversation starts it afresh.
func TestAConversationCompactsWhatItWasTold(t *testing.T) {
	v, b := guide.Deps(t)
	s, _ := nl.NewSession(context.Background(), mitQ, v, nil, nl.Options{Base: b, Actions: &nl.Actions{Compact: true}})
	c := s.NewConversation(nil, true)
	c.Act(context.Background(), nl.ActionLook, "pkg")
	c.Judge(context.Background(), reply(`pkg(?p, "nope") => ?p`))
	got := c.Compact()
	if len(got) != 1 || !strings.HasPrefix(got[0].Text, mitQ) || !strings.Contains(got[0].Text, "- look pkg") || !strings.Contains(got[0].Text, `refused (empty)`) {
		t.Errorf("compact %+v", got)
	}
	if got := s.NewConversation(nil, true).Compact(); len(got) != 1 || got[0].Text != mitQ {
		t.Errorf("a new conversation's compact %+v", got)
	}
}

// Two draws answering the same rows key alike though their text differs, a refused draw comes back
// with its error, and without a Base the key is the program.
func TestADrawIsKeyedByItsRows(t *testing.T) {
	v := vocabulary(t)
	s, _ := nl.NewSession(context.Background(), "q", v, nil, nl.Options{Base: graphBase(t, v)})
	_, a, err := s.Draw(context.Background(), reply(`edge("a", ?b) => ?b`))
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := s.Draw(context.Background(), reply(`edge(?a, "c") => ?a`))
	if err != nil {
		t.Fatal(err)
	}
	_, c, err := s.Draw(context.Background(), reply(`edge("b", ?b) => ?b`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Key(a) != s.Key(b) || s.Key(a) == s.Key(c) {
		t.Errorf("keys %q %q %q", s.Key(a), s.Key(b), s.Key(c))
	}
	if try, _, err := s.Draw(context.Background(), reply(`nope(?a) => ?a`)); err == nil || try.Kind != string(datalog.KindUnknown) {
		t.Errorf("a refused draw: %v %+v", err, try)
	}
	plain, _ := nl.NewSession(context.Background(), "q", v, nil, nl.Options{})
	if plain.Key(a) == plain.Key(b) {
		t.Errorf("control: without a Base two programs keyed alike")
	}
}

// A pack with Callables has a pick: none stops with ErrNoCall and no try, and a pick is a call try.
// control: a pack without Callables has none.
func TestAPickStopsOnNone(t *testing.T) {
	v := vocabulary(t)
	s, _ := nl.NewSession(context.Background(), "q", v, nil, nl.Options{Base: graphBase(t, v), Pack: edges()})
	pick, err := s.Pick()
	if err != nil || pick == nil || !strings.Contains(pick.Prefix(), "# Answer the question with one call") || !strings.Contains(pick.Grammar(), `"none"`) {
		t.Fatalf("pick %v %v", pick, err)
	}
	vd := pick.NewConversation(nil, true).Judge(context.Background(), reply("none"))
	if verdict(t, vd) != "stop" || !errors.Is(vd.Stop, nl.ErrNoCall) || len(vd.Tries) != 0 {
		t.Errorf("none %+v", vd)
	}
	vd = pick.NewConversation(nil, true).Judge(context.Background(), reply(`edge("a", ?to)`))
	if verdict(t, vd) != "accept" || vd.Tries[0].Mode != nl.ModeCall {
		t.Errorf("a pick %+v", vd)
	}
	plain, _ := nl.NewSession(context.Background(), "q", v, nil, nl.Options{Pack: &nl.Pack{About: "a small graph"}})
	if p, err := plain.Pick(); p != nil || err != nil {
		t.Errorf("control: a pack without callables picks: %v %v", p, err)
	}
}

// A conversation counts its actions: the same one asked twice isn't run again and opens with
// AskedAgain over what it answered first, taking no step; the last one MaxSteps allows ends with
// LastLook; and a new conversation starts the count again.
func TestAConversationCountsItsActions(t *testing.T) {
	v, b := guide.Deps(t)
	d := nl.DefaultPrompts()
	s, _ := nl.NewSession(context.Background(), mitQ, v, nil, nl.Options{Base: b, Actions: &nl.Actions{Steps: 2}})
	c := s.NewConversation(nil, true)
	first := c.Act(context.Background(), nl.ActionLook, "pkg")
	if strings.HasPrefix(first, d.AskedAgain) || strings.HasSuffix(first, d.LastLook) || c.Steps() != 1 {
		t.Errorf("first look %q, steps %d", first, c.Steps())
	}
	if !c.Repeats(nl.ActionLook, "pkg") || c.Repeats(nl.ActionValues, "pkg.license") {
		t.Errorf("Repeats: the look %v, values %v", c.Repeats(nl.ActionLook, "pkg"), c.Repeats(nl.ActionValues, "pkg.license"))
	}
	again := c.Act(context.Background(), nl.ActionLook, "pkg")
	if again != d.AskedAgain+" "+first || c.Steps() != 1 {
		t.Errorf("second look %q, steps %d", again, c.Steps())
	}
	values := c.Act(context.Background(), nl.ActionValues, "pkg.license")
	if !strings.HasSuffix(values, "\n\n"+d.LastLook) || c.Steps() != 2 {
		t.Errorf("values %q, steps %d", values, c.Steps())
	}
	if c := s.NewConversation(nil, true); c.Steps() != 0 || strings.HasPrefix(c.Act(context.Background(), nl.ActionLook, "pkg"), d.AskedAgain) {
		t.Errorf("a new conversation carried the count")
	}
}

// The MaxRepeats-th repeat ends the actions and says so with LastLook (#517); the repeats before it
// leave the steps alone. control: a conversation with as many distinct actions keeps its steps.
func TestRepeatsEndTheActionsAtTheCap(t *testing.T) {
	v, b := guide.Deps(t)
	d := nl.DefaultPrompts()
	s, _ := nl.NewSession(context.Background(), mitQ, v, nil, nl.Options{Base: b, Actions: &nl.Actions{Steps: 10}})
	c := s.NewConversation(nil, true)
	nope := `pkg(?p, "nope") => ?p`
	c.Act(context.Background(), nl.ActionTry, nope)
	c.Act(context.Background(), nl.ActionLook, "pkg")
	for i := 1; i < nl.MaxRepeats; i++ {
		action, arg := nl.ActionTry, nope
		if i%2 == 0 {
			action, arg = nl.ActionLook, "pkg"
		}
		if r := c.Act(context.Background(), action, arg); strings.HasSuffix(r, d.LastLook) || c.Steps() != 2 {
			t.Fatalf("repeat %d answered %q, steps %d", i, r, c.Steps())
		}
	}
	last := c.Act(context.Background(), nl.ActionTry, nope)
	if !strings.HasPrefix(last, d.TriedAgain) || !strings.HasSuffix(last, "\n\n"+d.LastLook) || c.Steps() != 10 {
		t.Errorf("repeat %d answered %q, steps %d", nl.MaxRepeats, last, c.Steps())
	}
	ctl := s.NewConversation(nil, true)
	for i := range nl.MaxRepeats + 2 {
		ctl.Act(context.Background(), nl.ActionTry, fmt.Sprintf(`pkg(?p, "n%d") => ?p`, i))
	}
	if ctl.Steps() != nl.MaxRepeats+2 {
		t.Errorf("control: %d distinct tries took %d steps", nl.MaxRepeats+2, ctl.Steps())
	}
}

// The Ledger lists what the actions found and each draft with what it did, under their headings, and
// a list with nothing in it is left out. Compact under Ledger leaves those lines to it. control:
// without Ledger, Compact lists them.
func TestTheLedgerSplitsFindingsFromDrafts(t *testing.T) {
	v, b := guide.Deps(t)
	d := nl.DefaultPrompts()
	s, _ := nl.NewSession(context.Background(), mitQ, v, nil, nl.Options{Base: b, Actions: &nl.Actions{Ledger: true, Compact: true}})
	c := s.NewConversation(nil, true)
	if c.Ledger() != "" {
		t.Errorf("an empty ledger %q", c.Ledger())
	}
	c.Act(context.Background(), nl.ActionValues, "pkg.license")
	if l := c.Ledger(); !strings.HasPrefix(l, d.LedgerFound+"\n- values pkg.license") || strings.Contains(l, d.LedgerTried) {
		t.Errorf("after values, the ledger %q", l)
	}
	nope := `pkg(?p, "nope") => ?p`
	c.Act(context.Background(), nl.ActionTry, nope)
	if vd := c.Judge(context.Background(), reply(`pkg(?p, "none") => ?p`)); vd.Continue == "" {
		t.Fatalf("the answer wasn't refused: %+v", vd)
	}
	l := c.Ledger()
	found, tried, ok := strings.Cut(l, "\n\n"+d.LedgerTried+"\n")
	if !ok || strings.Contains(found, nope) || !strings.Contains(tried, "- try "+nope) || !strings.Contains(tried, `- pkg(?p, "none") => ?p`) {
		t.Errorf("the ledger %q", l)
	}
	if turn := c.Compact()[0].Text; strings.Contains(turn, d.CompactFound) || !strings.Contains(turn, d.CompactRefused) {
		t.Errorf("Compact under Ledger %q", turn)
	}
	plain, _ := nl.NewSession(context.Background(), mitQ, v, nil, nl.Options{Base: b, Actions: &nl.Actions{Compact: true}})
	pc := plain.NewConversation(nil, true)
	pc.Act(context.Background(), nl.ActionValues, "pkg.license")
	if turn := pc.Compact()[0].Text; !strings.Contains(turn, d.CompactFound+"\n- values pkg.license") {
		t.Errorf("control: Compact without Ledger %q", turn)
	}
}

// Tools are the actions to offer as tools, find only with a Grounder, values naming the front end's
// target; with AsTools the prefix and grammar leave the text actions out. control: without AsTools
// they're in.
func TestASessionOffersItsActionsAsTools(t *testing.T) {
	v := vocabulary(t)
	g := &grounder{cands: []nl.Candidate{{Name: "a", Values: []string{"a"}}}}
	s, _ := nl.NewSession(context.Background(), "q", v, g, nl.Options{Actions: &nl.Actions{AsTools: true}})
	var names []string
	for _, tl := range s.Tools() {
		names = append(names, tl.Action+"("+tl.Arg+")")
	}
	if got := strings.Join(names, " "); got != "look(name) values(target) try(draft) find(name)" {
		t.Errorf("tools %s", got)
	}
	if d := s.Tools()[1].Description; !strings.Contains(d, "relation.argument") {
		t.Errorf("values' description %q", d)
	}
	if strings.Contains(s.Prefix(), "#   look NAME") || s.ActionGrammar() != s.Grammar() {
		t.Errorf("AsTools left the text actions in")
	}
	plain, _ := nl.NewSession(context.Background(), "q", v, nil, nl.Options{Actions: &nl.Actions{}})
	if len(plain.Tools()) != 3 || !strings.Contains(plain.Prefix(), "#   look NAME") || plain.ActionGrammar() == plain.Grammar() {
		t.Errorf("control: without AsTools, %d tools", len(plain.Tools()))
	}
	if none, _ := nl.NewSession(context.Background(), "q", v, nil, nl.Options{}); none.Tools() != nil {
		t.Errorf("tools without Actions")
	}
}

// Under AsTools, TextActions still hands a model that writes its actions the prefix listing them
// (find among them with a Grounder) and the grammar taking them, the same text the session writes
// without AsTools. Without Actions it is Prefix and Grammar.
func TestASessionKeepsItsTextActionsUnderAsTools(t *testing.T) {
	v := vocabulary(t)
	g := &grounder{cands: []nl.Candidate{{Name: "a", Values: []string{"a"}}}}
	s, _ := nl.NewSession(context.Background(), "q", v, g, nl.Options{Actions: &nl.Actions{AsTools: true}})
	plain, _ := nl.NewSession(context.Background(), "q", v, g, nl.Options{Actions: &nl.Actions{}})
	prefix, grammar := s.TextActions()
	if prefix != plain.Prefix() || grammar != plain.ActionGrammar() {
		t.Errorf("TextActions differ from the text session's prefix and grammar")
	}
	if !strings.Contains(prefix, "#   find NAME") || !strings.Contains(grammar, `"find "`) || prefix == s.Prefix() {
		t.Errorf("the text actions leave out find, or are the tools' prefix")
	}
	none, _ := nl.NewSession(context.Background(), "q", v, nil, nl.Options{})
	if p, gr := none.TextActions(); p != none.Prefix() || gr != none.Grammar() {
		t.Errorf("without Actions, TextActions is not Prefix and Grammar")
	}
}

// ReadAction takes a fence off and reads the actions; a draft, or a look with no name, is an answer.
func TestReadActionTellsAnActionFromAnAnswer(t *testing.T) {
	for _, c := range []struct{ reply, action, arg string }{
		{"look pkg", nl.ActionLook, "pkg"},
		{"```\nvalues pkg.license\n```", nl.ActionValues, "pkg.license"},
		{"try\npkg(?p, _) => ?p", nl.ActionTry, "pkg(?p, _) => ?p"},
		{"find mit", nl.ActionFind, "mit"},
		{`pkg(?p, "MIT") => ?p`, "", ""},
		{"look", "", ""},
	} {
		action, arg, ok := nl.ReadAction(c.reply)
		if action != c.action || arg != c.arg || ok != (c.action != "") {
			t.Errorf("ReadAction(%q) = %q, %q, %v", c.reply, action, arg, ok)
		}
	}
}

// A try of a draft tried before isn't run and takes no step: it gets TriedAgain and what the draft
// answered then. control: another draft runs and takes one.
func TestARepeatedTryIsNotRunAgain(t *testing.T) {
	v, b := guide.Deps(t)
	d := nl.DefaultPrompts()
	s, _ := nl.NewSession(context.Background(), mitQ, v, nil, nl.Options{Base: b, Actions: &nl.Actions{Steps: 3}})
	c := s.NewConversation(nil, true)
	nope := `pkg(?p, "nope") => ?p`
	first := c.Act(context.Background(), nl.ActionTry, nope)
	again := c.Act(context.Background(), nl.ActionTry, nope)
	if again != d.TriedAgain+"\n"+first || c.Steps() != 1 {
		t.Errorf("the repeat answered %q, steps %d", again, c.Steps())
	}
	if other := c.Act(context.Background(), nl.ActionTry, mit); strings.HasPrefix(other, d.TriedAgain) || c.Steps() != 2 {
		t.Errorf("control: another draft answered %q, steps %d", other, c.Steps())
	}
}

// A reply opening with a try line alone is judged as the draft after it, under the final grammar a
// model is held to once its steps run out. control: without Actions, the try line is the draft's.
func TestAnAnswerAfterTheLastStepMayOpenWithTry(t *testing.T) {
	v, b := guide.Deps(t)
	s, _ := nl.NewSession(context.Background(), mitQ, v, nil, nl.Options{Base: b, Actions: &nl.Actions{}})
	vd := s.NewConversation(nil, true).Judge(context.Background(), reply("try\n"+mit))
	if verdict(t, vd) != "accept" || vd.Program.Text != mit {
		t.Errorf("verdict %+v", vd)
	}
	plain, _ := nl.NewSession(context.Background(), mitQ, v, nil, nl.Options{Base: b})
	if vd := plain.NewConversation(nil, true).Judge(context.Background(), reply("try\n"+mit)); vd.Program != nil {
		t.Errorf("control: without Actions, a try line was taken off")
	}
}

// EndActions spends the steps left, and the next reply is the answer.
func TestEndActionsSpendsTheStepsLeft(t *testing.T) {
	v, b := guide.Deps(t)
	s, _ := nl.NewSession(context.Background(), mitQ, v, nil, nl.Options{Base: b, Actions: &nl.Actions{Steps: 4}})
	c := s.NewConversation(nil, true)
	c.Act(context.Background(), nl.ActionLook, "pkg")
	if c.EndActions(); c.Steps() != 4 {
		t.Errorf("steps %d", c.Steps())
	}
}

// A look naming nothing lists the members whose name or first doc line hold the word, names first;
// with nothing listed it answers as find does, and with no Grounder it says what look takes.
// control: a look naming a member answers its card line.
func TestALookForAPlainWordSearches(t *testing.T) {
	v, b := guide.Deps(t)
	d := nl.DefaultPrompts()
	s, _ := nl.NewSession(context.Background(), mitQ, v, nil, nl.Options{Base: b, Actions: &nl.Actions{}})
	got := s.Act(context.Background(), nl.ActionLook, "license")
	if !strings.HasPrefix(got, "Nothing is named license; these mention it:\n") || !strings.Contains(got, "pkg(name, license)") || strings.Contains(got, "imports(") {
		t.Errorf("look license: %q", got)
	}
	if got := s.Act(context.Background(), nl.ActionLook, "IMPORT"); !strings.Contains(got, "imports(from, to)") {
		t.Errorf("look IMPORT: %q", got)
	}
	if got := s.Act(context.Background(), nl.ActionLook, "zebra"); !strings.HasSuffix(got, "\n"+d.LookNothing) {
		t.Errorf("look zebra: %q", got)
	}
	g := &grounder{cands: []nl.Candidate{{Name: "zebra", Values: []string{"app"}}}}
	found, _ := nl.NewSession(context.Background(), mitQ, v, g, nl.Options{Base: b, Actions: &nl.Actions{}})
	if got := found.Act(context.Background(), nl.ActionLook, "zebra"); got != found.Act(context.Background(), nl.ActionFind, "zebra") {
		t.Errorf("look zebra with a Grounder: %q", got)
	}
	if got := s.Act(context.Background(), nl.ActionLook, "pkg"); strings.Contains(got, "mention") || !strings.Contains(got, "pkg(name, license)") {
		t.Errorf("control: look pkg: %q", got)
	}
}
