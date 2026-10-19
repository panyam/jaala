package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/frontends/mcp"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
	"github.com/panyam/mcpkit/testutil"
)

// code is Declaire's store shape: node(id, kind), edge(from, kind, to) and attr(id, key, value).
// main calls parse and help, help calls parse, parse calls lex, and a.go contains main.
func code(t *testing.T) (*ns.Vocabulary, *datalog.Base) {
	t.Helper()
	src := ns.NewMemSource().
		DeclareSchema("node", ns.Schema{Arity: 2, Labels: []string{"id", "kind"}, Types: []ns.ArgType{{}, {Domain: []string{"func", "file"}}}}).
		DeclareSchema("edge", ns.Schema{Arity: 3, Labels: []string{"from", "kind", "to"}}).
		DeclareSchema("attr", ns.Schema{Arity: 3, Labels: []string{"id", "key", "value"}})
	add := func(rel string, vals ...string) {
		tu := ns.Tuple{}
		for _, v := range vals {
			tu.Vals = append(tu.Vals, ns.S(v))
		}
		src.Add(rel, tu)
	}
	for _, f := range []string{"f:main", "f:parse", "f:lex", "f:help"} {
		add("node", f, "func")
		add("attr", f, "name", f[2:])
	}
	add("node", "file:a.go", "file")
	for _, e := range [][3]string{{"f:main", "calls", "f:parse"}, {"f:main", "calls", "f:help"}, {"f:help", "calls", "f:parse"},
		{"f:parse", "calls", "f:lex"}, {"file:a.go", "contains", "f:main"}} {
		add("edge", e[0], e[1], e[2])
	}
	v, err := ns.NewVocabulary(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := stdlib.Register(v); err != nil {
		t.Fatal(err)
	}
	b, err := datalog.NewBase(v, src)
	if err != nil {
		t.Fatal(err)
	}
	return v, b
}

// codeGraph is code's graph, one label per node kind and one type per edge kind.
func codeGraph(funcLabel string) *cypher.Graph {
	return &cypher.Graph{
		Nodes: []cypher.Node{
			{Label: funcLabel, Relation: "node", Key: 0, Fixed: map[int]string{1: "func"}},
			{Label: "File", Relation: "node", Key: 0, Fixed: map[int]string{1: "file"}},
		},
		Edges: []cypher.Edge{
			{Type: "CALLS", Relation: "edge", From: 0, To: 2, Fixed: map[int]string{1: "calls"}},
			{Type: "CONTAINS", Relation: "edge", From: 0, To: 2, Fixed: map[int]string{1: "contains"}},
		},
		Props: &cypher.PropRelation{Relation: "attr", Node: 0, Key: 1, Value: 2},
	}
}

func codePack(g *cypher.Graph) mcp.Pack {
	return mcp.Pack{Pack: nl.Pack{About: "The functions and files of a Go repo."}, Name: "code", Cypher: g}
}

func dialCode(t *testing.T, p mcp.Pack, o mcp.Options) *testutil.TestClient {
	t.Helper()
	v, b := code(t)
	o.Base = func(context.Context) (*datalog.Base, error) { return b, nil }
	srv, err := mcp.New(v, p, o)
	if err != nil {
		t.Fatal(err)
	}
	return testutil.NewTestClient(t, srv)
}

// The cypher tool, and every word about it, appears only for a pack that maps its graph. control: the
// same server without a graph says nothing of Cypher.
func TestCypherIsOfferedWhenThePackMapsAGraph(t *testing.T) {
	plain, withGraph := codePack(nil), codePack(codeGraph("Func"))
	c := dialCode(t, plain, mcp.Options{})
	if names := toolNames(c); strings.Contains(names, "cypher") {
		t.Errorf("control: a pack without a graph offers %s", names)
	}
	if res := call(t, c, "schema", map[string]any{"path": "cypher"}); !res.IsError {
		t.Errorf("control: schema answered a graph it doesn't have: %s", res.Content[0].Text)
	}
	if strings.Contains(strings.ToLower(mcp.Instructions(plain, false)+mcp.AgentLine(plain)), "cypher") {
		t.Errorf("control: the instructions mention Cypher with no graph")
	}

	c = dialCode(t, withGraph, mcp.Options{})
	if names := toolNames(c); !strings.Contains(names, "cypher") {
		t.Fatalf("tools %s, want cypher", names)
	}
	labels := "Node labels: Func, File. Relationship types: CALLS, CONTAINS. A node's other properties are any key its attr facts name."
	for _, tool := range c.ListTools() {
		if tool.Name == "cypher" && !strings.Contains(tool.Description, labels) {
			t.Errorf("the cypher tool's description doesn't name the graph: %s", tool.Description)
		}
	}
	if res := call(t, c, "schema", map[string]any{"path": "cypher"}); res.IsError || res.Content[0].Text != labels+"\n" {
		t.Errorf("schema at cypher = %q", res.Content[0].Text)
	}
	if in := mcp.Instructions(withGraph, false); !strings.Contains(in, "ask in Cypher with the cypher tool") || !strings.Contains(in, labels) {
		t.Errorf("instructions:\n%s", in)
	}
	if line := mcp.AgentLine(withGraph); !strings.HasSuffix(line, "then query, or cypher to ask in Cypher.") {
		t.Errorf("agent line: %s", line)
	}
}

// A property the host computes for every node is named beside the labels, so an agent asks for it.
func TestTheCypherToolNamesTheComputedProperties(t *testing.T) {
	g := codeGraph("Func")
	g.Derived = map[string]cypher.PropSource{"name": {Relation: "str.lower", Node: 0, Value: 1}}
	c := dialCode(t, codePack(g), mcp.Options{})
	want := "CONTAINS. Any node may have {name}, which the host computes. A node's other properties"
	if res := call(t, c, "schema", map[string]any{"path": "cypher"}); res.IsError || !strings.Contains(res.Content[0].Text, want) {
		t.Errorf("schema at cypher = %q, want it to contain %q", res.Content[0].Text, want)
	}
}

// The issue's acceptance case: a match over node, edge and attr answers each row with the facts it
// came from, the query's own column names and the program it ran, a page at a time.
func TestCypherAnswersWithWitnessesAndItsColumns(t *testing.T) {
	c := dialCode(t, codePack(codeGraph("Func")), mcp.Options{Limit: 2})
	q := `MATCH (a:Func)-[:CALLS]->(b:Func) RETURN a, b ORDER BY a, b`
	res := call(t, c, "cypher", map[string]any{"query": q})
	text := res.Content[0].Text
	for _, want := range []string{
		"ran: node(?a, \"func\"), node(?b, \"func\"), edge(?a, \"calls\", ?b) => ?a, ?b order by ?a, ?b\n",
		"columns: a, b\n1. f:help, f:parse\n",
		`   edge("f:help", "calls", "f:parse")`,
		`rows 1 to 2 of 4; continue with page_token "2"`,
	} {
		if res.IsError || !strings.Contains(text, want) {
			t.Fatalf("want %q in:\n%s", want, text)
		}
	}
	res = call(t, c, "cypher", map[string]any{"query": q, "page_token": "2", "json": true})
	data, _ := json.Marshal(res.StructuredContent)
	var got struct {
		Cypher, Program string
		Columns         []string
		Rows            [][]string
		Witness         [][]struct{}
	}
	if err := json.Unmarshal(data, &got); err != nil || got.Cypher != q || !strings.HasPrefix(got.Program, "node(?a,") ||
		strings.Join(got.Columns, ",") != "a,b" || len(got.Rows) != 2 || got.Rows[1][0] != "f:parse" || len(got.Witness) != 2 {
		t.Errorf("json %s (%v)", data, err)
	}
	named := call(t, c, "cypher", map[string]any{"query": `MATCH (f:Func {name: 'main'})-[:CALLS]->(g:Func) RETURN g.name AS callee ORDER BY callee`})
	if text := named.Content[0].Text; !strings.Contains(text, "columns: callee\n1. help\n") || !strings.Contains(text, "\n2. parse\n") {
		t.Errorf("an alias names its column:\n%s", named.Content[0].Text)
	}
}

func TestCypherBindsParameters(t *testing.T) {
	c := dialCode(t, codePack(codeGraph("Func")), mcp.Options{})
	q := `MATCH (a:Func {name: $who})-[:CALLS]->(b:Func) RETURN b.name ORDER BY b.name`
	res := call(t, c, "cypher", map[string]any{"query": q, "params": map[string]any{"who": "main"}})
	if text := res.Content[0].Text; res.IsError || !strings.Contains(text, "columns: b.name\n1. help\n") || !strings.Contains(text, "\n2. parse\n") || !strings.Contains(text, `attr(?a, "name", ?who)`) {
		t.Errorf("$who = main:\n%s", res.Content[0].Text)
	}
	for name, params := range map[string]map[string]any{
		"not a string or number": {"who": true},
		"not in the query":       {"whom": "main"},
	} {
		if res := call(t, c, "cypher", map[string]any{"query": q, "params": params}); !res.IsError {
			t.Errorf("%s: accepted, answering %s", name, res.Content[0].Text)
		}
	}
}

// Cypher a program can't express yet comes back naming the issue that would let it, under the place
// in the query, so an agent rewrites rather than retries; a mistake against the graph has no issue to
// name, and a program that runs out of budget says so as query's would.
func TestCypherRefusesWithThePlaceAndTheGap(t *testing.T) {
	c := dialCode(t, codePack(codeGraph("Func")), mcp.Options{})
	res := call(t, c, "cypher", map[string]any{"query": "MATCH (a:Func)\nSET a.name = 'x' RETURN a"})
	text := res.Content[0].Text
	for _, want := range []string{"jaala#287", "line 2, column 1:\n  SET a.name = 'x' RETURN a\n  ^\n", "Write the query without it"} {
		if !res.IsError || !strings.Contains(text, want) {
			t.Errorf("want %q in the refusal:\n%s", want, text)
		}
	}
	res = call(t, c, "cypher", map[string]any{"query": "MATCH (a:Method) RETURN a"})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "Method") || strings.Contains(res.Content[0].Text, "Write the query without it") {
		t.Errorf("an unknown label:\n%s", res.Content[0].Text)
	}
	res = call(t, c, "cypher", map[string]any{"query": "MATCH (a:Func)-[:CALLS*]->(b:Func) RETURN a, b", "max_work": 3})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "budget of 3") || !strings.Contains(res.Content[0].Text, "raise max_work") {
		t.Errorf("a stopped program:\n%s", res.Content[0].Text)
	}
}

// graphOf is a host argument choosing which graph a call reads, as Declaire builds one per view.
type graphOf struct {
	Graph string `json:"graph,omitempty" jsonschema_description:"Which graph to read."`
}

// A Run's pack brings the call's graph, or none; the server's pack decides only that the tool exists.
func TestACallReadsItsOwnGraph(t *testing.T) {
	v, b := code(t)
	srv, err := mcp.NewWith(v, codePack(codeGraph("Func")), mcp.Options{Base: func(context.Context) (*datalog.Base, error) { return b, nil }},
		mcp.Extension[graphOf]{Prepare: func(_ context.Context, _ string, a graphOf) (mcp.Run, error) {
			switch a.Graph {
			case "renamed":
				p := codePack(codeGraph("Function"))
				return mcp.Run{Pack: &p}, nil
			case "none":
				p := codePack(nil)
				return mcp.Run{Pack: &p}, nil
			}
			return mcp.Run{}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	c := testutil.NewTestClient(t, srv)
	q := func(label string) string { return "MATCH (a:" + label + " {name: 'lex'}) RETURN a" }
	if res := call(t, c, "cypher", map[string]any{"query": q("Func")}); res.IsError || !strings.Contains(res.Content[0].Text, "1. f:lex") {
		t.Errorf("control: the server's graph:\n%s", res.Content[0].Text)
	}
	if res := call(t, c, "cypher", map[string]any{"query": q("Function"), "graph": "renamed"}); res.IsError || !strings.Contains(res.Content[0].Text, "1. f:lex") {
		t.Errorf("the call's graph:\n%s", res.Content[0].Text)
	}
	if res := call(t, c, "cypher", map[string]any{"query": q("Func"), "graph": "renamed"}); !res.IsError {
		t.Errorf("the server's label answered over the call's graph:\n%s", res.Content[0].Text)
	}
	if res := call(t, c, "cypher", map[string]any{"query": q("Func"), "graph": "none"}); !res.IsError || !strings.Contains(res.Content[0].Text, "no Cypher graph") {
		t.Errorf("a call with no graph:\n%s", res.Content[0].Text)
	}
}

func TestAPackFormatsACypherAnswer(t *testing.T) {
	p := codePack(codeGraph("Func"))
	var seen mcp.Answer
	p.Format = func(a mcp.Answer) string { seen = a; return "formatted" }
	c := dialCode(t, p, mcp.Options{})
	res := call(t, c, "cypher", map[string]any{"query": "MATCH (a:Func {name: 'lex'}) RETURN a.name AS fn"})
	if res.Content[0].Text != "formatted" || !strings.HasPrefix(seen.Cypher, "MATCH") || strings.Join(seen.Columns, ",") != "fn" || seen.Table.Len() != 1 {
		t.Errorf("Format saw %+v and answered %q", seen, res.Content[0].Text)
	}
	call(t, c, "query", map[string]any{"program": `node(?n, "file") => ?n`})
	if seen.Cypher != "" || strings.Join(seen.Columns, ",") != "n" {
		t.Errorf("query's answer: Cypher %q, columns %v", seen.Cypher, seen.Columns)
	}
}

func TestNewRefusesAGraphTheVocabularyLacks(t *testing.T) {
	v, b := code(t)
	g := codeGraph("Func")
	g.Nodes[0].Relation = "function"
	if _, err := mcp.New(v, codePack(g), mcp.Options{Base: func(context.Context) (*datalog.Base, error) { return b, nil }}); err == nil || !strings.Contains(err.Error(), "function") {
		t.Errorf("New accepted a graph reading a relation the vocabulary lacks: %v", err)
	}
}

// A program that compiles but doesn't check comes back in the query's terms, at its place in the query
// the agent wrote, with the program it ran for reference.
func TestACypherCheckErrorPointsIntoTheQuery(t *testing.T) {
	g := codeGraph("Func")
	g.Nodes = append(g.Nodes, cypher.Node{Label: "Method", Relation: "node", Key: 0, Fixed: map[int]string{1: "method"}})
	c := dialCode(t, codePack(g), mcp.Options{})
	q := "MATCH (f:Func)-[:CALLS]->\n      (m:Method) RETURN m"
	res := call(t, c, "cypher", map[string]any{"query": q})
	text := res.Content[0].Text
	for _, want := range []string{
		`node's "kind" argument cannot be "method"`,
		"line 2, column 7:\n        (m:Method) RETURN m\n        ^\n",
		"fix: ",
		"ran: node(?f, \"func\")",
	} {
		if !res.IsError || !strings.Contains(text, want) {
			t.Errorf("want %q in:\n%s", want, text)
		}
	}
}

// prompted replies with one text, recording each prompt it was sent.
type prompted struct {
	reply string
	asked []nl.Prompt
}

func (m *prompted) Name() string { return "small" }

func (m *prompted) Complete(_ context.Context, p nl.Prompt) (nl.Completion, error) {
	m.asked = append(m.asked, p)
	return nl.Completion{Text: m.reply}, nil
}

// ask translates into Cypher when the call or the pack says so (#386): the model sees the graph and
// the pack's Cypher examples, and the answer is the cypher tool's, the query, the program it ran and
// Cypher's column names. control: the same reply asked in datalog doesn't parse.
func TestAskTranslatesIntoCypher(t *testing.T) {
	p := codePack(codeGraph("Func"))
	p.AskLanguage = "cypher"
	p.CypherExamples = []nl.Example{{Question: "what does lex call?", Program: `MATCH (:Func {name: 'lex'})-[:CALLS]->(b:Func) RETURN b`}}
	m := &prompted{reply: `MATCH (a:Func {name: 'main'})-[:CALLS]->(b:Func) RETURN b.name AS callee ORDER BY callee`}
	c := dialCode(t, p, mcp.Options{Model: m})
	text := c.ToolCall("ask", map[string]any{"question": "what does main call?"})
	for _, want := range []string{"cypher:\n  MATCH (a:Func", "ran: ", "columns: callee\n", "1. help\n", "2. parse\n", "1. small: accepted"} {
		if !strings.Contains(text, want) {
			t.Errorf("ask lacks %q:\n%s", want, text)
		}
	}
	if prefix := m.asked[0].Prefix; !strings.Contains(prefix, "(:Func") || !strings.Contains(prefix, "what does lex call?") {
		t.Errorf("the model wasn't shown the graph and the Cypher example:\n%s", prefix)
	}
	res := call(t, c, "ask", map[string]any{"question": "what does main call?", "language": "datalog"})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "small: parse") {
		t.Errorf("control: asked in datalog, %+v", res.Content)
	}
	if !strings.Contains(mcp.Instructions(p, true), "With language cypher") {
		t.Errorf("the instructions don't say ask writes Cypher")
	}

	plain := codePack(nil)
	c = dialCode(t, plain, mcp.Options{Model: m})
	if res := call(t, c, "ask", map[string]any{"question": "what does main call?", "language": "cypher"}); !res.IsError || !strings.Contains(res.Content[0].Text, "no Cypher graph") {
		t.Errorf("a pack without a graph translated into Cypher: %+v", res.Content)
	}
	v, b := code(t)
	base := func(context.Context) (*datalog.Base, error) { return b, nil }
	for _, broken := range []mcp.Pack{
		{AskLanguage: "cypher"},
		{AskLanguage: "gql", Cypher: codeGraph("Func")},
		{Cypher: codeGraph("Func"), CypherExamples: []nl.Example{{Question: "q", Program: `MATCH (a:Nope) RETURN a`}}},
	} {
		if _, err := mcp.New(v, broken, mcp.Options{Base: base}); err == nil {
			t.Errorf("New took a pack asking in %q with examples %v", broken.AskLanguage, broken.CypherExamples)
		}
	}
}

// A pack's Hint follows an empty Cypher answer too, under the culprit placed in the query.
func TestAnEmptyCypherAnswerCarriesThePacksHint(t *testing.T) {
	p := codePack(codeGraph("Func"))
	p.Hint = func(_ context.Context, why *datalog.EmptyReport) string {
		if why.Relation != "attr" || why.Args[2] == nil {
			return ""
		}
		return fmt.Sprintf("hint: no node has %s %q", why.Args[1].S, why.Args[2].S)
	}
	c := dialCode(t, p, mcp.Options{})
	text := c.ToolCall("cypher", map[string]any{"query": `MATCH (a:Func)-[:CALLS]->(b:Func {id: 'f:nope'}) RETURN a`})
	if !strings.Contains(text, "no rows") || !strings.Contains(text, `hint: no node has id "f:nope"`) {
		t.Errorf("cypher:\n%s", text)
	}
}
