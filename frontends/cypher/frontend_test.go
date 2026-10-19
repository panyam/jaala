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
		"MATCH (p:Package)\nUNWIND [1, 2] AS x RETURN p.name, x",
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
	for _, want := range []string{"jaala#283", "line 2, column 1:\n  UNWIND", "  ^\n", "Write the query without it"} {
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

// looker answers right only once a result in its conversation shows "MIT", and asks for the values
// first when the prompt says it may.
type looker struct{ ask, right, wrong string }

func (looker) Name() string { return "small" }

func (m looker) Complete(_ context.Context, p nl.Prompt) (nl.Completion, error) {
	for _, t := range p.Turns[1:] {
		if t.Role == nl.RoleUser && strings.Contains(t.Text, `"MIT"`) {
			return nl.Completion{Text: m.right}, nil
		}
	}
	if strings.Contains(p.Prefix, "values TARGET") && len(p.Turns) == 1 {
		return nl.Completion{Text: m.ask}, nil
	}
	return nl.Completion{Text: m.wrong}, nil
}

// The action loop is the same in Cypher (#393): values names a label's property and runs as a Cypher
// query, the grammar wraps Cypher's, and look describes a label. control: the same model without
// Actions guesses, and its guess answers nothing.
func TestActionsSpeakCypher(t *testing.T) {
	v, b := guide.Deps(t)
	m := looker{ask: "values Package.license", right: `MATCH (p:Package {license: 'MIT'}) RETURN p.name`, wrong: `MATCH (p:Package {license: 'mit'}) RETURN p.name`}
	fe := cypher.NewFrontEnd(depsGraph)
	p, tr, err := nl.Translate(context.Background(), "which packages are MIT licensed?", v, nil, m, nl.Options{Base: b, FrontEnd: fe, Actions: &nl.Actions{}})
	if err != nil || p.Text != m.right || len(p.Rows) != 1 || len(tr.Tries) != 1 || len(tr.Steps) != 1 {
		t.Fatalf("Translate = %+v, %v, steps %+v", p, err, tr.Steps)
	}
	if res := tr.Steps[0].Result; !strings.HasPrefix(res, "values of Package.license answered") || !strings.Contains(res, `"Apache-2.0"`) {
		t.Errorf("values sent back %q", res)
	}
	_, tr, err = nl.Translate(context.Background(), "which packages are MIT licensed?", v, nil, m, nl.Options{Base: b, FrontEnd: fe})
	if err == nil || len(tr.Steps) != 0 || tr.Tries[0].Kind != nl.KindEmpty {
		t.Errorf("control: without Actions, %v, %+v", err, tr.Tries)
	}

	r := &replies{texts: []string{"look Package", "look Nope", "values Package.licence", "try\nMATCH (p:Package {license: 'GPL'}) RETURN p.name", m.right}}
	if _, tr, err = nl.Translate(context.Background(), "which packages are MIT licensed?", v, nil, r, nl.Options{Base: b, FrontEnd: fe, Actions: &nl.Actions{}}); err != nil || len(tr.Steps) != 4 {
		t.Fatalf("%v, steps %+v", err, tr.Steps)
	}
	for i, want := range []string{"(:Package {license, name})", "the graph's labels are Package", `did you mean "license"?`, "line 1, column 7:\n  MATCH (p:Package {license: 'GPL'})"} {
		if res := tr.Steps[i].Result; !strings.Contains(res, want) {
			t.Errorf("step %d (%s) sent back %q, want %q in it", i+1, tr.Steps[i].Text, res, want)
		}
	}
	if prefix := r.asked[0].Prefix; !strings.Contains(prefix, "such as Label.property") {
		t.Errorf("the instructions don't write a target in Cypher's terms:\n%s", prefix)
	}
	if g := r.asked[0].Grammar; !strings.HasPrefix(g, `root ::= "look " target`) || !strings.Contains(g, `"IMPORTS"`) {
		t.Errorf("the grammar doesn't wrap Cypher's:\n%.300s", g)
	}
}
