package cypher

import (
	"errors"
	"testing"

	"github.com/panyam/jaala/internal/gbnf"
	"github.com/panyam/jaala/internal/guide"
)

// Every query the grammar can write parses, compiling it never fails but with a *Error, and the
// grammar llama.cpp is sent accepts what the table draws (#354). Few random samples compile, since
// sampling takes the shortest way out of every rule (empty nodes, variables nothing matched): what a
// query means is Compile's to check, as a program's is Validate's.
func TestEveryQueryTheGrammarWritesParses(t *testing.T) {
	v, _ := guide.Deps(t)
	graphs := map[string]Graph{
		"columns": {
			Nodes: []Node{{Label: "Package", Relation: "pkg", Key: 0, Props: map[string]int{"name": 0, "license": 1}}},
			Edges: []Edge{{Type: "IMPORTS", Relation: "imports", From: 0, To: 1}, {Type: "DEPENDS_ON", Relation: "deps.depends_on", From: 0, To: 1}},
		},
		"no properties": {
			Nodes: []Node{{Label: "Package", Relation: "pkg", Key: 0}},
			Edges: []Edge{{Type: "IMPORTS", Relation: "imports", From: 0, To: 1}},
		},
	}
	for name, g := range graphs {
		t.Run(name, func(t *testing.T) {
			rules := grammarRules(g)
			recognizer, err := gbnf.Parse(gbnf.Render(rules))
			if err != nil {
				t.Fatal(err)
			}
			var s *gbnf.Sampler
			failed, n := 0, 7000
			for i := 0; i < n; i++ {
				if i%100 == 0 { // 70 seeds over every depth
					s = gbnf.NewSampler(rules, int64(i), 3+i/100%7)
				}
				text := s.Sample("root")
				fail := func(format string, args ...any) {
					failed++
					t.Errorf("%q: "+format, append([]any{text}, args...)...)
					if failed >= 15 {
						t.Fatal("stopping after 15")
					}
				}
				if _, err := parse(text); err != nil {
					fail("doesn't parse: %v", err)
					continue
				}
				if i%50 == 0 && !recognizer.Accepts(text) {
					fail("the rendered grammar refuses what its table drew")
				}
				var ce *Error
				if _, err := Compile(v, g, text); err != nil && !errors.As(err, &ce) {
					fail("refused with %T %v, not a *cypher.Error", err, err)
				}
			}
		})
	}
}

// The queries the guide's questions are answered with (the exercise's drafts, and the docs') are in
// the grammar, so a model decoding under it can write them. control: names the graph doesn't have
// aren't.
func TestTheGrammarHoldsTheGuidesAnswers(t *testing.T) {
	g := Graph{
		Nodes: []Node{{Label: "Package", Relation: "pkg", Key: 0, Props: map[string]int{"name": 0, "license": 1}}},
		Edges: []Edge{{Type: "IMPORTS", Relation: "imports", From: 0, To: 1}, {Type: "DEPENDS_ON", Relation: "deps.depends_on", From: 0, To: 1}},
	}
	recognizer, err := gbnf.Parse(Grammar(g))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`MATCH (:Package {name: 'app'})-[:IMPORTS]->(d) RETURN d.name`,
		`MATCH (:Package {name: 'app'})-[:IMPORTS*]->(d) RETURN DISTINCT d.name`,
		`MATCH (p:Package {name: 'util'}) RETURN p.license`,
		`MATCH (p:Package) WHERE p.license IS NULL RETURN p.name`,
		`MATCH (:Package {name: 'app'})-[:IMPORTS*]->(d) RETURN count(DISTINCT d)`,
		`MATCH (x:Package)-[:IMPORTS]->(:Package {name: 'util'}) RETURN x.name`,
		`MATCH (p:Package) RETURN p.name, p.license`,
		`MATCH (a:Package {name: 'app'})-[:IMPORTS*]->(b:Package) RETURN b.name AS dependency, b.license AS license ORDER BY dependency`,
		`MATCH (p:Package)-[:IMPORTS]->(q:Package) WITH p, count(q) AS n WHERE n > 1 RETURN p.name, n`,
		`MATCH (p:Package) WHERE NOT EXISTS { MATCH (p)-[:IMPORTS]->() } RETURN p.name`,
		`MATCH (p:Package) WHERE p.name = 'a' XOR p.license IS NULL OR p.name = 'b' RETURN p`, // XOR binds tighter than OR (#377)
	} {
		if !recognizer.Accepts(q) {
			t.Errorf("the grammar refuses %q", q)
		}
	}
	for _, q := range []string{
		`MATCH (p:Pkg) RETURN p`,                // a label the graph doesn't have
		`MATCH (p)-[:CALLS]->(q) RETURN p`,      // a type it doesn't have
		`MATCH (p:Package) RETURN p.maintainer`, // a property it doesn't have
	} {
		if recognizer.Accepts(q) {
			t.Errorf("control: the grammar accepts %q", q)
		}
	}
}
