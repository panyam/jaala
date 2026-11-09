// Package guide is the guide's example in miniature, for the front ends' tests: a vocabulary of
// packages and their imports, and the questions both front ends' TestEval ask of it, so the two
// languages are scored on the same cases. Only tests import it, and it imports no front end, so the
// front ends can be modules of their own without the root depending on one.
package guide

import (
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

// Deps is app importing api, api importing log, log importing util, four packages' licenses (api's
// absent, log's empty), and the deps module reaching through imports.
func Deps(t testing.TB) (*ns.Vocabulary, *datalog.Base) {
	t.Helper()
	src := Source()
	v := Vocabulary(t, src)
	b, err := datalog.NewBase(v, src)
	if err != nil {
		t.Fatal(err)
	}
	return v, b
}

// Source is Deps' facts.
func Source() *ns.MemSource {
	src := ns.NewMemSource().
		DeclareSchema("imports", ns.Schema{Arity: 2, Labels: []string{"from", "to"}, Doc: "A package importing another."}).
		DeclareSchema("pkg", ns.Schema{Arity: 2, Labels: []string{"name", "license"}, Doc: "Every package and its license."})
	for _, e := range [][2]string{{"app", "api"}, {"api", "log"}, {"log", "util"}} {
		src.Add("imports", ns.Tuple{Vals: []ns.Value{ns.S(e[0]), ns.S(e[1])}})
	}
	src.Add("pkg", ns.Tuple{Vals: []ns.Value{ns.S("app"), ns.S("MIT")}})
	src.Add("pkg", ns.Tuple{Vals: []ns.Value{ns.S("api"), ns.Absent()}})
	src.Add("pkg", ns.Tuple{Vals: []ns.Value{ns.S("log"), ns.S("")}})
	src.Add("pkg", ns.Tuple{Vals: []ns.Value{ns.S("util"), ns.S("Apache-2.0")}})
	return src
}

// Vocabulary is Deps' vocabulary over src, which serves Source's relations: stdlib and the deps
// module, so a store holding the same facts answers the guide's questions too.
func Vocabulary(t testing.TB, src ns.Source) *ns.Vocabulary {
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
	if err := v.AddModule("deps", datalog.LanguageName, `# Everything a package depends on, directly or through others.
depends_on(?a, ?b) :- _step(?a, ?b);
depends_on(?a, ?c) :- depends_on(?a, ?b), _step(?b, ?c);
_step(?a, ?b) :- imports(?a, ?b);
`, ""); err != nil {
		t.Fatal(err)
	}
	return v
}

// A Case is a question and the rows a right program answers, as nl.Case holds them.
type Case struct {
	Name, Question string
	Want           [][]string
}

// Cases are questions as the guide asks them, with the rows a right program answers.
var Cases = []Case{
	{Name: "direct", Question: "what does app import directly?", Want: [][]string{{"api"}}},
	{Name: "transitive", Question: "what does app depend on, directly or not?", Want: [][]string{{"api"}, {"log"}, {"util"}}},
	{Name: "license", Question: "what license does util have?", Want: [][]string{{"Apache-2.0"}}},
	{Name: "unstated", Question: "which packages have no license stated?", Want: [][]string{{"api"}}},
	{Name: "count", Question: "how many packages does app depend on?", Want: [][]string{{"3"}}},
	{Name: "reverse", Question: "what imports util?", Want: [][]string{{"log"}}},
	{Name: "columns", Question: "list every package with its license", Want: [][]string{{"app", "MIT"}, {"api", "absent"}, {"log", ""}, {"util", "Apache-2.0"}}},
	{Name: "missing", Question: "who maintains api?", Want: [][]string{{"someone"}}},
}

// CypherDrafts are the Cypher a scripted model writes for the guide's questions (guide.Cases), the ones
// the drafting arm's TestEval asks in jaala's syntax (nlmodels/translator), in the order it writes them: a right query for each,
// after a misspelled label for "transitive", which the refusal's did-you-mean fixes, and for "missing",
// the property the graph doesn't have, which Compile refuses and the model writes again.
var CypherDrafts = map[string][]string{
	"what does app import directly?":            {`MATCH (:Package {name: 'app'})-[:IMPORTS]->(d) RETURN d.name`},
	"what does app depend on, directly or not?": {`MATCH (:Pakage {name: 'app'})-[:IMPORTS*]->(d) RETURN DISTINCT d.name`, `MATCH (:Package {name: 'app'})-[:IMPORTS*]->(d) RETURN DISTINCT d.name`},
	"what license does util have?":              {`MATCH (p:Package {name: 'util'}) RETURN p.license`},
	"which packages have no license stated?":    {`MATCH (p:Package) WHERE p.license IS NULL RETURN p.name`},
	"how many packages does app depend on?":     {`MATCH (:Package {name: 'app'})-[:IMPORTS*]->(d) RETURN count(DISTINCT d)`},
	"what imports util?":                        {`MATCH (x:Package)-[:IMPORTS]->(:Package {name: 'util'}) RETURN x.name`},
	"list every package with its license":       {`MATCH (p:Package) RETURN p.name, p.license`},
	"who maintains api?":                        {`MATCH (p:Package {name: 'api'}) RETURN p.maintainer`, `MATCH (p:Package {name: 'api'}) RETURN p.maintainer`},
}

// frontends/cypher's FuzzCompile seeds from them, and nlmodels/cypherarm's TestEval writes them.

// Code is a small graph laid out as Declaire lays out its own: node(id, kind), attr(id, key,
// value), and an edge relation per kind. goapp's Auth contains a callsite whose callee is oneauth's
// Get, and calls holds no edge across the two repos, as Declaire's doesn't. TestParse reaches
// oneauth's Parse through a helper, two calls away. Its shape for nl.Paths is node for kinds, calls
// and contains for edges, and attr's callee for a ref (#514 piece 5).
func Code(t *testing.T) *datalog.Base {
	t.Helper()
	src := ns.NewMemSource().
		DeclareSchema("node", ns.Schema{Arity: 2, Labels: []string{"id", "kind"}, Types: []ns.ArgType{{KindFrom: "kind"}, {Type: ns.TypeString}}}).
		Declare("attr", "id", "key", "value").
		Declare("contains", "src", "dst").
		Declare("calls", "src", "dst")
	add := func(rel string, vals ...string) {
		vs := make([]ns.Value, len(vals))
		for i, s := range vals {
			vs[i] = ns.S(s)
		}
		src.Add(rel, ns.Tuple{Vals: vs})
	}
	for _, n := range [][2]string{
		{"goapp/svc.Auth", "func"}, {"goapp/svc.Other", "func"}, {"goapp/svc.Auth#1", "callsite"}, {"goapp/svc.Other#1", "callsite"},
		{"oneauth/acct.Get", "func"}, {"oneauth/acct.Save", "func"},
		{"oneauth/api.TestParse", "func"}, {"oneauth/api.helper", "func"}, {"oneauth/api.Parse", "func"},
	} {
		add("node", n[0], n[1])
	}
	add("contains", "goapp/svc.Auth", "goapp/svc.Auth#1")
	add("contains", "goapp/svc.Other", "goapp/svc.Other#1")
	add("attr", "goapp/svc.Auth#1", "callee", "oneauth/acct.Get")
	add("attr", "goapp/svc.Other#1", "callee", "goapp/svc.Auth")
	add("attr", "oneauth/api.TestParse", "role", "test")
	add("calls", "goapp/svc.Other", "goapp/svc.Auth")
	add("calls", "oneauth/api.TestParse", "oneauth/api.helper")
	add("calls", "oneauth/api.helper", "oneauth/api.Parse")
	add("calls", "oneauth/acct.Save", "oneauth/acct.Get")
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
	return datalog.MustBase(v, src)
}
