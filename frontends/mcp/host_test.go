package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/mcp"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/mcpkit/testutil"
)

// trip is the toy host's own arguments: the towns a call's roads leave from, and how much an
// answer says.
type trip struct {
	Towns  []string `json:"towns,omitempty" jsonschema_description:"Only roads leaving these towns."`
	Detail int      `json:"detail,omitempty" jsonschema_description:"How much an answer says."`
}

// prepared records what Prepare was handed.
type prepared struct {
	tool string
	a    trip
}

// tripHost is the toy host's Extension: towns narrows the Base, and a call naming any argument
// gets a pack whose Card names its towns and whose Format writes its detail. A town "x" fails, and a
// negative detail is a budget instead.
func tripHost(t *testing.T, v *ns.Vocabulary, seen *[]prepared) mcp.Extension[trip] {
	return mcp.Extension[trip]{Prepare: func(_ context.Context, tool string, a trip) (mcp.Run, error) {
		*seen = append(*seen, prepared{tool, a})
		if slices.Contains(a.Towns, "x") {
			return mcp.Run{}, errors.New("no town x on this map")
		}
		if a.Detail < 0 {
			return mcp.Run{Eval: []datalog.Option{datalog.Budget(int64(-a.Detail))}}, nil
		}
		if a.Towns == nil && a.Detail == 0 {
			return mcp.Run{}, nil
		}
		b, err := datalog.NewBase(v, roadSource(func(from string) bool { return slices.Contains(a.Towns, from) }))
		if err != nil {
			t.Fatal(err)
		}
		p := pack
		p.Card = "# Roads from " + strings.Join(a.Towns, ", ") + "."
		p.Format = func(ans mcp.Answer) string { return fmt.Sprintf("detail %d: %d rows", a.Detail, ans.Table.Len()) }
		return mcp.Run{Base: b, Pack: &p}, nil
	}}
}

func dialWith[A any](t *testing.T, o mcp.Options, x func(*ns.Vocabulary) mcp.Extension[A]) *testutil.TestClient {
	t.Helper()
	v, b := roads(t)
	if o.Base == nil {
		o.Base = func(context.Context) (*datalog.Base, error) { return b, nil }
	}
	srv, err := mcp.NewWith(v, pack, o, x(v))
	if err != nil {
		t.Fatal(err)
	}
	return testutil.NewTestClient(t, srv)
}

type inputSchema struct {
	Properties           map[string]json.RawMessage `json:"properties"`
	Required             []string                   `json:"required"`
	AdditionalProperties *bool                      `json:"additionalProperties"`
}

func schemas(t *testing.T, c *testutil.TestClient) map[string]inputSchema {
	t.Helper()
	out := map[string]inputSchema{}
	for _, tool := range c.ListTools() {
		data, _ := json.Marshal(tool.InputSchema)
		var s inputSchema
		if err := json.Unmarshal(data, &s); err != nil {
			t.Fatal(err)
		}
		out[tool.Name] = s
	}
	return out
}

// A host's arguments sit flat beside each tool's own, in every tool, and the schema allows no others.
func TestHostArgumentsAreFlatOnEveryTool(t *testing.T) {
	var seen []prepared
	c := dialWith(t, mcp.Options{Model: &scripted{name: "small"}}, func(v *ns.Vocabulary) mcp.Extension[trip] { return tripHost(t, v, &seen) })
	own := map[string]string{"query": "program", "schema": "path", "explain": "program", "ask": "question"}
	got := schemas(t, c)
	if len(got) != len(own) {
		t.Fatalf("tools %v", got)
	}
	for tool, arg := range own {
		s := got[tool]
		for _, want := range []string{arg, "towns", "detail"} {
			if _, ok := s.Properties[want]; !ok {
				t.Errorf("%s's schema lacks %q: %v", tool, want, s.Properties)
			}
		}
		if s.AdditionalProperties == nil || *s.AdditionalProperties {
			t.Errorf("%s's schema allows other arguments", tool)
		}
	}
	if d := string(got["query"].Properties["towns"]); !strings.Contains(d, "Only roads leaving these towns.") || !strings.Contains(d, `"array"`) {
		t.Errorf("towns' schema %s", d)
	}
	type board struct {
		Board string `json:"board" jsonschema_description:"A board."`
	}
	required := schemas(t, dialWith(t, mcp.Options{}, func(*ns.Vocabulary) mcp.Extension[board] { return mcp.Extension[board]{} }))["query"].Required
	if !slices.Equal(required, []string{"program", "board"}) {
		t.Errorf("query requires %v", required)
	}
}

// Prepare gets each call's tool and host arguments decoded, and its Run decides the Base, the format
// and the data card; a call naming none runs as the server would.
func TestPrepareDecidesWhatTheHostArgumentsMean(t *testing.T) {
	var seen []prepared
	small := &scripted{name: "small", replies: []string{`trip.reach(?a, ?b) => ?a, ?b`, `trip.reach(?a, ?b) => ?a, ?b`}}
	c := dialWith(t, mcp.Options{Model: small}, func(v *ns.Vocabulary) mcp.Extension[trip] { return tripHost(t, v, &seen) })
	program := `trip.reach(?a, ?b) => ?a, ?b`
	host := map[string]any{"towns": []string{"c", "d"}, "detail": 2}
	with := func(args map[string]any) map[string]any {
		for k, v := range host {
			args[k] = v
		}
		return args
	}
	if text := c.ToolCall("query", with(map[string]any{"program": program})); text != "detail 2: 3 rows" {
		t.Errorf("query: %q", text)
	}
	if text := c.ToolCall("query", map[string]any{"program": program}); !strings.Contains(text, "10 rows") {
		t.Errorf("control: query over the server's Base and format:\n%s", text)
	}
	if text := c.ToolCall("schema", with(map[string]any{"path": "data"})); text != "# Roads from c, d." {
		t.Errorf("schema data: %q", text)
	}
	if text := c.ToolCall("schema", map[string]any{"path": "data"}); text != pack.Card {
		t.Errorf("control: schema data: %q", text)
	}
	if text := c.ToolCall("explain", with(map[string]any{"program": `road(?a, ?b) => ?a`})); !strings.HasPrefix(text, "2 rows\n") {
		t.Errorf("explain:\n%s", text)
	}
	if text := c.ToolCall("ask", with(map[string]any{"question": "where do roads go?"})); !strings.Contains(text, "detail 2: 3 rows") {
		t.Errorf("ask:\n%s", text)
	}
	if text := c.ToolCall("ask", map[string]any{"question": "where do roads go?"}); !strings.Contains(text, "10 rows") {
		t.Errorf("control: ask over the server's Base and format:\n%s", text)
	}
	var tools []string
	for _, p := range seen {
		tools = append(tools, p.tool)
		if p.a.Towns != nil && (!slices.Equal(p.a.Towns, []string{"c", "d"}) || p.a.Detail != 2) {
			t.Errorf("%s was prepared with %+v", p.tool, p.a)
		}
	}
	if want := []string{"query", "query", "schema", "schema", "explain", "ask", "ask"}; !slices.Equal(tools, want) {
		t.Errorf("prepared %v, want %v", tools, want)
	}
	if res := call(t, c, "query", map[string]any{"program": program, "detail": -3}); !res.IsError || !strings.Contains(res.Content[0].Text, "budget of 3") {
		t.Errorf("a Run's Eval: %+v", res.Content)
	}
	res := call(t, c, "query", map[string]any{"program": program, "towns": []string{"x"}})
	if !res.IsError || res.Content[0].Text != "no town x on this map" {
		t.Errorf("a Prepare error: %+v", res.Content)
	}
}

// NewWith refuses host arguments that aren't a struct's or that a tool already names, ask's too when
// the server has no model to offer it with.
func TestNewWithRefusesArgumentsATool(t *testing.T) {
	v, b := roads(t)
	o := mcp.Options{Base: func(context.Context) (*datalog.Base, error) { return b, nil }}
	type paged struct {
		Limit int `json:"limit,omitempty"`
	}
	if _, err := mcp.NewWith(v, pack, o, mcp.Extension[paged]{}); err == nil || !strings.Contains(err.Error(), `argument "limit" is one of query's own`) {
		t.Errorf("limit: %v", err)
	}
	type asking struct {
		Question string `json:"question,omitempty"`
	}
	if _, err := mcp.NewWith(v, pack, o, mcp.Extension[asking]{}); err == nil || !strings.Contains(err.Error(), `"question" is one of ask's own`) {
		t.Errorf("question with no model: %v", err)
	}
	if _, err := mcp.NewWith(v, pack, o, mcp.Extension[int]{}); err == nil || !strings.Contains(err.Error(), "want a struct") {
		t.Errorf("an int: %v", err)
	}
	if _, err := mcp.NewWith(v, pack, o, mcp.Extension[trip]{}); err != nil {
		t.Errorf("control: %v", err)
	}
}

// An argument neither the tool nor the host names fails the call rather than being ignored, on a
// server with host arguments and one without.
func TestAnUnknownArgumentFailsTheCall(t *testing.T) {
	var seen []prepared
	c := dialWith(t, mcp.Options{}, func(v *ns.Vocabulary) mcp.Extension[trip] { return tripHost(t, v, &seen) })
	program := `road(?a, ?b) => ?a`
	if res := call(t, c, "query", map[string]any{"program": program, "town": []string{"c"}}); !res.IsError || !strings.Contains(res.Content[0].Text, "town") {
		t.Errorf("town: %+v", res.Content)
	}
	if res := call(t, c, "query", map[string]any{"program": program, "towns": []string{"c"}}); res.IsError {
		t.Errorf("control: towns: %+v", res.Content)
	}
	plain := dial(t, pack, mcp.Options{})
	if res := call(t, plain, "query", map[string]any{"program": program, "max_works": 5}); !res.IsError || !strings.Contains(res.Content[0].Text, "max_works") {
		t.Errorf("max_works: %+v", res.Content)
	}
	if res := call(t, plain, "query", map[string]any{"program": program, "max_work": 5000}); res.IsError {
		t.Errorf("control: max_work: %+v", res.Content)
	}
}
