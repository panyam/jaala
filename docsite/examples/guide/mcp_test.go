package guide_test

import (
	"context"
	"fmt"
	"net/http/httptest"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/mcp"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// depsPack is the server's pack: what the data is about and a worked
// example, which its instructions and tool descriptions are written from,
// and the graph a Cypher query reads (depsGraph, on the Cypher page).
var depsPack = mcp.Pack{
	Pack: nl.Pack{
		About: "the packages of a Go service and what each imports",
		Examples: []nl.Example{
			{Question: "what does api import?", Program: `imports("api", ?d) => ?d`},
		},
	},
	Name:   "deps",
	Cypher: &depsGraph,
}

// The instructions an agent reads when it connects, and the line for its
// own instructions (a CLAUDE.md, say), are written from the pack.
func Example_mcpInstructions() {
	fmt.Println(mcp.Instructions(depsPack, false))
	fmt.Println(mcp.AgentLine(depsPack))
	// Output:
	// the packages of a Go service and what each imports
	//
	// Ask with the query tool, in jaala's Datalog. A program is rules, if any, then one goal: 'rule(?a, ?b) :- body; ... body => ?a, ?b'. Variables are ?name and strings are "quoted"; there's no "?-" before a goal, and its columns follow => without parentheses. Every row carries the facts it was derived from.
	//
	// Before writing a program, call schema with no path for every relation and module, and with a module's or relation's path for its members and docs.
	//
	// For example:
	//   # what does api import?
	//   imports("api", ?d) => ?d
	//
	// Or ask in Cypher with the cypher tool: MATCH, WHERE and RETURN over this graph, and it answers with the program it ran. Node labels: Package {license, name}. Relationship types: IMPORTS.
	//
	// The explain tool says where a program's work went, and why its answer is empty.
	//
	// For questions about the packages of a Go service and what each imports, use the deps MCP server before searching files: it answers exactly from its graph. Call its schema tool, then query, or cypher to ask in Cypher.
}

// mcp.New builds an MCP server for the vocabulary; a host serves it over the
// transport it wants (here HTTP, as mcpkit's Handler serves it) and an agent
// calls its tools.
func Example_mcpServer() {
	v, err := vocabulary(source())
	if err != nil {
		panic(err)
	}
	b := datalog.MustBase(v, source())
	srv, err := mcp.New(v, depsPack, mcp.Options{
		Base:  func(context.Context) (*datalog.Base, error) { return b, nil },
		Limit: 2,
	})
	if err != nil {
		panic(err)
	}
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	defer ts.Close()

	ctx := context.Background()
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "agent", Version: "0.0.1"})
	if err := c.Connect(ctx); err != nil {
		panic(err)
	}
	defer c.Close()
	call := func(tool string, args map[string]any) {
		text, err := c.ToolCall(ctx, tool, args)
		if err != nil {
			fmt.Printf("%s: error: %v\n", tool, err)
			return
		}
		fmt.Printf("%s:\n%s\n", tool, text)
	}
	call("schema", map[string]any{"path": "deps"})
	call("query", map[string]any{"program": `deps.depends_on("app", ?d) => ?d`})
	call("query", map[string]any{"program": `depends_on("app", ?d) => ?d`})
	call("cypher", map[string]any{"query": `MATCH (p:Package)-[:IMPORTS]->(:Package {name: $to}) RETURN p.name AS importer`,
		"params": map[string]any{"to": "log"}})
	// Output:
	// schema:
	// module deps
	// deps.depends_on(a, b) # Everything a package depends on, directly or through others.
	//
	// query:
	// columns: d
	// 1. api
	//    deps.depends_on("app", "api")
	//       deps._step("app", "api")
	//          imports("app", "api")
	// 2. log
	//    deps.depends_on("app", "log")
	//       deps.depends_on("app", "api")
	//          deps._step("app", "api")
	//             imports("app", "api")
	//       deps._step("api", "log")
	//          imports("api", "log")
	// rows 1 to 2 of 3; continue with page_token "2"
	//
	// query: error: tool error: query: unknown relation "depends_on"; did you mean "deps.depends_on"?
	// line 1, column 1:
	//   depends_on("app", ?d) => ?d
	//   ^
	// fix: did you mean "deps.depends_on"?
	// cypher:
	// ran: pkg(?p, _), pkg(?to, _), imports(?p, ?to) => ?p
	// columns: importer
	// 1. api
	//    pkg("api", absent)
	//    pkg("log", "")
	//    imports("api", "log")
	// 1 row
}
