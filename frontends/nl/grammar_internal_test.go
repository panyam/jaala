package nl

import (
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/internal/gbnf"
)

// Every program the grammar can write parses, which is what decoding under it promises a host.
func TestEveryProgramTheGrammarWritesParses(t *testing.T) {
	var s *gbnf.Sampler
	failed, rules := 0, programRules()
	for i := 0; i < 21000; i++ {
		if i%300 == 0 { // 70 seeds over every depth: one seed alone missed -(9) as a column
			s = gbnf.NewSampler(table(rules), int64(i), 3+i/300%7)
		}
		text := s.Sample("root")
		if _, err := datalog.Parse(text); err != nil {
			failed++
			t.Errorf("%q: %v", text, err)
			if failed >= 15 {
				t.Fatal("stopping after 15")
			}
		}
	}
}

// A name stops at nameMax characters, so a model writing prose under the grammar can't run one name
// to its token limit (#340), in the plain grammar and in a vocabulary's, a program's own rule names
// included. control: the parser takes the longer names.
func TestTheGrammarCapsAName(t *testing.T) {
	plain, err := gbnf.Parse(Grammar())
	if err != nil {
		t.Fatal(err)
	}
	vocab, err := GrammarFor(grammarVocabulary(t))
	if err != nil {
		t.Fatal(err)
	}
	g, err := gbnf.Parse(vocab)
	if err != nil {
		t.Fatal(err)
	}
	at, past := strings.Repeat("x", nameMax), strings.Repeat("x", nameMax+1)
	for _, c := range []struct {
		form    string
		inVocab bool
	}{
		{`edge(?%s, ?b) => ?b`, true},
		{`edge(?a, ?b) => ?a, count(?%s)`, true},
		{`%s(?a) :- edge(?a, ?a); %s(?a) => ?a`, true},
		{`edge(?a, ?b), weight(?b, 3%s) => ?a`, false},
		{`edge(?a, ?b), ?b > 3%s => ?a`, true},
	} {
		ok, long := strings.ReplaceAll(c.form, "%s", at), strings.ReplaceAll(c.form, "%s", past)
		if !plain.Accepts(ok) || plain.Accepts(long) {
			t.Errorf("Grammar() on %q: accepts %d characters %v, %d %v", c.form, nameMax, plain.Accepts(ok), nameMax+1, plain.Accepts(long))
		}
		if c.inVocab && (!g.Accepts(ok) || g.Accepts(long)) {
			t.Errorf("GrammarFor on %q: accepts %d characters %v, %d %v", c.form, nameMax, g.Accepts(ok), nameMax+1, g.Accepts(long))
		}
		if _, err := datalog.Parse(long); err != nil {
			t.Errorf("control: %q doesn't parse: %v", long, err)
		}
	}
}
