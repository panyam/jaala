// Package guide is the guide's example in miniature, for the front ends' tests: a vocabulary of
// packages and their imports, and the questions both front ends' TestEval ask of it, so the two
// languages are scored on the same cases. Only tests import it.
package guide

import (
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

// Deps is app importing api, api importing log, log importing util, four packages' licenses (api's
// absent, log's empty), and the deps module reaching through imports.
func Deps(t testing.TB) (*ns.Vocabulary, *datalog.Base) {
	t.Helper()
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
	b, err := datalog.NewBase(v, src)
	if err != nil {
		t.Fatal(err)
	}
	return v, b
}

// Cases are questions as the guide asks them, with the rows a right program answers.
var Cases = []nl.Case{
	{Name: "direct", Question: "what does app import directly?", Want: [][]string{{"api"}}},
	{Name: "transitive", Question: "what does app depend on, directly or not?", Want: [][]string{{"api"}, {"log"}, {"util"}}},
	{Name: "license", Question: "what license does util have?", Want: [][]string{{"Apache-2.0"}}},
	{Name: "unstated", Question: "which packages have no license stated?", Want: [][]string{{"api"}}},
	{Name: "count", Question: "how many packages does app depend on?", Want: [][]string{{"3"}}},
	{Name: "reverse", Question: "what imports util?", Want: [][]string{{"log"}}},
	{Name: "columns", Question: "list every package with its license", Want: [][]string{{"app", "MIT"}, {"api", "absent"}, {"log", ""}, {"util", "Apache-2.0"}}},
	{Name: "missing", Question: "who maintains api?", Want: [][]string{{"someone"}}},
}
