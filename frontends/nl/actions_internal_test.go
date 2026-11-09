package nl

import (
	"fmt"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/internal/gbnf"
)

// The actions' grammar is GBNF the recognizer reads, and every reply it accepts reads as what it
// looks like: a sampled program alone is an answer, after try on its own line a try of that program,
// and look or values with a name an action with it, and find with a phrase when Translate has a
// Grounder. control: what it refuses, the bare words, a look with a space in its name, and find
// without a Grounder, isn't an action readAction takes either.
func TestEveryReplyTheActionsGrammarAcceptsReadsAsItsAction(t *testing.T) {
	for _, find := range []bool{false, true} {
		t.Run(fmt.Sprintf("find=%v", find), func(t *testing.T) { testActionGrammar(t, find) })
	}
}

func testActionGrammar(t *testing.T, find bool) {
	grammar, err := GrammarFor(grammarVocabulary(t))
	if err != nil {
		t.Fatal(err)
	}
	g := mustGBNF(t, actionGrammar(grammar, find, false, false))
	rules, err := vocabRules(grammarVocabulary(t), grammarOpts{})
	if err != nil {
		t.Fatal(err)
	}
	s := gbnf.NewSampler(table(rules), 1, 4)
	for i := 0; i < 200; i++ {
		prog := s.Sample("root")
		for _, c := range []struct{ reply, action, arg string }{
			{prog, "", ""},
			{"try\n" + prog, ActionTry, strings.TrimSpace(prog)},
		} {
			if !g.Accepts(c.reply) {
				t.Fatalf("the grammar refuses %q", c.reply)
			}
			if action, arg, ok := readAction(c.reply); action != c.action || ok && arg != c.arg {
				t.Fatalf("%q reads as %q %q, want %q %q", c.reply, action, arg, c.action, c.arg)
			}
		}
	}
	for _, reply := range []string{"look pkg", "values pkg.license", "look a.b_c.d9"} {
		if action, _, ok := readAction(reply); !g.Accepts(reply) || !ok || !strings.HasPrefix(reply, action+" ") {
			t.Errorf("%q: accepted %v, read as %q", reply, g.Accepts(reply), action)
		}
	}
	for _, reply := range []string{"look", "values ", "look pkg license", "try", "find", "find "} {
		if g.Accepts(reply) {
			t.Errorf("control: the grammar accepts %q", reply)
		}
	}
	phrase := "find checkout handler"
	if action, arg, ok := readAction(phrase); g.Accepts(phrase) != find || !ok || action != ActionFind || arg != "checkout handler" {
		t.Errorf("%q with find %v: accepted %v, read as %q %q", phrase, find, g.Accepts(phrase), action, arg)
	}
}

// The grammar after the last step takes a sampled program alone or after a try line, which
// withoutTry takes off, and no action. control: the draft's grammar refuses the try line.
func TestTheFinalGrammarTakesATryLineBeforeTheDraft(t *testing.T) {
	grammar, err := GrammarFor(grammarVocabulary(t))
	if err != nil {
		t.Fatal(err)
	}
	g, plain := mustGBNF(t, finalGrammar(grammar)), mustGBNF(t, grammar)
	rules, err := vocabRules(grammarVocabulary(t), grammarOpts{})
	if err != nil {
		t.Fatal(err)
	}
	s := gbnf.NewSampler(table(rules), 2, 4)
	for i := 0; i < 200; i++ {
		prog := s.Sample("root")
		if !g.Accepts(prog) || !g.Accepts("try\n"+prog) {
			t.Fatalf("the final grammar refuses %q, alone or after try", prog)
		}
		if got := withoutTry("try\n" + prog); got != strings.TrimSpace(prog) {
			t.Fatalf("withoutTry gave %q for %q", got, prog)
		}
		if plain.Accepts("try\n" + prog) {
			t.Fatalf("control: the draft's grammar takes a try line before %q", prog)
		}
	}
	for _, reply := range []string{"look pkg", "values pkg.license", "try\n"} {
		if g.Accepts(reply) {
			t.Errorf("the final grammar accepts %q", reply)
		}
	}
}

// Under Build the actions' grammar takes add with any literal the draft grammar writes, and drop, and
// each reads as its action; the answer may be a goal's tail alone, and a sampled literal with that
// tail after it parses, as Judge reads it. control: without Build the grammar refuses all three.
func TestEveryBuildReplyReadsAsItsAction(t *testing.T) {
	grammar, err := GrammarFor(grammarVocabulary(t))
	if err != nil {
		t.Fatal(err)
	}
	g := mustGBNF(t, actionGrammar(buildGrammar(grammar), false, true, false))
	plain := mustGBNF(t, actionGrammar(grammar, false, false, false))
	rules, err := vocabRules(grammarVocabulary(t), grammarOpts{})
	if err != nil {
		t.Fatal(err)
	}
	s := gbnf.NewSampler(table(rules), 3, 4)
	tails := 0
	for i := 0; i < 200; i++ {
		lit := s.Sample("literal")
		reply := "add " + lit
		if action, arg, ok := readAction(reply); !g.Accepts(reply) || !ok || action != ActionAdd || arg != strings.TrimSpace(lit) {
			t.Fatalf("%q: accepted %v, read as %q %q", reply, g.Accepts(reply), action, arg)
		}
		if plain.Accepts(reply) {
			t.Fatalf("control: without Build the grammar accepts %q", reply)
		}
		goal := s.Sample("goal")
		_, tail, ok := strings.Cut(goal, " => ")
		if !ok {
			continue
		}
		tails++
		tail = "=> " + tail
		if !g.Accepts(tail) || plain.Accepts(tail) {
			t.Fatalf("the tail %q: accepted %v under Build, %v without", tail, g.Accepts(tail), plain.Accepts(tail))
		}
		if _, err := datalog.Parse(withBody(Try{Text: tail}, []string{lit}).Text); err != nil {
			t.Fatalf("%q after %q doesn't parse: %v", tail, lit, err)
		}
	}
	if tails == 0 {
		t.Fatalf("control: no sampled goal had a tail")
	}
	if action, _, ok := readAction("drop"); !g.Accepts("drop") || !ok || action != ActionDrop || plain.Accepts("drop") {
		t.Errorf("drop: accepted %v (%v without Build), read as %q", g.Accepts("drop"), plain.Accepts("drop"), action)
	}
	for _, reply := range []string{"add ", "drop it", "add pkg(?p, ?l)\npkg(?q, ?l)"} {
		if g.Accepts(reply) {
			t.Errorf("the grammar accepts %q", reply)
		}
	}
}

// With a Shape the actions' grammar takes paths with two ends, each a kind and any literals the draft
// grammar writes, and each reads as paths with its ends. control: without paths it refuses them.
func TestEveryPathsReplyReadsAsItsAction(t *testing.T) {
	grammar, err := GrammarFor(grammarVocabulary(t))
	if err != nil {
		t.Fatal(err)
	}
	g := mustGBNF(t, actionGrammar(grammar, false, false, true))
	plain := mustGBNF(t, actionGrammar(grammar, false, false, false))
	rules, err := vocabRules(grammarVocabulary(t), grammarOpts{})
	if err != nil {
		t.Fatal(err)
	}
	s := gbnf.NewSampler(table(rules), 5, 4)
	for i := 0; i < 200; i++ {
		ends := "func " + s.Sample("literal") + " => callsite"
		if i%2 == 0 {
			ends = "func => test_case " + s.Sample("literal") + ", " + s.Sample("literal")
		}
		reply := "paths " + ends
		if action, arg, ok := readAction(reply); !g.Accepts(reply) || !ok || action != ActionPaths || arg != ends {
			t.Fatalf("%q: accepted %v, read as %q %q", reply, g.Accepts(reply), action, arg)
		}
		if plain.Accepts(reply) {
			t.Fatalf("control: without paths the grammar accepts %q", reply)
		}
	}
	for _, reply := range []string{"paths func", "paths => func", "paths func =>"} {
		if g.Accepts(reply) {
			t.Errorf("the grammar accepts %q", reply)
		}
	}
}
