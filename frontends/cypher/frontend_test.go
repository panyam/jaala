package cypher_test

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

// The card names the graph in Cypher's terms, one label or type a line, so the model reads every name
// it may use.
func TestTheFrontEndsCardIsTheGraph(t *testing.T) {
	v, _ := guide.Deps(t)
	card, err := cypher.NewFrontEnd(depsGraph).Card(v)
	if err != nil {
		t.Fatal(err)
	}
	want := `# The graph a Cypher query reads. Node labels, with their properties:
#   (:Package {license, name})
# Relationship types:
#   -[:IMPORTS]->
#   -[:DEPENDS_ON]->
`
	if card != want {
		t.Errorf("card:\n%s\nwant:\n%s", card, want)
	}
}

// replies answers in turn, recording what it was asked.
type replies struct {
	texts []string
	asked []nl.Prompt
}

func (r *replies) Name() string { return "small" }

func (r *replies) Complete(_ context.Context, p nl.Prompt) (nl.Completion, error) {
	r.asked = append(r.asked, p)
	return nl.Completion{Text: r.texts[min(len(r.asked)-1, len(r.texts)-1)]}, nil
}

// A draft using Cypher a program can't express yet is refused as a gap and goes back in Cypher's terms,
// at its place in the draft; the model then writes it another way. The prompt carries the graph's
// grammar.
func TestTranslateRefusesAGapInCypher(t *testing.T) {
	v, b := guide.Deps(t)
	m := &replies{texts: []string{
		"MATCH (p:Package)\nOPTIONAL MATCH (p)-[:IMPORTS]->(q) RETURN p.name, q.name",
		`MATCH (p:Package)-[:IMPORTS]->(q) RETURN p.name, q.name`,
	}}
	p, tr, err := nl.Translate(context.Background(), "what imports what?", v, nil, m, nl.Options{Base: b, FrontEnd: cypher.NewFrontEnd(depsGraph)})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Tries[0].Kind != cypher.KindGap || len(p.Rows) != 3 || p.Language != "cypher" {
		t.Errorf("tries %v, program %+v", tr.Tries, p)
	}
	fed := m.asked[1].Turns[2].Text
	for _, want := range []string{"jaala#282", "line 2, column 1:\n  OPTIONAL MATCH", "  ^\n", "Write the query without it"} {
		if !strings.Contains(fed, want) {
			t.Errorf("fed back %q, want %q in it", fed, want)
		}
	}
	if g := m.asked[0].Grammar; !strings.HasPrefix(g, "root ::= ") || !strings.Contains(g, `"IMPORTS"`) {
		t.Errorf("grammar sent:\n%s", g)
	}
}

// An example that doesn't compile fails Translate before the model is asked.
func TestAFrontEndsExampleMustCompile(t *testing.T) {
	v, _ := guide.Deps(t)
	m := &replies{texts: []string{`MATCH (p:Package) RETURN p`}}
	fe := cypher.NewFrontEnd(depsGraph, nl.Example{Question: "who?", Program: `MATCH (p:Pakage) RETURN p`})
	if _, _, err := nl.Translate(context.Background(), "q", v, nil, m, nl.Options{FrontEnd: fe}); err == nil || !strings.Contains(err.Error(), `did you mean "Package"?`) {
		t.Errorf("err %v, want the example refused with its fix", err)
	}
	if len(m.asked) != 0 {
		t.Errorf("asked %d times", len(m.asked))
	}
}

// A draft that compiles and answers nothing goes back at the Cypher that matched nothing (#386):
// the pattern under a caret and the program's variables in the query's names, not the compiled
// literal alone, which names nothing the model wrote.
func TestAnEmptyAnswerIsExplainedAtItsCypher(t *testing.T) {
	v, b := guide.Deps(t)
	m := &replies{texts: []string{
		"MATCH (p:Package)-[:IMPORTS]->(q:Package)\nWHERE p.name = 'app'\nMATCH (q)-[:IMPORTS]->(r:Package {license: 'GPL'}) RETURN r.name",
		`MATCH (p:Package {name: 'app'})-[:IMPORTS]->(q) RETURN q.name`,
	}}
	p, tr, err := nl.Translate(context.Background(), "what does app import?", v, nil, m, nl.Options{Base: b, FrontEnd: cypher.NewFrontEnd(depsGraph)})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Tries[0].Kind != nl.KindEmpty || len(p.Rows) != 1 {
		t.Fatalf("tries %v, program %+v", tr.Tries, p)
	}
	fed := m.asked[1].Turns[2].Text
	for _, want := range []string{"answered no rows", "line 3, column 23:\n  MATCH (q)-[:IMPORTS]->(r:Package {license: 'GPL'})", "\n                        ^\n"} {
		if !strings.Contains(fed, want) {
			t.Errorf("fed back %q, want %q in it", fed, want)
		}
	}
	if strings.Contains(fed, "?r") {
		t.Errorf("fed back %q, which names the program's variables", fed)
	}
	// an EXISTS that matched nothing ends its trail in the rule cypher_exists wrote, whose literal
	// the goal's report only reaches through Because
	m = &replies{texts: []string{
		"MATCH (p:Package) WHERE EXISTS { (p)-[:IMPORTS]->(:Package {license: 'GPL'}) } RETURN p.name",
		`MATCH (p:Package) RETURN p.name`,
	}}
	if _, _, err := nl.Translate(context.Background(), "what imports a GPL package?", v, nil, m, nl.Options{Base: b, FrontEnd: cypher.NewFrontEnd(depsGraph)}); err != nil {
		t.Fatal(err)
	}
	if fed := m.asked[1].Turns[2].Text; !strings.Contains(fed, "because") || !strings.Contains(fed, "line 1, column 25:") {
		t.Errorf("fed back %q, want the trail through the rule, at the EXISTS", fed)
	}
}
