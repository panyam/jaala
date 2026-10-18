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

// A WITH closes a stage, a rule whose head is what it hands on, so its
// aggregate can be filtered as Cypher's HAVING. A parameter such a rule reads
// is out of Bind's reach, so cypher.Params writes its value in.
func Example_cypherWith() {
	runCypher(`MATCH (a:Package)-[:IMPORTS*]->(d:Package)
WITH a, count(DISTINCT d) AS n WHERE n > 1
RETURN a.name AS name, n ORDER BY n DESC`, nil)
	v, err := vocabulary(source())
	if err != nil {
		panic(err)
	}
	q := `MATCH (:Package {name: $pkg})-[:IMPORTS*]->(d) WITH d RETURN d.name`
	_, err = cypher.Compile(v, depsGraph, q)
	fmt.Println(err)
	p, err := cypher.Compile(v, depsGraph, q, cypher.Params(map[string]ns.Value{"pkg": ns.S("api")}))
	if err != nil {
		panic(err)
	}
	fmt.Println(p.Text)
	// Output:
	// cypher_reach1(?x, ?y) :- imports(?x, ?y);
	// cypher_reach1(?x, ?y) :- cypher_reach1(?x, ?z), imports(?z, ?y);
	// cypher_with1(?a, count(distinct ?d)) :- pkg(?a, _), pkg(?d, _), cypher_reach1(?a, ?d);
	// cypher_with1(?a, ?n), ?n > 1 => ?a, ?n order by ?n desc
	// name | n
	// "app" | "3"
	// "api" | "2"
	// cypher: $pkg is read inside a WITH stage, a UNION part, an OR, a pattern test or a variable-length relationship, which datalog.Bind can't reach: compile with cypher.Params
	// cypher_reach1(?x, ?y) :- imports(?x, ?y);
	// cypher_reach1(?x, ?y) :- cypher_reach1(?x, ?z), imports(?z, ?y);
	// cypher_with1(?d) :- pkg("api", _), cypher_reach1("api", ?d);
	// cypher_with1(?d) => ?d
}

// COUNT { } counts a pattern's matches from each row's nodes, a node with none
// counting 0, and UNION joins parts returning the same columns.
func Example_cypherCountAndUnion() {
	runCypher(`MATCH (p:Package) RETURN p.name AS name, COUNT { (p)<-[:IMPORTS]-() } AS importers ORDER BY name`, nil)
	runCypher(`MATCH (p:Package {license: 'MIT'}) RETURN p.name AS name
UNION MATCH (p:Package) WHERE p.license IS NULL RETURN p.name AS name`, nil)
	// Output:
	// pkg(?p, _), ?_count = count(?_n) : { imports(?_n, ?p) } => ?p, ?_count order by ?p
	// name | importers
	// "api" | "1"
	// "app" | "0"
	// "log" | "1"
	// "util" | "1"
	// cypher_union1(?p) :- pkg(?p, "MIT");
	// cypher_union1(?p) :- pkg(?p, ?p_license), absent(?p_license);
	// cypher_union1(?name) => ?name
	// name
	// "api"
	// "app"
}

// Cypher a program can't express yet is refused with the issue that would
// let it, so a model or a caller knows to rewrite rather than retry.
func Example_cypherGap() {
	v, err := vocabulary(source())
	if err != nil {
		panic(err)
	}
	q := `MATCH (p:Package) OPTIONAL MATCH (p)-[:IMPORTS]->(q) RETURN p, q`
	p, err := cypher.Compile(v, depsGraph, q)
	var ce *cypher.Error
	if errors.As(err, &ce) {
		fmt.Printf("gap #%d\n", ce.Gap)
	}
	fmt.Print(cypher.Explain(q, p, err))
	// Output:
	// gap #282
	// cypher: not supported yet: OPTIONAL MATCH and null values (jaala#282): write the match without OPTIONAL, or as a second query
	// line 1, column 19:
	//   MATCH (p:Package) OPTIONAL MATCH (p)-[:IMPORTS]->(q) RETURN p, q
	//                     ^
	// Write the query without it.
}

// A store keeping every node in node(id, kind), every relationship in
// edge(from, kind, to) and properties in attr(id, key, value) needs no graph
// written by hand: cypher.Store reads its kinds and writes one.
func Example_cypherStore() {
	src := ns.NewMemSource().
		Declare("node", "id", "kind").
		Declare("edge", "from", "kind", "to").
		Declare("attr", "id", "key", "value")
	add := func(rel string, vals ...string) {
		var t ns.Tuple
		for _, v := range vals {
			t.Vals = append(t.Vals, ns.S(v))
		}
		src.Add(rel, t)
	}
	add("node", "f:main", "func")
	add("node", "f:parse", "func")
	add("node", "t:main", "test")
	add("edge", "f:main", "calls", "f:parse")
	add("edge", "t:main", "covers", "f:main")
	add("attr", "f:parse", "doc", "reads the input")
	v := ns.MustVocabulary(src)
	b := datalog.MustBase(v, src)

	store := cypher.Store{Node: "node", Edge: "edge", Attr: "attr"}
	nodeKinds, edgeKinds, err := store.Kinds(context.Background(), b)
	if err != nil {
		panic(err)
	}
	g := store.Graph(nodeKinds, edgeKinds)
	var names []string
	for _, n := range g.Nodes {
		names = append(names, n.Label)
	}
	for _, e := range g.Edges {
		names = append(names, e.Type)
	}
	fmt.Println(strings.Join(names, " "))
	p, err := cypher.Compile(v, g, `MATCH (t:Test)-[:COVERS]->(:Func)-[:CALLS]->(f:Func) RETURN t, f, f.doc`)
	if err != nil {
		panic(err)
	}
	rows, err := datalog.SemiNaive{}.Eval(context.Background(), p.Query, b)
	if err != nil {
		panic(err)
	}
	for _, r := range rows {
		var vals []string
		for _, c := range p.Query.Columns() {
			vals = append(vals, show(r.Bind[c]))
		}
		fmt.Println(strings.Join(vals, " | "))
	}
	// Output:
	// Func Test CALLS COVERS
	// "t:main" | "f:parse" | "reads the input"
}
