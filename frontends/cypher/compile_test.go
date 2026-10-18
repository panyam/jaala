package cypher_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/internal/guide"
	"github.com/panyam/jaala/ns"
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
	{`MATCH (p:Package) RETURN p.name AS name ORDER BY name SKIP 1 LIMIT 2`,
		`pkg(?p, _) => ?p order by ?p limit 2 offset 1`, [][]string{{"app"}, {"log"}}},
	{`MATCH (p:Package) RETURN collect(p.name)`,
		`pkg(?p, _) => list(?p)`, [][]string{{"api app log util"}}},
	{`MATCH (p:Package) WHERE p.license = 'MIT' OR p.license STARTS WITH 'Apache' RETURN p.name`,
		"cypher_match1(?p) :- pkg(?p, ?p_license), ?p_license = \"MIT\";\ncypher_match1(?p) :- pkg(?p, ?p_license), str.prefix(?p_license, \"Apache\");\n" +
			`cypher_match1(?p) => ?p`, [][]string{{"app"}, {"util"}}},
	{`MATCH (p:Package) WHERE NOT (p.name = 'app' OR p.license IS NULL) RETURN p`,
		`pkg(?p, ?p_license), ?p != "app", not absent(?p_license) => ?p`, [][]string{{"log"}, {"util"}}},
	{`MATCH (p:Package) WHERE p.name IN ['app', 'log'] RETURN p`,
		"cypher_match1(?p) :- pkg(?p, _), ?p = \"app\";\ncypher_match1(?p) :- pkg(?p, _), ?p = \"log\";\n" +
			`cypher_match1(?p) => ?p`, [][]string{{"app"}, {"log"}}},
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
		{`MATCH (a:Package) RETURN toUpper(a.name)`, 284, "toUpper"},
		{`MATCH (a:Package) RETURN a.name + '!'`, 284, "+ '!'"},
		{`MATCH (a:Package) RETURN CASE WHEN a.license IS NULL THEN 'none' END`, 284, "CASE"},
		{`MATCH (a:Package) SET a.license = 'MIT' RETURN a`, 287, "SET"},
		{`CREATE (a:Package {name: 'x'}) RETURN a`, 287, "CREATE"},
		{`USE other MATCH (a) RETURN a`, 289, "USE"},
		{`MATCH (a:Package) WITH a RETURN a`, 352, "WITH"},
		{`MATCH (a:Package) RETURN a UNION MATCH (b:Package) RETURN b`, 352, "UNION"},
		{`MATCH (a:Package) WHERE EXISTS { (a)-->() } RETURN a`, 352, "EXISTS"},
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
		{`MATCH (a)-[:IMPORT]->(b) RETURN b`, "no relationship type IMPORT in this graph (its types: IMPORTS, DEPENDS_ON)", "-[:IMPORT]"},
		{`MATCH (a:Package) RETURN b`, "unknown variable b", "b"},
		{`MATCH (a:Package) RETURN a.name ORDER BY a.license`, "ORDER BY has to name a column RETURN writes", "a.license"},
		{`MATCH (a:Package) RETURN a SKIP 1`, "SKIP needs an ORDER BY", "RETURN"},
		{`MATCH (a:Package) WHERE a.license = null RETURN a`, "write IS NULL", "null"},
		{`MATCH (a:Package) WHERE a.license = true RETURN a`, "no booleans", "true"},
		{`MATCH (a:Package) RETURN a LIMIT $n`, "not a parameter", "$n"},
		{`MATCH (a:Package RETURN a`, "expected ) to end the node", "RETURN"},
		{`MATCH (a:Package) RETURN 'x'`, "RETURN of a constant", "'x'"},
		{`RETURN 1`, "expected MATCH", "RETURN"},
		{`MATCH (a:Package) RETURN a extra`, `unexpected "extra"`, "extra"},
		{`MATCH (p:Package) WHERE p.name = $x OR p.name = 'log' RETURN p`, "$x is read inside an OR", "$x"},
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
