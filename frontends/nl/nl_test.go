package nl_test

import (
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// vocabulary is a small graph with docs and types, the standard vocabulary, and two modules: one
// whose relation infers a type and takes an input, and one nested under it.
func vocabulary(t *testing.T) *ns.Vocabulary {
	t.Helper()
	node := ns.ArgType{Kind: "node"}
	src := ns.NewMemSource().
		DeclareSchema("edge", ns.Schema{Arity: 2, Labels: []string{"from", "to"}, Types: []ns.ArgType{node, node},
			Doc: "A directed edge between two nodes.\nThis second line stays out of the card."}).
		DeclareSchema("node", ns.Schema{Arity: 1, Labels: []string{"name"}, Types: []ns.ArgType{node}, Doc: "Every node."}).
		DeclareSchema("weight", ns.Schema{Arity: 2, Labels: []string{"node", "w"}, Types: []ns.ArgType{node, {Type: ns.TypeNumber}}})
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
	for _, m := range [][2]string{
		{"graph", `# reach is every node a node gets to by edges.
reach(?a, ?b) :- edge(?a, ?b);
reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c);
# heavy is a node weighing more than min.
heavy(?n, ?w, ?min) :- weight(?n, ?w), ?w > ?min;
`},
		{"graph.loops", `self(?n: node) :- edge(?n, ?n);`},
	} {
		if err := v.AddModule(m[0], datalog.LanguageName, m[1], ""); err != nil {
			t.Fatal(err)
		}
	}
	return v
}

// The card is the vocabulary as a model reads it: every member, a line each, the same text every time.
func TestTheCardListsTheVocabulary(t *testing.T) {
	card, err := nl.Card(vocabulary(t))
	if err != nil {
		t.Fatal(err)
	}
	const golden = "testdata/card.golden"
	if *update {
		if err := os.WriteFile(golden, []byte(card), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if card != string(want) {
		t.Errorf("the card changed; if on purpose, rerun with -update\ngot:\n%s", card)
	}
	for _, line := range []string{
		"edge(from: node, to: node) # A directed edge between two nodes.\n",
		"graph.reach(a: ~node, b: ~node) # reach is every node a node gets to by edges.\n",
		"graph.heavy(n: ~node, w: ~number, in min) # heavy is a node weighing more than min.\n",
		"\nmodule graph.loops\ngraph.loops.self(n: node)\n",
		"str.prefix(string: string, prefix: string) filter",
	} {
		if !strings.Contains(card, line) {
			t.Errorf("the card has no %q", line)
		}
	}
	if strings.Contains(card, "second line") || strings.ContainsRune(card, 0) {
		t.Error("the card holds a doc's second line or a rewrite's name")
	}
}

func TestTheCardIsTheSameEveryTime(t *testing.T) {
	v := vocabulary(t)
	first, err := nl.Card(v)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		for _, w := range []*ns.Vocabulary{v, v.Clone(), vocabulary(t)} {
			if again, err := nl.Card(w); err != nil || again != first {
				t.Fatalf("a card of the same vocabulary differs (%v)", err)
			}
		}
	}
}

func TestTheCardReportsAModuleThatDoesntCheck(t *testing.T) {
	v := vocabulary(t)
	if err := v.AddModule("broken", datalog.LanguageName, `r(?a) :- nothing(?a);`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := nl.Card(v); err == nil || !strings.Contains(err.Error(), `unknown relation "nothing"`) {
		t.Fatalf("Card = %v, want the module's error", err)
	}
}

// Explain gives a model what it needs to fix a program: the message, the place and the fix.
func TestExplainPointsAtTheMistakeAndSaysHowToFixIt(t *testing.T) {
	v := vocabulary(t)
	for _, c := range []struct{ name, text, want string }{
		{"arity", "far(?a, ?b) :- graph.reach(?a, ?b);\nfar(?a, ?b), edge(?b) => ?a",
			`query: relation "edge" takes 2 args, got 1
line 2, column 14:
  far(?a, ?b), edge(?b) => ?a
               ^
fix: edge(from: node, to: node)`},
		{"a member without its module", `reach(?a, ?b) => ?a`,
			`query: unknown relation "reach"; did you mean "graph.reach"?
line 1, column 1:
  reach(?a, ?b) => ?a
  ^
fix: did you mean "graph.reach"?`},
		{"an unbound comparison", "node(?n),\n  ?w > 3 => ?n",
			`query: comparison operand is unbound (a variable must appear in a relation before it is compared)
line 2, column 3:
    ?w > 3 => ?n
    ^
fix: add a relation that binds ?w to the same body`},
		{"a parse error", "node(?n),\n  edge(?n ?m) => ?n",
			`query: variable ?n ?m: a variable's name is letters, digits and _
line 2, column 8:
    edge(?n ?m) => ?n
         ^`},
		{"no place to point at", "p(?a) :- node(?a), not q(?a);\nq(?a) :- node(?a), not p(?a);\np(?a) => ?a",
			`query: rules are not stratifiable (recursion through negation: p, q)`},
	} {
		t.Run(c.name, func(t *testing.T) {
			q, err := datalog.Parse(c.text)
			if err == nil {
				err = datalog.Validate(q, v)
			}
			if got := nl.Explain(c.text, err); got != c.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, c.want)
			}
		})
	}
	if nl.Explain("node(?n) => ?n", nil) != "" {
		t.Error("Explain(nil) isn't empty")
	}
}

// The grammar is also a file, so a host or a reviewer can read it, and datalog's tests check it
// against the programs only they can reach (TestTheGrammarAcceptsEveryProgramAsJaalaSpellsIt).
func TestTheGrammarFileIsTheGrammar(t *testing.T) {
	const golden = "testdata/program.gbnf"
	g := nl.Grammar()
	if *update {
		if err := os.WriteFile(golden, []byte(g), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if g != string(want) {
		t.Errorf("the grammar changed; if on purpose, rerun with -update\ngot:\n%s", g)
	}
	if !strings.HasPrefix(g, "root ::= ") {
		t.Errorf("the grammar doesn't start at root:\n%s", g[:min(len(g), 200)])
	}
}
