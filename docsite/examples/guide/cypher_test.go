package guide_test

import (
	"context"
	"fmt"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/ns"
)

// packages is the vocabulary read as a graph: a Package is a pkg fact, keyed by its name, with the
// license as a property; IMPORTS is imports, from to to; DEPENDS_ON is the deps module's relation.
var packages = cypher.Graph{
	Nodes: []cypher.Node{{Label: "Package", Relation: "pkg", Key: 0, Props: map[string]int{"name": 0, "license": 1}}},
	Edges: []cypher.Edge{
		{Type: "IMPORTS", Relation: "imports", From: 0, To: 1},
		{Type: "DEPENDS_ON", Relation: "deps.depends_on", From: 0, To: 1},
	},
}

// Compile writes the jaala program a Cypher query asks, which then runs like any other.
func Example_cypherCompile() {
	src := source()
	v, err := vocabulary(src)
	if err != nil {
		panic(err)
	}
	b, err := datalog.NewBase(v, src)
	if err != nil {
		panic(err)
	}
	p, err := cypher.Compile(v, packages, `
		MATCH (a:Package)-[:IMPORTS*]->(d:Package)
		WITH a, count(DISTINCT d) AS n
		WHERE n > 1
		RETURN a.name AS name, n ORDER BY n DESC`)
	if err != nil {
		panic(err)
	}
	fmt.Println(p.Text)
	rows, err := datalog.SemiNaive{}.Eval(context.Background(), p.Query, b)
	if err != nil {
		panic(err)
	}
	for _, r := range rows {
		fmt.Println(r.Bind["a"].S, r.Bind["n"].S)
	}
	// Output:
	// cypher_reach1(?x, ?y) :- imports(?x, ?y);
	// cypher_reach1(?x, ?y) :- cypher_reach1(?x, ?z), imports(?z, ?y);
	// cypher_with1(?a, count(distinct ?d)) :- pkg(?a, _), pkg(?d, _), cypher_reach1(?a, ?d);
	// cypher_with1(?a, ?n), ?n > 1 => ?a, ?n order by ?n desc
	// app 3
	// api 2
}

// A parameter is a goal variable a host binds, or with cypher.Params a constant written into every
// rule, which a WITH stage needs.
func Example_cypherParams() {
	v, err := vocabulary(source())
	if err != nil {
		panic(err)
	}
	q := `MATCH (:Package {name: $pkg})-[:IMPORTS*]->(d) WITH d RETURN d.name`
	_, err = cypher.Compile(v, packages, q)
	fmt.Println(err)
	p, err := cypher.Compile(v, packages, q, cypher.Params(map[string]ns.Value{"pkg": ns.S("api")}))
	if err != nil {
		panic(err)
	}
	fmt.Println(p.Text)
	// Output:
	// cypher: $pkg is read inside a WITH stage, a UNION part, an OR, a pattern test or a variable-length relationship, which datalog.Bind can't reach: compile with cypher.Params
	// cypher_reach1(?x, ?y) :- imports(?x, ?y);
	// cypher_reach1(?x, ?y) :- cypher_reach1(?x, ?z), imports(?z, ?y);
	// cypher_with1(?d) :- pkg("api", _), cypher_reach1("api", ?d);
	// cypher_with1(?d) => ?d
}

// What a program can't express yet is refused naming the issue that would let it.
func Example_cypherRefused() {
	v, err := vocabulary(source())
	if err != nil {
		panic(err)
	}
	for _, q := range []string{
		`MATCH (a:Package) OPTIONAL MATCH (a)-[:IMPORTS]->(b) RETURN a, b`,
		`MATCH p = (a:Package)-[:IMPORTS*]->(b) RETURN length(p)`,
		`MATCH (p:Package) RETURN p.maintainer`,
	} {
		_, err := cypher.Compile(v, packages, q)
		fmt.Println(err)
	}
	// Output:
	// cypher: not supported yet: OPTIONAL MATCH and null values (jaala#282): write the match without OPTIONAL, or as a second query
	// cypher: not supported yet: paths as values (jaala#280): a path variable (p = ...)
	// cypher: no property maintainer on p:Package (its properties: license, name)
}
