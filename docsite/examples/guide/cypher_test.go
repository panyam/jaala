package guide_test

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/frontends/nl"
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
	// pkg("app", _), pkg(?b, ?b_license), graph.closure("imports", "app", ?b, 1, 0, _) => ?b, ?b_license order by ?b
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
	// cypher_with1(?a, count(distinct ?d)) :- pkg(?a, _), pkg(?d, _), graph.closure("imports", ?a, ?d, 1, 0, _);
	// cypher_with1(?a, ?n), ?n > 1 => ?a, ?n order by ?n desc
	// name | n
	// "app" | "3"
	// "api" | "2"
	// cypher: $pkg is read inside a WITH stage, a UNION part, an OR, a pattern test, a variable-length relationship or an OPTIONAL MATCH, which datalog.Bind can't reach: compile with cypher.Params
	// cypher_with1(?d) :- pkg("api", _), graph.closure("imports", "api", ?d, 1, 0, _);
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

// OPTIONAL MATCH keeps a row whose optional part doesn't match, with null
// (jaala's absent) in its place, and count over an optional node counts only
// its matches, as Cypher's skips a null.
func Example_cypherOptional() {
	runCypher(`MATCH (p:Package) OPTIONAL MATCH (p)-[:IMPORTS]->(d) RETURN p.name AS name, d.name AS imports`, nil)
	runCypher(`MATCH (p:Package) OPTIONAL MATCH (p)<-[:IMPORTS]-(i) RETURN p.name AS name, count(i) AS importers`, nil)
	// Output:
	// pkg(?p, _), optional { imports(?p, ?d) } => ?p, ?d
	// name | imports
	// "api" | "log"
	// "app" | "api"
	// "log" | "util"
	// "util" | absent
	// pkg(?p, _), optional { imports(?i, ?p), ?_seen = 1 * 1 } => ?p, sum(?_seen)
	// name | importers
	// "api" | "1"
	// "app" | "0"
	// "log" | "1"
	// "util" | "1"
}

// Cypher a program can't express yet is refused with the issue that would
// let it, so a model or a caller knows to rewrite rather than retry.
func Example_cypherGap() {
	v, err := vocabulary(source())
	if err != nil {
		panic(err)
	}
	q := `MATCH (p:Package) SET p.license = 'MIT' RETURN p`
	p, err := cypher.Compile(v, depsGraph, q)
	var ce *cypher.Error
	if errors.As(err, &ce) {
		fmt.Printf("gap #%d\n", ce.Gap)
	}
	fmt.Print(cypher.Explain(q, p, err))
	// Output:
	// gap #287
	// cypher: not supported yet: writing to the graph (jaala#287): SET
	// line 1, column 19:
	//   MATCH (p:Package) SET p.license = 'MIT' RETURN p
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

// A property the host computes rather than stores, here a node's short name
// from its id, is read through a relation of its own: Derived names it, and
// each read is an atom over it, so the predicate answers by name or by node.
func Example_cypherDerived() {
	src := ns.NewMemSource().
		Declare("node", "id", "kind").
		Declare("edge", "from", "kind", "to")
	src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S("f:main"), ns.S("func")}})
	src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S("f:parse"), ns.S("func")}})
	src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S("f:main"), ns.S("calls"), ns.S("f:parse")}})
	v := ns.MustVocabulary(src)
	short := func(id string) string { return id[strings.Index(id, ":")+1:] }
	err := v.AddPredicate("name.of", ns.Builtin{
		Arity: 2,
		Modes: [][]bool{{true, false}, {false, true}},
		Gen: func(_ context.Context, _ ns.Source, args []ns.Arg, emit func([]ns.Value, []string) error) error {
			if args[0].Bound {
				return emit([]ns.Value{args[0].Value, ns.S(short(args[0].Value.S))}, nil)
			}
			// By name: a real host asks its name index.
			for _, id := range []string{"f:main", "f:parse"} {
				if short(id) == args[1].Value.S {
					if err := emit([]ns.Value{ns.S(id), args[1].Value}, nil); err != nil {
						return err
					}
				}
			}
			return nil
		},
	})
	if err != nil {
		panic(err)
	}
	b := datalog.MustBase(v, src)

	store := cypher.Store{Node: "node", Edge: "edge",
		Derived: map[string]cypher.PropSource{"name": {Relation: "name.of", Node: 0, Value: 1}}}
	g := store.Graph([]string{"func"}, []string{"calls"})
	p, err := cypher.Compile(v, g, `MATCH (f:Func {name: 'parse'})<-[:CALLS]-(g) RETURN g, g.name`)
	if err != nil {
		panic(err)
	}
	fmt.Println(p.Text)
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
	// node(?f, "func"), edge(?g, "calls", ?f), name.of(?f, "parse"), name.of(?g, ?g_name) => ?g, ?g_name
	// "f:main" | "main"
}

// With the Cypher front end, Translate asks a model for Cypher: the prompt
// is the graph and Cypher's instructions, each draft is compiled before it
// is checked, and a refusal goes back in Cypher's terms.
func Example_cypherTranslate() {
	v, err := vocabulary(source())
	if err != nil {
		panic(err)
	}
	m := &scriptedModel{drafts: []string{
		`MATCH (:Pakage {name: 'app'})-[:IMPORTS*]->(d) RETURN d.name`,
		`MATCH (:Package {name: 'app'})-[:IMPORTS*]->(d) RETURN d.name`,
	}}
	fe := cypher.NewFrontEnd(depsGraph)
	p, tr, err := nl.Translate(context.Background(), "what does app depend on?", v, nil, m,
		nl.Options{Base: datalog.MustBase(v, source()), FrontEnd: fe})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, t := range tr.Tries {
		fmt.Printf("try in %s: %q\n", t.Language, t.Kind)
	}
	fmt.Println(p.Text)
	fmt.Println("compiled to:", strings.Join(strings.Fields(p.Compiled), " "))
	for _, r := range p.Rows {
		fmt.Print(r.Bind["d"].S, " ")
	}
	fmt.Println()
	// Output:
	// told: cypher: no label Pakage in this graph, did you mean "Package"? (its labels: Package)
	// line 1, column 7:
	//   MATCH (:Pakage {name: 'app'})-[:IMPORTS*]->(d) RETURN d.name
	//         ^
	//
	// Write the whole query again, fixed.
	//
	// try in cypher: "compile"
	// try in cypher: ""
	// MATCH (:Package {name: 'app'})-[:IMPORTS*]->(d) RETURN d.name
	// compiled to: pkg("app", _), graph.closure("imports", "app", ?d, 1, 0, _) => ?d
	// api log util
}
