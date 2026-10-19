package cypher_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/internal/guide"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

type compileCase struct {
	cypher string
	text   string     // the program Compile writes
	rows   [][]string // what it answers, in column order, in the default order
}

// Over the guide's packages: each construct the subset compiles, the program it writes, and what that
// answers in all three evaluators.
var depsCases = []compileCase{
	{`MATCH (:Package {name: 'app'})-[:IMPORTS]->(d) RETURN d.name`,
		`pkg("app", _), imports("app", ?d) => ?d`, [][]string{{"api"}}},
	{`match (p:Package) where p.name = "util" return p.license`,
		`pkg("util", ?p_license) => ?p_license`, [][]string{{"Apache-2.0"}}},
	{`MATCH (p:Package) RETURN p.name, p.license`,
		`pkg(?p, ?p_license) => ?p, ?p_license`, [][]string{{"api", "absent"}, {"app", "MIT"}, {"log", ""}, {"util", "Apache-2.0"}}},
	{`MATCH (p:Package) WHERE p.license IS NULL RETURN p`,
		`pkg(?p, ?p_license), absent(?p_license) => ?p`, [][]string{{"api"}}},
	{`MATCH (p:Package) WHERE NOT p.license IS NULL AND p.license <> '' RETURN p.name`,
		`pkg(?p, ?p_license), not absent(?p_license), ?p_license != "" => ?p`, [][]string{{"app"}, {"util"}}},
	// The license read in RETURN keeps its column a variable, so the constant becomes a comparison.
	{`MATCH (p:Package {license: 'MIT'}) RETURN p.name, p.license`,
		`pkg(?p, ?p_license), ?p_license = "MIT" => ?p, ?p_license`, [][]string{{"app", "MIT"}}},
	{`MATCH (p:Package {license: 'MIT'}) RETURN p.name`,
		`pkg(?p, "MIT") => ?p`, [][]string{{"app"}}},
	{`MATCH (x)-[:IMPORTS]->(:Package {name: 'util'}) RETURN x.name AS importer`,
		`pkg("util", _), imports(?x, "util") => ?x`, [][]string{{"log"}}},
	{`MATCH (u:Package {name: 'util'})<-[:IMPORTS]-(x) RETURN x`,
		`pkg("util", _), imports(?x, "util") => ?x`, [][]string{{"log"}}},
	{`MATCH (a:Package {name: 'app'})-[:IMPORTS*]->(d) RETURN DISTINCT d.name`,
		"cypher_reach1(?x, ?y) :- imports(?x, ?y);\ncypher_reach1(?x, ?y) :- cypher_reach1(?x, ?z), imports(?z, ?y);\n" +
			`pkg("app", _), cypher_reach1("app", ?d) => ?d`, [][]string{{"api"}, {"log"}, {"util"}}},
	{`MATCH (a {name: 'app'})-[:IMPORTS*2..]->(d) RETURN d`,
		"cypher_reach1(?x, ?y) :- imports(?x, ?y1), imports(?y1, ?y);\ncypher_reach1(?x, ?y) :- cypher_reach1(?x, ?z), imports(?z, ?y);\n" +
			`cypher_reach1("app", ?d) => ?d`, [][]string{{"log"}, {"util"}}},
	{`MATCH (a {name: 'app'})-[:IMPORTS*1..2]->(d) RETURN d`,
		"cypher_reach1(?x, ?y) :- imports(?x, ?y);\ncypher_reach1(?x, ?y) :- imports(?x, ?y1), imports(?y1, ?y);\n" +
			`cypher_reach1("app", ?d) => ?d`, [][]string{{"api"}, {"log"}}},
	{`MATCH (a {name: 'util'})<-[:IMPORTS*2]-(d) RETURN d`,
		"cypher_reach1(?x, ?y) :- imports(?y1, ?x), imports(?y, ?y1);\n" +
			`cypher_reach1("util", ?d) => ?d`, [][]string{{"api"}}},
	{`MATCH (a {name: 'api'})-[:IMPORTS]-(n) RETURN n`,
		"cypher_step1(?x, ?y) :- imports(?x, ?y);\ncypher_step1(?x, ?y) :- imports(?y, ?x);\n" +
			`cypher_step1("api", ?n) => ?n`, [][]string{{"app"}, {"log"}}},
	{`MATCH (a {name: 'log'})-[:IMPORTS|DEPENDS_ON]->(n) RETURN n`,
		"cypher_step1(?x, ?y) :- imports(?x, ?y);\ncypher_step1(?x, ?y) :- deps.depends_on(?x, ?y);\n" +
			`cypher_step1("log", ?n) => ?n`, [][]string{{"util"}}},
	{`MATCH (:Package {name: 'app'})-[:DEPENDS_ON]->(d) RETURN count(DISTINCT d) AS n`,
		`pkg("app", _), deps.depends_on("app", ?d) => count(distinct ?d)`, [][]string{{"3"}}},
	{`MATCH (a)-[:IMPORTS]->(b) RETURN a, count(*) ORDER BY a DESC LIMIT 2`,
		`imports(?a, ?b) => ?a, count(?a) order by ?a desc limit 2`, [][]string{{"log", "1"}, {"app", "1"}}},
	{`MATCH (p:Package) RETURN p.name AS name ORDER BY name SKIP 0 LIMIT 1`,
		`pkg(?p, _) => ?p order by ?p limit 1`, [][]string{{"api"}}},
	{`MATCH (p:Package) RETURN p.name AS name ORDER BY name SKIP 1 LIMIT 2`,
		`pkg(?p, _) => ?p order by ?p limit 2 offset 1`, [][]string{{"app"}, {"log"}}},
	{`MATCH (p:Package) RETURN collect(p.name)`,
		`pkg(?p, _) => list(?p)`, [][]string{{"api app log util"}}},
	{`MATCH (p:Package) WHERE p.license = 'MIT' OR p.license STARTS WITH 'Apache' RETURN p.name`,
		"cypher_match1(?p) :- pkg(?p, ?p_license), ?p_license = \"MIT\";\ncypher_match1(?p) :- pkg(?p, ?p_license), str.prefix(?p_license, \"Apache\");\n" +
			`cypher_match1(?p) => ?p`, [][]string{{"app"}, {"util"}}},
	{`MATCH (p:Package) WHERE NOT (p.name = 'app' OR p.license IS NULL) RETURN p`,
		`pkg(?p, ?p_license), ?p != "app", not absent(?p_license) => ?p`, [][]string{{"log"}, {"util"}}},
	// Each alternative's equality is written in the key's place, its head too, so demand starts there
	// (#392).
	{`MATCH (p:Package) WHERE p.name IN ['app', 'log'] RETURN p`,
		"cypher_match1(\"app\") :- pkg(\"app\", _);\ncypher_match1(\"log\") :- pkg(\"log\", _);\n" +
			`cypher_match1(?p) => ?p`, [][]string{{"app"}, {"log"}}},
	{`MATCH (a:Package)-[:IMPORTS*]->(d) WHERE d.name IN ['log', 'util'] RETURN a, d.name`,
		"cypher_reach1(?x, ?y) :- imports(?x, ?y);\ncypher_reach1(?x, ?y) :- cypher_reach1(?x, ?z), imports(?z, ?y);\n" +
			"cypher_match1(?a, \"log\") :- pkg(?a, _), cypher_reach1(?a, \"log\");\ncypher_match1(?a, \"util\") :- pkg(?a, _), cypher_reach1(?a, \"util\");\n" +
			`cypher_match1(?a, ?d) => ?a, ?d`, [][]string{{"api", "log"}, {"api", "util"}, {"app", "log"}, {"app", "util"}, {"log", "util"}}},
	// A key the alternative reads again stays a variable, and so does one it equates twice.
	{`MATCH (p:Package) WHERE p.name = 'app' AND p.name STARTS WITH 'a' OR p.name = 'log' RETURN p`,
		"cypher_match1(?p) :- pkg(?p, _), str.prefix(?p, \"a\"), ?p = \"app\";\ncypher_match1(\"log\") :- pkg(\"log\", _);\n" +
			`cypher_match1(?p) => ?p`, [][]string{{"app"}, {"log"}}},
	{`MATCH (p:Package) WHERE p.name = 'app' AND p.name = 'log' OR p.name = 'util' RETURN p`,
		"cypher_match1(?p) :- pkg(?p, _), ?p = \"app\", ?p = \"log\";\ncypher_match1(\"util\") :- pkg(\"util\", _);\n" +
			`cypher_match1(?p) => ?p`, [][]string{{"util"}}},
	{`MATCH (p:Package) WHERE p.name =~ 'a.*' AND NOT p.name ENDS WITH 'i' RETURN p`,
		`pkg(?p, _), str.match(?p, "^(?:a.*)$"), not str.suffix(?p, "i") => ?p`, [][]string{{"app"}}},
	{`MATCH (p:Package) WHERE p.name CONTAINS 'o' XOR p.name STARTS WITH 'u' RETURN p`,
		"cypher_match1(?p) :- pkg(?p, _), str.contains(?p, \"o\"), not str.prefix(?p, \"u\");\ncypher_match1(?p) :- pkg(?p, _), not str.contains(?p, \"o\"), str.prefix(?p, \"u\");\n" +
			`cypher_match1(?p) => ?p`, [][]string{{"log"}, {"util"}}},
	{`MATCH (p:Package) WHERE NOT (p)-[:IMPORTS]->() RETURN p.name`,
		"cypher_exists1(?p) :- imports(?p, ?_n);\n" +
			`pkg(?p, _), not cypher_exists1(?p) => ?p`, [][]string{{"util"}}},
	{`MATCH (p:Package) WHERE (p)-[:IMPORTS]->(:Package {license: ''}) RETURN p.name`,
		"cypher_exists1(?p) :- pkg(?_n, ?_n_license), imports(?p, ?_n), ?_n_license = \"\";\n" +
			`pkg(?p, _), cypher_exists1(?p) => ?p`, [][]string{{"api"}}},
	{`MATCH (a)-[:IMPORTS]->(b)-[:IMPORTS]->(c) WHERE a.name <> c.name RETURN a, c`,
		`imports(?a, ?b), imports(?b, ?c), ?a != ?c => ?a, ?c`, [][]string{{"api", "util"}, {"app", "log"}}},
	{`MATCH (a)-[:IMPORTS]->(b), (b)-[:IMPORTS]->(c) RETURN c`,
		`imports(?a, ?b), imports(?b, ?c) => ?c`, [][]string{{"log"}, {"util"}}},
	{`MATCH (a) WHERE a:Package RETURN a.name`,
		`pkg(?a, _) => ?a`, [][]string{{"api"}, {"app"}, {"log"}, {"util"}}},
	{`MATCH (a) RETURN a`,
		"cypher_node1(?n) :- pkg(?n, _);\ncypher_node1(?a) => ?a", [][]string{{"api"}, {"app"}, {"log"}, {"util"}}},
	{`MATCH (a)-[:IMPORTS]->(b) WHERE id(a) = 'app' RETURN id(b)`,
		`imports(?a, ?b), ?a = "app" => ?b`, [][]string{{"api"}}},
	// A WITH stage is a rule; what it hands on is its head, a node keeping its labels, so a column
	// read after it brings its relation back.
	{`MATCH (a:Package)-[:IMPORTS*]->(d) WITH a, count(DISTINCT d) AS n WHERE n > 1 RETURN a.name, n ORDER BY n DESC`,
		"cypher_reach1(?x, ?y) :- imports(?x, ?y);\ncypher_reach1(?x, ?y) :- cypher_reach1(?x, ?z), imports(?z, ?y);\n" +
			"cypher_with1(?a, count(distinct ?d)) :- pkg(?a, _), cypher_reach1(?a, ?d);\n" +
			`cypher_with1(?a, ?n), ?n > 1 => ?a, ?n order by ?n desc`, [][]string{{"app", "3"}, {"api", "2"}}},
	{`MATCH (a:Package {name: 'app'})-[:IMPORTS*]->(b:Package) WITH b RETURN b.license`,
		"cypher_reach1(?x, ?y) :- imports(?x, ?y);\ncypher_reach1(?x, ?y) :- cypher_reach1(?x, ?z), imports(?z, ?y);\n" +
			"cypher_with1(?b) :- pkg(\"app\", _), pkg(?b, _), cypher_reach1(\"app\", ?b);\n" +
			`cypher_with1(?b), pkg(?b, ?b_license) => ?b_license`, [][]string{{"absent"}, {""}, {"Apache-2.0"}}},
	// control: a carried node whose columns nothing reads needs no atom after the WITH.
	{`MATCH (b:Package) WITH b RETURN b`,
		"cypher_with1(?b) :- pkg(?b, _);\n" + `cypher_with1(?b) => ?b`, [][]string{{"api"}, {"app"}, {"log"}, {"util"}}},
	{`MATCH (a:Package)-[:IMPORTS]->(b) WITH a, b WITH * RETURN a, b`,
		"cypher_with1(?a, ?b) :- pkg(?a, _), imports(?a, ?b);\ncypher_with2(?a, ?b) :- cypher_with1(?a, ?b);\n" +
			`cypher_with2(?a, ?b) => ?a, ?b`, [][]string{{"api", "log"}, {"app", "api"}, {"log", "util"}}},
	{`MATCH (a {name: 'app'})-[:IMPORTS]->(x) RETURN x.name AS n UNION MATCH (y {name: 'util'}) RETURN y.name AS n`,
		"cypher_node1(?n) :- pkg(?n, _);\ncypher_union1(?x) :- imports(\"app\", ?x);\ncypher_union1(?y) :- ?y = \"util\", cypher_node1(?y);\n" +
			`cypher_union1(?n) => ?n`, [][]string{{"api"}, {"util"}}},
	// An aggregating part is a rule of its own, since an aggregating head is its relation's only rule.
	{`MATCH (p:Package) RETURN count(p) AS n UNION ALL MATCH (a)-[:IMPORTS]->(b) RETURN count(b) AS n`,
		"cypher_part1(count(?p)) :- pkg(?p, _);\ncypher_union1(?n) :- cypher_part1(?n);\n" +
			"cypher_part2(count(?b)) :- imports(?a, ?b);\ncypher_union1(?n) :- cypher_part2(?n);\n" +
			`cypher_union1(?n) => ?n`, [][]string{{"3"}, {"4"}}},
	// A call comes after the patterns that bind its arguments, wherever it's written.
	{`MATCH (p:Package {name: 'util'}) CALL str.split(p.license, '-') YIELD index, part RETURN index, part`,
		`pkg("util", ?p_license), str.split(?p_license, "-", ?index, ?part) => ?index, ?part`, [][]string{{"0", "Apache"}, {"1", "2.0"}}},
	{`MATCH (p:Package) CALL str.split(p.license, '-') YIELD part AS piece WHERE piece = '2.0' RETURN p`,
		`pkg(?p, ?p_license), str.split(?p_license, "-", _, ?piece), ?piece = "2.0" => ?p`, [][]string{{"util"}}},
	{`MATCH (p:Package) WHERE EXISTS { (p)-[:IMPORTS]->(q) WHERE q.license = '' } RETURN p`,
		"cypher_exists1(?p) :- imports(?p, ?q), pkg(?q, ?q_license), ?q_license = \"\";\n" +
			`pkg(?p, _), cypher_exists1(?p) => ?p`, [][]string{{"api"}}},
	{`MATCH (p:Package) WHERE NOT EXISTS { MATCH (p)-[:IMPORTS]->(:Package) } RETURN p`,
		"cypher_exists1(?p) :- pkg(?_n, _), imports(?p, ?_n);\n" +
			`pkg(?p, _), not cypher_exists1(?p) => ?p`, [][]string{{"util"}}},
	// COUNT { } is a body aggregate, so a node it never matches from counts 0 (util) rather than
	// dropping out, as a helper rule with a counting head would.
	{`MATCH (p:Package) RETURN p.name, COUNT { (p)-[:IMPORTS]->() } AS n`,
		`pkg(?p, _), ?_count = count(?_n) : { imports(?p, ?_n) } => ?p, ?_count`,
		[][]string{{"api", "1"}, {"app", "1"}, {"log", "1"}, {"util", "0"}}},
	{`MATCH (p:Package) WHERE COUNT { (p)<-[:IMPORTS]-() } = 0 RETURN p`,
		`pkg(?p, _), ?_count = count(?_n) : { imports(?_n, ?p) }, ?_count = 0 => ?p`, [][]string{{"app"}}},
	{`MATCH (p:Package) RETURN toUpper(p.name) AS up`,
		`pkg(?p, _), ?_c = str.upper(?p) => ?_c`, [][]string{{"API"}, {"APP"}, {"LOG"}, {"UTIL"}}},
	{`MATCH (p:Package) WHERE toLower(p.license) STARTS WITH 'apache' RETURN replace(p.license, '-', ' ') AS l, split(p.license, '-')[0] AS head`,
		`pkg(?p, ?p_license), ?_c = str.replace(?p_license, "-", " "), ?_c1 = str.split(?p_license, "-", 0), ?_c2 = str.lower(?p_license), str.prefix(?_c2, "apache") => ?_c, ?_c1`,
		[][]string{{"Apache 2.0", "Apache"}}},
	{`MATCH (p:Package) RETURN p.name + '!' AS loud`,
		`pkg(?p, _), ?_c = str.concat(?p, "!") => ?_c`, [][]string{{"api!"}, {"app!"}, {"log!"}, {"util!"}}},
	// MATCH-less WHERE text and a trailing semicolon, comments, backticks and keywords in any case.
	{"// a comment\nMatch (`p`:Package) /* and another */ Return p.name Order By p.name Desc Limit 1;",
		`pkg(?p, _) => ?p order by ?p desc limit 1`, [][]string{{"util"}}},
}

func TestCompile(t *testing.T) {
	v, b := guide.Deps(t)
	for _, c := range depsCases {
		check(t, v, b, depsGraph, c)
	}
}

func check(t *testing.T, v *ns.Vocabulary, b *datalog.Base, g cypher.Graph, c compileCase) {
	t.Helper()
	p, err := cypher.Compile(v, g, c.cypher)
	if err != nil {
		t.Errorf("%s: %v", c.cypher, err)
		return
	}
	if p.Text != c.text {
		t.Errorf("%s\nwrote:\n%s\nwant:\n%s", c.cypher, p.Text, c.text)
	}
	if err := datalog.Validate(p.Query, v); err != nil {
		t.Errorf("%s: %s doesn't check: %v", c.cypher, p.Text, err)
		return
	}
	rows, err := rowsOf(t, p, b)
	if err != nil {
		t.Errorf("%s: %v", c.cypher, err)
		return
	}
	if fmt.Sprintf("%q", rows) != fmt.Sprintf("%q", c.rows) {
		t.Errorf("%s (%s)\nanswered %q\nwant     %q", c.cypher, p.Text, rows, c.rows)
	}
}

// Over Declaire's shape, labels and types are constants in shared relations and properties are attr
// facts, read where they're tested: a property one side of an OR reads doesn't filter the other.
var codeCases = []compileCase{
	{`MATCH (f:Function {name: 'main'})-[:CALLS]->(g:Function) RETURN g.name`,
		`node(?f, "Function"), node(?g, "Function"), edge(?f, "CALLS", ?g), attr(?f, "name", "main"), attr(?g, "name", ?g_name) => ?g_name`,
		[][]string{{"help"}, {"parse"}}},
	{`MATCH (:File {name: 'a.go'})-[:CONTAINS]->(f)-[:CALLS*]->(g) RETURN DISTINCT g`,
		"cypher_reach1(?x, ?y) :- edge(?x, \"CALLS\", ?y);\ncypher_reach1(?x, ?y) :- cypher_reach1(?x, ?z), edge(?z, \"CALLS\", ?y);\n" +
			`node(?_n, "File"), edge(?_n, "CONTAINS", ?f), cypher_reach1(?f, ?g), attr(?_n, "name", "a.go") => ?g`,
		[][]string{{"f:help"}, {"f:lex"}, {"f:parse"}}},
	{`MATCH (f:Function) WHERE f.doc IS NULL RETURN f`,
		`node(?f, "Function"), not attr(?f, "doc", _) => ?f`, [][]string{{"f:help"}, {"f:lex"}, {"f:parse"}}},
	{`MATCH (f:Function) WHERE f.doc IS NOT NULL OR f.name = 'lex' RETURN f`,
		"cypher_match1(?f) :- node(?f, \"Function\"), attr(?f, \"doc\", _);\ncypher_match1(?f) :- node(?f, \"Function\"), attr(?f, \"name\", ?f_name), ?f_name = \"lex\";\n" +
			`cypher_match1(?f) => ?f`, [][]string{{"f:lex"}, {"f:main"}}},
	{`MATCH (a)-->(b:Function {name: 'parse'}) RETURN a`,
		"cypher_step1(?x, ?y) :- edge(?x, \"CALLS\", ?y);\ncypher_step1(?x, ?y) :- edge(?x, \"CONTAINS\", ?y);\n" +
			`node(?b, "Function"), cypher_step1(?a, ?b), attr(?b, "name", "parse") => ?a`,
		[][]string{{"f:help"}, {"f:main"}, {"file:a.go"}}},
	{`MATCH (f:Function) WHERE NOT (f)-[:CALLS]->() AND NOT ()-[:CALLS]->(f) RETURN f`,
		"cypher_exists1(?f) :- edge(?f, \"CALLS\", ?_n);\ncypher_exists2(?f) :- edge(?_n1, \"CALLS\", ?f);\n" +
			`node(?f, "Function"), not cypher_exists1(?f), not cypher_exists2(?f) => ?f`, nil},
	{`MATCH (f:Function)-[:CALLS]->(g) WITH f, count(g) AS n WHERE n > 1 RETURN f.name`,
		"cypher_with1(?f, count(?g)) :- node(?f, \"Function\"), edge(?f, \"CALLS\", ?g);\n" +
			`cypher_with1(?f, ?n), attr(?f, "name", ?f_name), ?n > 1 => ?f_name`, [][]string{{"main"}}},
	{`MATCH (f:Function) WHERE COUNT { (f)<-[:CALLS]-() } = 2 RETURN f`,
		`node(?f, "Function"), ?_count = count(?_n) : { edge(?_n, "CALLS", ?f) }, ?_count = 2 => ?f`, [][]string{{"f:parse"}}},
	{`MATCH (f:Function)-[:CALLS]->(g) RETURN f, count(g) AS calls ORDER BY calls DESC, f LIMIT 1`,
		`node(?f, "Function"), edge(?f, "CALLS", ?g) => ?f, count(?g) order by count(?g) desc, ?f limit 1`, [][]string{{"f:main", "2"}}},
}

func TestCompileOverGenericRelations(t *testing.T) {
	v, b, g := codeBase(t)
	for _, c := range codeCases {
		check(t, v, b, g, c)
	}
}

// A $parameter is a goal variable the host binds, so one compiled program answers for any value.
func TestAParameterIsBound(t *testing.T) {
	v, b := guide.Deps(t)
	p, err := cypher.Compile(v, depsGraph, `MATCH (p:Package {name: $pkg})-[:IMPORTS]->(d) RETURN d`)
	if err != nil {
		t.Fatal(err)
	}
	if p.Text != `pkg(?pkg, _), imports(?pkg, ?d) => ?d` || p.Params["pkg"] != "pkg" {
		t.Fatalf("wrote %s, params %v", p.Text, p.Params)
	}
	for pkg, want := range map[string]string{"app": "[[api]]", "log": "[[util]]", "util": "[]"} {
		bind, err := p.Bind(map[string]ns.Value{"pkg": ns.S(pkg)})
		if err != nil {
			t.Fatal(err)
		}
		rows, err := rowsOf(t, p, b, bind)
		if err != nil || fmt.Sprint(rows) != want {
			t.Errorf("$pkg = %s: %v, %v; want %s", pkg, rows, err, want)
		}
	}
	if _, err := p.Bind(map[string]ns.Value{"other": ns.S("x")}); err == nil {
		t.Error("bound a parameter the query doesn't have")
	}
	// A parameter Cypher names like a variable gets a name of its own.
	p, err = cypher.Compile(v, depsGraph, `MATCH (d:Package) WHERE d.license = $d RETURN d`)
	if err != nil || p.Text != `pkg(?d, ?p_d) => ?d` || p.Params["d"] != "p_d" {
		t.Errorf("wrote %s, params %v, %v", p.Text, p.Params, err)
	}
}

// Each construct outside the subset is refused where it's written, naming the jaala issue that would
// let it, so a model and the mission's queue both know what it hit.
func TestGapsAreRefusedByName(t *testing.T) {
	v, _ := guide.Deps(t)
	for _, c := range []struct {
		cypher string
		gap    int
		at     string // the text the error's position starts at
	}{
		{`MATCH (a)-[r:IMPORTS]->(b) RETURN r, a`, 279, "r, a"},
		{`MATCH (a)-[r:IMPORTS]->(b) RETURN type(r)`, 279, "type(r)"},
		{`MATCH p = (a)-[:IMPORTS]->(b) RETURN p`, 280, "p ="},
		{`MATCH (a)-[r:IMPORTS*]->(b) RETURN a`, 280, "-[r"},
		{`MATCH (a)-[:IMPORTS]->(b) RETURN length(a)`, 280, "length"},
		{`MATCH (a), (b) RETURN shortestPath((a)-[*]->(b))`, 281, "shortestPath"},
		{`MATCH (a)-[:IMPORTS*0..2]->(b) RETURN b`, 281, "-[:IMPORTS*0"},
		{`MATCH (a)-[:IMPORTS*1..20]->(b) RETURN b`, 281, "-[:IMPORTS*1"},
		{`OPTIONAL MATCH (a:Package) RETURN a`, 282, "OPTIONAL"},
		{`MATCH (a:Package) OPTIONAL MATCH (a)-[:IMPORTS]->(b) RETURN a, b`, 282, "OPTIONAL"},
		{`MATCH (a:Package) UNWIND [1, 2] AS x RETURN x`, 283, "UNWIND"},
		{`MATCH (a:Package) WHERE a.name IN $names RETURN a`, 283, "IN $names"},
		{`MATCH (a:Package) RETURN a {.name}`, 283, "{.name}"},
		{`MATCH (a:Package) RETURN labels(a)`, 283, "labels"},
		{`MATCH (a:Package) RETURN avg(a.license)`, 284, "avg"},
		{`MATCH (a:Package) RETURN trim(a.name)`, 284, "trim"},
		{`MATCH (a:Package) RETURN split(a.name, '-')`, 283, "split"},
		{`MATCH (a:Package) WHERE size(COLLECT { (a)-->(b) }) > 1 RETURN a`, 283, "COLLECT"},
		{`MATCH (a:Package) RETURN CASE WHEN a.license IS NULL THEN 'none' END`, 284, "CASE"},
		{`MATCH (a:Package) SET a.license = 'MIT' RETURN a`, 287, "SET"},
		{`CREATE (a:Package {name: 'x'}) RETURN a`, 287, "CREATE"},
		{`USE other MATCH (a) RETURN a`, 289, "USE"},
		{`MATCH (a:Package) WITH a ORDER BY a LIMIT 1 RETURN a`, 364, "WITH a ORDER"},
		{`MATCH (a:Package) RETURN a.name AS n UNION MATCH (b:Package) RETURN b.name AS n LIMIT 1`, 364, "RETURN b.name"},
		{`CALL { MATCH (a:Package) RETURN a } RETURN a`, 364, "CALL {"},
		{`MATCH (a:Package) RETURN a LIMIT $n`, 364, "$n"},
		{`RETURN 1`, 364, "RETURN"},
		{`MATCH (a:Package) WHERE EXISTS { MATCH (a)-->(b) RETURN b } RETURN a`, 364, "RETURN b }"},
	} {
		_, err := cypher.Compile(v, depsGraph, c.cypher)
		var ce *cypher.Error
		if !asError(err, &ce) || ce.Gap != c.gap {
			t.Errorf("%s: %v, want a refusal naming #%d", c.cypher, err, c.gap)
			continue
		}
		if !strings.HasPrefix(c.cypher[ce.Pos.Offset:], c.at) {
			t.Errorf("%s: refused at %q, want at %q", c.cypher, c.cypher[ce.Pos.Offset:], c.at)
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("jaala#%d", c.gap)) {
			t.Errorf("%s: %q doesn't name the issue", c.cypher, err)
		}
	}
}

// A mistake against the text or the view is an error with no gap, and says what the view does hold.
func TestMistakesSayWhatTheGraphHolds(t *testing.T) {
	v, _ := guide.Deps(t)
	for _, c := range []struct{ cypher, want, at string }{
		{`MATCH (p:Package {name: 'api'}) RETURN p.maintainer`, "no property maintainer on p:Package (its properties: license, name)", "p.maintainer"},
		{`MATCH (p:Pkg) RETURN p`, "no label Pkg in this graph (its labels: Package)", "(p:Pkg"},
		{`MATCH (a)-[:IMPORT]->(b) RETURN b`, "no relationship type IMPORT in this graph, did you mean \"IMPORTS\"? (its types: IMPORTS, DEPENDS_ON)", "-[:IMPORT]"},
		{`MATCH (a:Package) RETURN b`, "unknown variable b", "b"},
		{`MATCH (a:Package) RETURN a.name ORDER BY a.license`, "ORDER BY has to name a column RETURN writes", "a.license"},
		{`MATCH (a:Package) RETURN a SKIP 1`, "SKIP needs an ORDER BY", "RETURN"},
		{`MATCH (a:Package) RETURN a LIMIT 0`, "LIMIT 0 answers nothing", "0"},
		{`MATCH (a:Package) WHERE a.license = null RETURN a`, "write IS NULL", "null"},
		{`MATCH (a:Package) WHERE a.license = true RETURN a`, "no booleans", "true"},
		{`MATCH (a:Package RETURN a`, "expected ) to end the node", "RETURN"},
		{`MATCH (a:Package) RETURN 'x'`, "RETURN of a constant", "'x'"},
		{`MATCH (a:Package) RETURN a extra`, `unexpected "extra"`, "extra"},
		{`MATCH (p:Package) WHERE p.name = $x OR p.name = 'log' RETURN p`, "which datalog.Bind can't reach: compile with cypher.Params", "$x"},
		{`MATCH (p:Package {name: $x}) WHERE p.license = 'MIT' OR p.license = '' RETURN p.license`, "which datalog.Bind can't reach", "$x"},
		{`MATCH (p:Package {name: $x})-[:IMPORTS]->(d) WITH d RETURN d`, "which datalog.Bind can't reach", "$x"},
		{`MATCH (a:Package) RETURN a UNION MATCH (b:Package) RETURN b`, "every UNION part returns the same columns: a, not b", "MATCH (b"},
		{`MATCH (a:Package) WITH a.name RETURN a`, "WITH a.name needs a name", "a.name RETURN"},
		{`MATCH (a:Package) WITH a.name AS n RETURN n.license`, "n is a value, not a node", "n.license"},
		{`MATCH (a:Package) CALL str.split(a.name, '-') YIELD piece RETURN piece`, "str.split yields index, part, not piece", "piece RETURN"},
		{`MATCH (a:Package) CALL nothing.here(a) YIELD x RETURN x`, "no procedure nothing.here", "CALL"},
		{`MATCH (a:Package) CALL str.split() YIELD part RETURN part`, "CALL str.split needs its string, separator as arguments", "CALL"},
		{`MATCH (a:Package) RETURN COUNT { (a)-->(b) WHERE b.name = 'x' OR b.name = 'y' } AS n`, "COUNT { } over a WHERE with OR", "COUNT"},
		{`MATCH (a:Package) WHERE a.license = 'say "hi"' RETURN a`, "double quote", "'say"},
	} {
		_, err := cypher.Compile(v, depsGraph, c.cypher)
		var ce *cypher.Error
		if !asError(err, &ce) || ce.Gap != 0 || !strings.Contains(ce.Msg, c.want) {
			t.Errorf("%s: %v, want %q", c.cypher, err, c.want)
			continue
		}
		if !strings.HasPrefix(c.cypher[ce.Pos.Offset:], c.at) {
			t.Errorf("%s: at %q, want at %q", c.cypher, c.cypher[ce.Pos.Offset:], c.at)
		}
	}
}

func asError(err error, target **cypher.Error) bool {
	ce, ok := err.(*cypher.Error)
	if ok {
		*target = ce
	}
	return ok
}

// The helper names Compile writes stay clear of the vocabulary's own.
func TestHelpersAvoidTheVocabularysNames(t *testing.T) {
	v, b := guide.Deps(t)
	if err := v.AddModule("cypher_reach1", datalog.LanguageName, "x(?a) :- pkg(?a, _);", ""); err != nil {
		t.Fatal(err)
	}
	_ = b
	p, err := cypher.Compile(v, depsGraph, `MATCH (a {name: 'app'})-[:IMPORTS*]->(d) RETURN d`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Text, "cypher_reach2(") || strings.Contains(p.Text, "cypher_reach1(") {
		t.Errorf("wrote %s", p.Text)
	}
	if !slices.Contains(p.Columns, "d") {
		t.Errorf("columns %v", p.Columns)
	}
}

// Params writes a parameter's value in as a constant, so it reaches a WITH stage, a UNION part or an
// OR's rules, where Bind can't. control: in the goal alone it answers as Bind does.
func TestParamsWriteValuesIntoEveryRule(t *testing.T) {
	v, b := guide.Deps(t)
	vals := map[string]ns.Value{"pkg": ns.S("api")}
	q := `MATCH (p:Package {name: $pkg})-[:IMPORTS*]->(d) RETURN d`
	bound, err := cypher.Compile(v, depsGraph, q)
	if err != nil {
		t.Fatal(err)
	}
	bind, err := bound.Bind(vals)
	if err != nil {
		t.Fatal(err)
	}
	viaBind, err := rowsOf(t, bound, b, bind)
	if err != nil {
		t.Fatal(err)
	}
	written, err := cypher.Compile(v, depsGraph, q, cypher.Params(vals))
	if err != nil {
		t.Fatal(err)
	}
	viaParams, err := rowsOf(t, written, b)
	if err != nil || fmt.Sprint(viaParams) != fmt.Sprint(viaBind) || fmt.Sprint(viaBind) != "[[log] [util]]" {
		t.Errorf("control: Params answered %v (%v), Bind %v", viaParams, err, viaBind)
	}
	if len(written.Params) != 0 || strings.Contains(written.Text, "?pkg") {
		t.Errorf("Params left a variable: %s, %v", written.Text, written.Params)
	}
	for _, c := range []struct {
		cypher string
		vals   map[string]ns.Value
		rows   string
	}{
		{`MATCH (p:Package {name: $pkg})-[:IMPORTS*]->(d) WITH d RETURN d`, vals, "[[log] [util]]"},
		{`MATCH (p:Package) WHERE p.name = $a OR p.name = $b RETURN p`, map[string]ns.Value{"a": ns.S("app"), "b": ns.S("log")}, "[[app] [log]]"},
		{`MATCH (p:Package) RETURN p.name AS n UNION MATCH (p:Package {license: $l}) RETURN p.name AS n`, map[string]ns.Value{"l": ns.S("MIT")}, "[[api] [app] [log] [util]]"},
		{`MATCH (a:Package)-[:IMPORTS*]->(d) WITH a, count(d) AS n WHERE n > $min RETURN a`, map[string]ns.Value{"min": ns.N(2)}, "[[app]]"},
	} {
		p, err := cypher.Compile(v, depsGraph, c.cypher, cypher.Params(c.vals))
		if err != nil {
			t.Errorf("%s: %v", c.cypher, err)
			continue
		}
		rows, err := rowsOf(t, p, b)
		if err != nil || fmt.Sprint(rows) != c.rows {
			t.Errorf("%s (%s): %v, %v; want %s", c.cypher, p.Text, rows, err, c.rows)
		}
	}
	for _, c := range []struct {
		vals map[string]ns.Value
		want string
	}{
		{map[string]ns.Value{}, "no value for $pkg"},
		{map[string]ns.Value{"pkg": ns.Absent()}, "$pkg is absent"},
		{map[string]ns.Value{"pkg": ns.S(`say "hi"`)}, "double quote"},
	} {
		if _, err := cypher.Compile(v, depsGraph, q, cypher.Params(c.vals)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.vals, err, c.want)
		}
	}
}

// A + joins text when a side is text: a string, a string function, or a property its schema types
// as text; otherwise it adds. control: the same + over two number columns adds.
func TestPlusJoinsTextAndAddsNumbers(t *testing.T) {
	src := ns.NewMemSource().DeclareSchema("file", ns.Schema{Arity: 3, Labels: []string{"path", "ext", "lines"},
		Types: []ns.ArgType{{Type: ns.TypeString}, {Type: ns.TypeString}, {Type: ns.TypeNumber}}})
	src.Add("file", ns.Tuple{Vals: []ns.Value{ns.S("main"), ns.S(".go"), ns.N(12)}})
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
	g := cypher.Graph{Nodes: []cypher.Node{{Label: "File", Relation: "file", Key: 0, Props: map[string]int{"path": 0, "ext": 1, "lines": 2}}}}
	for _, c := range []compileCase{
		{`MATCH (f:File) RETURN f.path + f.ext AS name`, `file(?f, ?f_ext, _), ?_c = str.concat(?f, ?f_ext) => ?_c`, [][]string{{"main.go"}}},
		{`MATCH (f:File) RETURN f.lines + f.lines AS twice`, `file(?f, _, ?f_lines), ?_c = (?f_lines + ?f_lines) => ?_c`, [][]string{{"24"}}},
		{`MATCH (f:File) RETURN toUpper(f.path) + f.ext AS name`, `file(?f, ?f_ext, _), ?_c = str.concat(str.upper(?f), ?f_ext) => ?_c`, [][]string{{"MAIN.go"}}},
	} {
		check(t, v, b, g, c)
	}
}

// A WHERE whose alternatives pass maxDisjuncts is refused when a part of it does, not after the
// whole disjunction is built: a chain of XORs doubled it per XOR and never finished (#377).
func TestAWhereOfTooManyAlternativesIsRefusedQuickly(t *testing.T) {
	v, _ := guide.Deps(t)
	compile := func(q string) error {
		done := make(chan error, 1)
		go func() {
			_, err := cypher.Compile(v, depsGraph, q)
			done <- err
		}()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: still compiling after 10s", q)
			return nil
		}
	}
	control := `MATCH (a:Package) WHERE a.license = 'x' XOR a.license = 'y' XOR a.name = 'z' RETURN a`
	if err := compile(control); err != nil {
		t.Fatalf("control: %s: %v", control, err)
	}
	for _, q := range []string{
		"MATCH (a:Package) WHERE a.license = 'x'" + strings.Repeat(" XOR a.license = 'y'", 64) + " RETURN a",
		"MATCH (a:Package) WHERE a.name = 'q'" + strings.Repeat(" AND (a.license = 'x' OR a.name = 'y')", 20) + " RETURN a",
		`MATCH(a)WHERE()--()XOR(0)XOR(0)XOR 0XOR 0XOR 0XOR[]XOR []XOR 0RETURN a`,
	} {
		err := compile(q)
		if err == nil || !strings.Contains(err.Error(), "expands to more than 16 alternatives") {
			t.Errorf("%s: want the alternatives refusal, got %v", q, err)
		}
	}
}

// calls is x10b's shape (#392): 300 chains of ten functions, each chain's last calling one of a
// hundred methods, over Declaire's node, edge and attr with every node's id a property.
func calls(t *testing.T) (*ns.Vocabulary, *datalog.Base, cypher.Graph) {
	t.Helper()
	src := ns.NewMemSource().
		DeclareSchema("node", ns.Schema{Arity: 2, Labels: []string{"id", "kind"}}).
		DeclareSchema("edge", ns.Schema{Arity: 3, Labels: []string{"from", "kind", "to"}}).
		DeclareSchema("attr", ns.Schema{Arity: 3, Labels: []string{"id", "key", "value"}})
	add := func(rel string, vals ...string) {
		tu := ns.Tuple{}
		for _, v := range vals {
			tu.Vals = append(tu.Vals, ns.S(v))
		}
		src.Add(rel, tu)
	}
	for m := 0; m < 100; m++ {
		add("node", fmt.Sprintf("m%d", m), "Method")
	}
	for c := 0; c < 300; c++ {
		for i := 0; i < 10; i++ {
			f := fmt.Sprintf("f%d_%d", c, i)
			add("node", f, "Func")
			if i < 9 {
				add("edge", f, "CALLS", fmt.Sprintf("f%d_%d", c, i+1))
			} else {
				add("edge", f, "CALLS", fmt.Sprintf("m%d", c%100))
			}
		}
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
	g := cypher.Store{Node: "node", Edge: "edge", Attr: "attr"}.Graph([]string{"Func", "Method"}, []string{"CALLS"})
	for i := range g.Nodes {
		g.Nodes[i].Props = map[string]int{"id": 0}
	}
	return v, b, g
}

// A variable-length match to an IN list walks back from the listed nodes, as one to each node does
// (#392), where it derived the reach over the whole graph: Declaire's x10b ran into its ten-minute cap.
// The rows are the union of the per-node questions', and each of them is asked under the same budget.
// control: the walk to every method doesn't fit the budget.
func TestAWalkToAnINListStartsFromTheList(t *testing.T) {
	const budget = 3000
	v, _, g := calls(t)
	run := func(text string, opts ...datalog.Option) ([][]string, error) {
		t.Helper()
		p, err := cypher.Compile(v, g, text)
		if err != nil {
			t.Fatal(err)
		}
		_, b, _ := calls(t)
		return rowsOf(t, p, b, opts...)
	}
	planned := func(text string) error {
		t.Helper()
		p, err := cypher.Compile(v, g, text)
		if err != nil {
			t.Fatal(err)
		}
		_, b, _ := calls(t)
		_, err = datalog.SemiNaive{}.Eval(context.Background(), p.Query, b, datalog.Budget(budget))
		return err
	}
	for _, hops := range []string{"*1..6", "*"} {
		in := fmt.Sprintf(`MATCH (t:Func)-[:CALLS%s]->(h:Method) WHERE h.id IN ['m1', 'm2', 'm3'] RETURN t, h ORDER BY t`, hops)
		want, err := run(in)
		if err != nil || len(want) == 0 {
			t.Fatalf("%s: %d rows, %v", in, len(want), err)
		}
		var union [][]string
		for _, id := range []string{"m1", "m2", "m3"} {
			one := fmt.Sprintf(`MATCH (t:Func)-[:CALLS%s]->(h:Method {id: '%s'}) RETURN t, h`, hops, id)
			rows, err := run(one)
			if err != nil {
				t.Fatalf("%s: %v", one, err)
			}
			union = append(union, rows...)
			if err := planned(one); err != nil {
				t.Errorf("%s: %v", one, err)
			}
		}
		slices.SortFunc(union, func(a, b []string) int { return strings.Compare(a[0], b[0]) })
		if fmt.Sprint(union) != fmt.Sprint(want) {
			t.Errorf("%s answered %v, want the per-node questions' %v", in, want, union)
		}
		if err := planned(in); err != nil {
			t.Errorf("%s: %v", in, err)
		}
	}
	var stopped *datalog.BudgetExceeded
	if err := planned(`MATCH (t:Func)-[:CALLS*]->(h:Method) RETURN t, h`); !errors.As(err, &stopped) {
		t.Errorf("control: the walk to every method answered under a budget of %d (%v), so the budget can't tell", budget, err)
	}
}
