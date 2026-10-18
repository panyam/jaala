package guide_test

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/ns"
)

// depsGraph reads the guide's relations as a property graph: a Package is a
// pkg fact keyed by its name, with its license as a property, and IMPORTS is
// an imports fact from its first argument to its second.
var depsGraph = cypher.Graph{
	Nodes: []cypher.Node{
		{Label: "Package", Relation: "pkg", Key: 0, Props: map[string]int{"name": 0, "license": 1}},
	},
	Edges: []cypher.Edge{
		{Type: "IMPORTS", Relation: "imports", From: 0, To: 1},
	},
}

// runCypher compiles a query, prints the program it compiled to, and runs
// that program, binding the query's $parameters.
func runCypher(text string, params map[string]ns.Value) {
	v, err := vocabulary(source())
	if err != nil {
		panic(err)
	}
	p, err := cypher.Compile(v, depsGraph, text)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(p.Text)
	var opts []datalog.Option
	if params != nil {
		bind, err := p.Bind(params)
		if err != nil {
			panic(err)
		}
		opts = append(opts, bind)
	}
	t, err := datalog.SemiNaive{}.EvalTable(context.Background(), p.Query, datalog.MustBase(v, source()), opts...)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(strings.Join(p.Columns, " | "))
	for i := range t.Len() {
		r := t.Row(i)
		var vals []string
		for _, c := range t.Columns {
			vals = append(vals, show(r.Bind[c]))
		}
		fmt.Println(strings.Join(vals, " | "))
	}
}

// Compile turns Cypher into a jaala program over the graph view; the program
// is ordinary jaala, run like any other.
func Example_cypher() {
	runCypher(`MATCH (a:Package {name: 'app'})-[:IMPORTS*]->(b:Package)
RETURN b.name AS dependency, b.license AS license ORDER BY dependency`, nil)
	// Output:
	// cypher_reach1(?x, ?y) :- imports(?x, ?y);
	// cypher_reach1(?x, ?y) :- cypher_reach1(?x, ?z), imports(?z, ?y);
	// pkg("app", _), pkg(?b, ?b_license), cypher_reach1("app", ?b) => ?b, ?b_license order by ?b
	// dependency | license
	// "api" | absent
	// "log" | ""
	// "util" | "Apache-2.0"
}

// A $parameter is a goal variable, bound with Program.Bind.
func Example_cypherParameter() {
	runCypher(`MATCH (p:Package)-[:IMPORTS]->(:Package {name: $target}) RETURN p.name`,
		map[string]ns.Value{"target": ns.S("log")})
	// Output:
	// pkg(?p, _), pkg(?target, _), imports(?p, ?target) => ?p
	// p.name
	// "api"
}

// Cypher a program can't express yet is refused with the issue that would
// let it, so a model or a caller knows to rewrite rather than retry.
func Example_cypherGap() {
	v, err := vocabulary(source())
	if err != nil {
		panic(err)
	}
	_, err = cypher.Compile(v, depsGraph, `MATCH (p:Package) OPTIONAL MATCH (p)-[:IMPORTS]->(q) RETURN p, q`)
	var ce *cypher.Error
	if errors.As(err, &ce) {
		fmt.Printf("line %d, column %d: gap #%d\n", ce.Pos.Line, ce.Pos.Column, ce.Gap)
	}
	fmt.Println(err)
	// Output:
	// line 1, column 19: gap #282
	// cypher: not supported yet: OPTIONAL MATCH and null values (jaala#282): write the match without OPTIONAL, or as a second query
}
