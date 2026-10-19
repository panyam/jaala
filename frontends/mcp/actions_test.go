package mcp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/frontends/mcp"
	"github.com/panyam/jaala/frontends/nl"
)

// look and values say what nl.Translate's actions say, so an agent and a small model read the same
// thing: look a relation's signature and doc, values an argument's distinct values, each refusal
// naming what is there.
func TestLookAndValuesSayWhatTheLoopSays(t *testing.T) {
	v, _ := roads(t)
	c := dial(t, pack, mcp.Options{})
	want, _ := nl.Look(v, nil, "road")
	if got := c.ToolCall("look", map[string]any{"name": "road"}); got != want+"\n" || !strings.Contains(got, "A one-way road") {
		t.Errorf("look road = %q, want %q", got, want)
	}
	if got := c.ToolCall("values", map[string]any{"target": "road.from"}); !strings.Contains(got, "1. a\n") || !strings.Contains(got, "4. d\n") || !strings.Contains(got, "4 rows") || strings.Contains(got, "map:1") {
		t.Errorf("values road.from, without witnesses:\n%s", got)
	}
	if got := c.ToolCall("values", map[string]any{"target": "road.from", "limit": 2}); !strings.Contains(got, "2. b\n") || !strings.Contains(got, "2 rows\n") || strings.Contains(got, "page_token") {
		t.Errorf("two values of road.from:\n%s", got)
	}
	if res := call(t, c, "values", map[string]any{"target": "road.nope"}); !res.IsError || !strings.Contains(res.Content[0].Text, "road's arguments are from, to") {
		t.Errorf("values road.nope: %v %s", res.IsError, res.Content[0].Text)
	}
	if res := call(t, c, "look", map[string]any{"name": "road", "language": "cypher"}); !res.IsError || !strings.Contains(res.Content[0].Text, "no Cypher graph") {
		t.Errorf("look in cypher without a graph: %s", res.Content[0].Text)
	}
}

// With language cypher, look and values speak the graph: a label's look, and a property's values run
// as the cypher tool runs a query.
func TestLookAndValuesSpeakCypher(t *testing.T) {
	c := dialCode(t, codePack(codeGraph("Func")), mcp.Options{})
	if got := c.ToolCall("values", map[string]any{"target": "Func.name", "language": "cypher"}); !strings.HasPrefix(got, "ran: ") || !strings.Contains(got, "lex") || !strings.Contains(got, "4 rows") {
		t.Errorf("values Func.name:\n%s", got)
	}
	v, _ := code(t)
	want, _ := nl.Look(v, cypher.NewFrontEnd(*codeGraph("Func")), "Func")
	if got := c.ToolCall("look", map[string]any{"name": "Func", "language": "cypher"}); got != strings.TrimRight(want, "\n")+"\n" {
		t.Errorf("look Func = %q, want %q", got, want)
	}
}

// towns finds every town whose name starts with the name asked.
type towns struct{}

func (towns) Ground(_ context.Context, name string) ([]nl.Candidate, error) {
	return []nl.Candidate{{Name: name, Values: []string{strings.ToLower(name[:1])}}}, nil
}

// find is offered, and named in the instructions, only for a pack with a Grounder, and answers what
// it found. control: without one there's no find tool and the instructions don't name it.
func TestFindIsOfferedWithAGrounder(t *testing.T) {
	p := pack
	p.Grounder = towns{}
	c := dial(t, p, mcp.Options{})
	if !strings.Contains(toolNames(c), "find") || !strings.Contains(c.Client.Instructions, "find turns a name") {
		t.Fatalf("tools %s\n%s", toolNames(c), c.Client.Instructions)
	}
	if got := c.ToolCall("find", map[string]any{"name": "Atown"}); got != "Atown: \"a\"\n" {
		t.Errorf("find Atown = %q", got)
	}
	plain := dial(t, pack, mcp.Options{})
	if strings.Contains(toolNames(plain), "find") || strings.Contains(plain.Client.Instructions, "find turns") {
		t.Errorf("control: without a grounder, tools %s", toolNames(plain))
	}
}

// A sample runs the program on that many rows, and an empty sampled answer says which literal matched
// nothing, in text and in JSON, and in Cypher at the pattern. control: unsampled, a query answers
// every row and an empty answer only says so.
func TestASampleStopsEarlyAndSaysWhyNothingCame(t *testing.T) {
	c := dial(t, pack, mcp.Options{})
	if got := c.ToolCall("query", map[string]any{"program": `road(?a, _) => ?a`, "sample": 2}); !strings.Contains(got, "2 rows") {
		t.Errorf("sampled:\n%s", got)
	}
	if got := c.ToolCall("query", map[string]any{"program": `road(?a, _) => ?a`}); !strings.Contains(got, "4 rows") {
		t.Errorf("control: unsampled:\n%s", got)
	}
	empty := `road("z", ?b) => ?b`
	if got := c.ToolCall("query", map[string]any{"program": empty, "sample": 2}); !strings.Contains(got, "no rows\n") || !strings.Contains(got, `road("z", ?b) matched nothing`) {
		t.Errorf("sampled empty:\n%s", got)
	}
	res := call(t, c, "query", map[string]any{"program": empty, "sample": 2, "json": true})
	data, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(data), `"why":`) || !strings.Contains(string(data), "matched nothing") {
		t.Errorf("sampled empty as JSON: %s", data)
	}
	if got := c.ToolCall("query", map[string]any{"program": empty}); got != "columns: b\nno rows\n" {
		t.Errorf("control: unsampled empty = %q", got)
	}
	cy := dialCode(t, codePack(codeGraph("Func")), mcp.Options{})
	if got := cy.ToolCall("cypher", map[string]any{"query": `MATCH (f:Func {name: "nope"}) RETURN f`, "sample": 3}); !strings.Contains(got, "no rows\n") || !strings.Contains(got, "^") {
		t.Errorf("sampled empty Cypher:\n%s", got)
	}
}
