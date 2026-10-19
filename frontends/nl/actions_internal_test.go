package nl

import (
	"fmt"
	"strings"
	"testing"

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
	g := mustGBNF(t, actionGrammar(grammar, find))
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
