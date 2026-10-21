package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/mcp"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
	"github.com/panyam/mcpkit/testutil"
)

// A value in bind reaches the program as a binding, named with or without its ?, and a list ranges
// the variable over its values. control: the same program unbound answers every road.
func TestABoundValueReachesTheProgramAsABinding(t *testing.T) {
	c := dial(t, pack, mcp.Options{})
	const program = `road(?from, ?to) => ?to`
	if got := c.ToolCall("query", map[string]any{"program": program}); !strings.Contains(got, "4 rows") {
		t.Fatalf("control: unbound, %s", got)
	}
	for _, name := range []string{"from", "?from"} {
		got := c.ToolCall("query", map[string]any{"program": program, "bind": map[string]any{name: "a"}})
		if !strings.Contains(got, "1. b\n") || !strings.Contains(got, "1 row\n") {
			t.Errorf("bind %s=a:\n%s", name, got)
		}
	}
	got := c.ToolCall("query", map[string]any{"program": program, "bind": map[string]any{"from": []any{"a", "c"}}})
	if !strings.Contains(got, "b\n") || !strings.Contains(got, "d\n") || !strings.Contains(got, "2 rows\n") {
		t.Errorf("bind from=[a c]:\n%s", got)
	}
	if got := c.ToolCall("query", map[string]any{"program": `weight(?t, ?w) => ?t`, "bind": map[string]any{"w": 3}}); !strings.Contains(got, "1. a\n") {
		t.Errorf("a number bound to a number argument:\n%s", got)
	}
	if got := c.ToolCall("explain", map[string]any{"program": program, "bind": map[string]any{"from": "a"}}); !strings.HasPrefix(got, "1 row\n") {
		t.Errorf("explain with bind from=a:\n%s", got)
	}
}

// A bind the engine can't take is a tool error saying which variable: a value that is neither text
// nor a number, and a variable the program doesn't have.
func TestABindThatCantBeTakenSaysWhy(t *testing.T) {
	c := dial(t, pack, mcp.Options{})
	const program = `road(?from, ?to) => ?to`
	res := call(t, c, "query", map[string]any{"program": program, "bind": map[string]any{"from": map[string]any{"town": "a"}}})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "bind: ?from is") {
		t.Errorf("an object bound: %v %s", res.IsError, res.Content[0].Text)
	}
	res = call(t, c, "query", map[string]any{"program": program, "bind": map[string]any{"nope": "a"}})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "cannot bind ?nope: the goal does not use it") {
		t.Errorf("a variable the program lacks: %v %s", res.IsError, res.Content[0].Text)
	}
	res = call(t, c, "explain", map[string]any{"program": program, "bind": map[string]any{"from": true}})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "bind: ?from is true") {
		t.Errorf("explain with a boolean bound: %v %s", res.IsError, res.Content[0].Text)
	}
}

// A pack's Data is the answer's structured content, with or without json, beside Format's text;
// with json its JSON is the text. control: without Data an answer carries structured content only
// when json asks.
func TestAPacksDataIsTheAnswersStructuredContent(t *testing.T) {
	type rows struct {
		Program string `json:"program"`
		Rows    int    `json:"rows"`
		Link    string `json:"link"`
	}
	p := pack
	p.Format = func(a mcp.Answer) string { return "see the link" }
	p.Data = func(a mcp.Answer) (any, error) {
		return rows{Program: a.Program, Rows: a.Table.Len(), Link: "https://example.test/view"}, nil
	}
	c := dial(t, p, mcp.Options{})
	const program = `road(?from, ?to) => ?to`
	res := call(t, c, "query", map[string]any{"program": program})
	var got rows
	if err := remarshal(res.StructuredContent, &got); err != nil || got.Rows != 4 || got.Program != program || got.Link == "" {
		t.Errorf("structured content %#v (%v)", res.StructuredContent, err)
	}
	if res.IsError || res.Content[0].Text != "see the link" {
		t.Errorf("text %q, want Format's", res.Content[0].Text)
	}
	res = call(t, c, "query", map[string]any{"program": program, "json": true})
	if err := json.Unmarshal([]byte(res.Content[0].Text), &got); err != nil || got.Rows != 4 {
		t.Errorf("with json, the text is the data's JSON: %q", res.Content[0].Text)
	}

	plain := dial(t, pack, mcp.Options{})
	if res := call(t, plain, "query", map[string]any{"program": program}); res.StructuredContent != nil {
		t.Errorf("control: without Data or json, structured content %#v", res.StructuredContent)
	}
	if res := call(t, plain, "query", map[string]any{"program": program, "json": true}); res.StructuredContent == nil {
		t.Errorf("control: with json and no Data, no structured content")
	}
}

// A Data that fails, or gives something other than an object, is the call's tool error.
func TestAPacksDataMustBeAnObject(t *testing.T) {
	const program = `road(?from, ?to) => ?to`
	for _, tc := range []struct {
		data func(mcp.Answer) (any, error)
		want string
	}{
		{func(mcp.Answer) (any, error) { return []string{"a"}, nil }, "gave an array"},
		{func(mcp.Answer) (any, error) { return "a", nil }, "gave a string"},
		{func(mcp.Answer) (any, error) { return nil, errors.New("no link for this design") }, "no link for this design"},
	} {
		p := pack
		p.Data = tc.data
		res := call(t, dial(t, p, mcp.Options{}), "query", map[string]any{"program": program})
		if !res.IsError || !strings.Contains(res.Content[0].Text, tc.want) {
			t.Errorf("want a tool error with %q: %v %s", tc.want, res.IsError, res.Content[0].Text)
		}
	}
}

// A Run's Vocabulary is what the call's schema reads, and the server's own, resolved lazily, is never
// asked for it. control: a call whose Run brings none reads the server's.
func TestARunsVocabularyIsTheCallsOwn(t *testing.T) {
	server, b := roads(t)
	project := withModule(t, roadSource(func(string) bool { return true }), "far", `# Towns two roads away.
two(?a, ?c) :- road(?a, ?b), road(?b, ?c);
`)
	asked := 0
	o := mcp.Options{
		Base: func(context.Context) (*datalog.Base, error) { return b, nil },
		Vocabulary: func(context.Context) (*ns.Vocabulary, error) {
			asked++
			return server, nil
		},
	}
	type proj struct {
		Project string `json:"project,omitempty" jsonschema_description:"The project whose modules a call reads."`
	}
	srv, err := mcp.NewWith(nil, pack, o, mcp.Extension[proj]{Prepare: func(_ context.Context, _ string, a proj) (mcp.Run, error) {
		if a.Project == "" {
			return mcp.Run{}, nil
		}
		return mcp.Run{Vocabulary: project}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	c := testutil.NewTestClient(t, srv)
	if got := c.ToolCall("schema", map[string]any{"project": "p"}); !strings.Contains(got, "far.two(") {
		t.Errorf("the project's schema lacks its module:\n%s", got)
	}
	if got := c.ToolCall("look", map[string]any{"name": "far.two", "project": "p"}); !strings.Contains(got, "Towns two roads away") {
		t.Errorf("look in the project:\n%s", got)
	}
	if asked != 0 {
		t.Errorf("the server's vocabulary was resolved %d times for calls whose Run brought one", asked)
	}
	if got := c.ToolCall("schema", map[string]any{}); strings.Contains(got, "far.two(") || asked != 1 {
		t.Errorf("control: without a project, asked %d, schema:\n%s", asked, got)
	}
}

// withModule is src's vocabulary with the standard library and one more module.
func withModule(t *testing.T, src *ns.MemSource, name, text string) *ns.Vocabulary {
	t.Helper()
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
	if err := v.AddModule(name, datalog.LanguageName, text, ""); err != nil {
		t.Fatal(err)
	}
	return v
}

func remarshal(from, to any) error {
	data, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, to)
}

// A Run's BaseFor is asked with the program each of query, explain and values runs, and its Base is
// the one the program reads. control: a Run with only a Base reads that Base, every road.
func TestARunsBaseForIsAskedWithTheProgram(t *testing.T) {
	v, whole := roads(t)
	fromA, err := datalog.NewBase(v, roadSource(func(from string) bool { return from == "a" }))
	if err != nil {
		t.Fatal(err)
	}
	var asked []datalog.Query
	type narrow struct {
		Narrow bool `json:"narrow,omitempty" jsonschema_description:"Read only the roads from a."`
	}
	srv, err := mcp.NewWith(v, pack, mcp.Options{Base: func(context.Context) (*datalog.Base, error) { return whole, nil }},
		mcp.Extension[narrow]{Prepare: func(_ context.Context, _ string, a narrow) (mcp.Run, error) {
			if !a.Narrow {
				return mcp.Run{Base: whole}, nil
			}
			return mcp.Run{BaseFor: func(_ context.Context, q datalog.Query) (*datalog.Base, error) {
				asked = append(asked, q)
				return fromA, nil
			}}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	c := testutil.NewTestClient(t, srv)
	const program = `road(?from, ?to) => ?to`
	if got := c.ToolCall("query", map[string]any{"program": program}); !strings.Contains(got, "4 rows") || len(asked) != 0 {
		t.Fatalf("control: with a Base alone, BaseFor asked %d times:\n%s", len(asked), got)
	}
	if got := c.ToolCall("query", map[string]any{"program": program, "narrow": true}); !strings.Contains(got, "1 row\n") {
		t.Errorf("query over BaseFor's base:\n%s", got)
	}
	want, _ := datalog.Parse(program)
	if len(asked) != 1 || !reflect.DeepEqual(asked[0], want) {
		t.Fatalf("BaseFor was asked with %v, want the program once", asked)
	}
	if got := c.ToolCall("explain", map[string]any{"program": program, "narrow": true}); !strings.HasPrefix(got, "1 row\n") || len(asked) != 2 {
		t.Errorf("explain over BaseFor's base, asked %d:\n%s", len(asked), got)
	}
	if got := c.ToolCall("values", map[string]any{"target": "road.from", "narrow": true}); !strings.Contains(got, "1 row") || len(asked) != 3 {
		t.Errorf("values over BaseFor's base, asked %d:\n%s", len(asked), got)
	}
}

// A failing BaseFor is the call's tool error, with its text.
func TestAFailingBaseForIsTheCallsError(t *testing.T) {
	v, b := roads(t)
	srv, err := mcp.NewWith(v, pack, mcp.Options{Base: func(context.Context) (*datalog.Base, error) { return b, nil }},
		mcp.Extension[struct{}]{Prepare: func(context.Context, string, struct{}) (mcp.Run, error) {
			return mcp.Run{BaseFor: func(context.Context, datalog.Query) (*datalog.Base, error) {
				return nil, errors.New("the board tier could not be read")
			}}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	res := call(t, testutil.NewTestClient(t, srv), "query", map[string]any{"program": `road(?from, ?to) => ?to`})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "the board tier could not be read") {
		t.Errorf("%v %s", res.IsError, res.Content[0].Text)
	}
}

// An answer carries what the call bound, by the name the call gave: query's variables without
// their ?, and cypher's parameters. control: an unbound call's Bind is nil.
func TestAnAnswerCarriesWhatTheCallBound(t *testing.T) {
	var got []map[string][]ns.Value
	record := func(a mcp.Answer) string { got = append(got, a.Bind); return "ok" }
	p := pack
	p.Format = record
	c := dial(t, p, mcp.Options{})
	c.ToolCall("query", map[string]any{"program": `road(?from, ?to) => ?to`})
	c.ToolCall("query", map[string]any{"program": `road(?from, ?to) => ?to`, "bind": map[string]any{"?from": []any{"a", "c"}}})
	if len(got) != 2 || got[0] != nil || !reflect.DeepEqual(got[1], map[string][]ns.Value{"from": {ns.S("a"), ns.S("c")}}) {
		t.Errorf("query's Bind: %v", got)
	}

	cp := codePack(codeGraph("Func"))
	cp.Format = record
	cy := dialCode(t, cp, mcp.Options{})
	cy.ToolCall("cypher", map[string]any{"query": `MATCH (a:Func {name: $who})-[:CALLS]->(b:Func) RETURN b.name`, "params": map[string]any{"who": "main"}})
	if len(got) != 3 || !reflect.DeepEqual(got[2], map[string][]ns.Value{"who": {ns.S("main")}}) {
		t.Errorf("cypher's Bind: %v", got[len(got)-1])
	}
}
