package nl_test

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

// codeGraph is a small graph laid out as Declaire lays out its own: node(id, kind), attr(id, key,
// value), and an edge relation per kind. goapp's Auth contains a callsite whose callee is oneauth's
// Get, and calls holds no edge across the two repos, as Declaire's doesn't. TestParse reaches
// oneauth's Parse through a helper, two calls away.
func codeGraph(t *testing.T) (*datalog.Base, nl.Shape) {
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
	return datalog.MustBase(v, src), nl.Shape{Kinds: "node", Edges: []string{"calls", "contains"}, Refs: []nl.Ref{{Relation: "attr", Key: "callee"}}}
}

// KindGraph reads from the data which kinds each edge and ref joins, with counts.
func TestKindGraphReadsTheKindsEachEdgeJoins(t *testing.T) {
	b, shape := codeGraph(t)
	links, err := nl.KindGraph(context.Background(), b, shape)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range links {
		got = append(got, l.Relation+"/"+l.Key+" "+l.From+"->"+l.To)
	}
	want := "attr/callee callsite->func, calls/ func->func, contains/ func->callsite"
	if strings.Join(got, ", ") != want {
		t.Errorf("links %v, want %s", got, want)
	}
	if links[1].Count != 4 {
		t.Errorf("calls joined func to func %d times, want 4", links[1].Count)
	}
}

// x05's shape: which oneauth functions goapp calls. calls holds no edge across the repos, so its
// one-hop path answers nothing and is dropped; the path through a callsite and its callee attr
// answers first, being shortest, and a longer one through a caller of Auth follows it. control: the
// same ends within one repo find calls itself.
func TestPathsFindTheJoinBetweenTwoSets(t *testing.T) {
	b, shape := codeGraph(t)
	ctx := context.Background()
	links, err := nl.KindGraph(ctx, b, shape)
	if err != nil {
		t.Fatal(err)
	}
	from := nl.End{Kind: "func", Where: `str.glob(?x, "goapp/*")`}
	to := nl.End{Kind: "func", Where: `str.glob(?x, "oneauth/*")`}
	paths, err := nl.Paths(ctx, b, shape, links, from, to, nl.PathOptions{})
	if err != nil || len(paths) == 0 {
		t.Fatalf("paths %+v, %v", paths, err)
	}
	for _, q := range paths {
		if q.Hops == 1 {
			t.Errorf("a one-hop path answered across the repos: %q", q.Program)
		}
		if q.Hops < paths[0].Hops {
			t.Errorf("a shorter path came after the first: %q", q.Program)
		}
	}
	if len(paths) < 2 || !strings.Contains(paths[1].Program, `calls(?from, ?n1), node(?n1, "func"), contains(?n1, ?n2)`) || paths[1].Rows[0] != [2]string{"goapp/svc.Other", "oneauth/acct.Get"} {
		t.Errorf("the second path %+v", paths[1:])
	}
	p := paths[0]
	if p.Hops != 2 || !strings.Contains(p.Program, `contains(?from, ?n1), node(?n1, "callsite"), attr(?n1, "callee", ?to)`) {
		t.Errorf("the path %q, %d hops", p.Program, p.Hops)
	}
	if len(p.Rows) != 1 || p.Rows[0] != [2]string{"goapp/svc.Auth", "oneauth/acct.Get"} {
		t.Errorf("rows %v", p.Rows)
	}
	ctl, err := nl.Paths(ctx, b, shape, links, nl.End{Kind: "func", Where: `?x = "oneauth/acct.Save"`}, to, nl.PathOptions{})
	if err != nil || len(ctl) == 0 || !strings.HasPrefix(ctl[0].Program, `node(?from, "func"), ?from = "oneauth/acct.Save", calls(?from, ?to)`) {
		t.Errorf("control: within one repo, paths %+v, %v", ctl, err)
	}
}

// x11's shape: which tests reach Parse. The test calls a helper that calls Parse, so one call
// doesn't reach it and the closure over calls does. control: a closure at most one step deep finds
// nothing.
func TestPathsReachThroughAClosure(t *testing.T) {
	b, shape := codeGraph(t)
	ctx := context.Background()
	links, err := nl.KindGraph(ctx, b, shape)
	if err != nil {
		t.Fatal(err)
	}
	from := nl.End{Kind: "func", Where: `attr(?x, "role", "test")`}
	to := nl.End{Kind: "func", Where: `?x = "oneauth/api.Parse"`}
	paths, err := nl.Paths(ctx, b, shape, links, from, to, nl.PathOptions{})
	if err != nil || len(paths) == 0 {
		t.Fatalf("paths %+v, %v", paths, err)
	}
	p := paths[0]
	if !strings.Contains(p.Program, `graph.closure("calls", ?from, ?to, 1, 6, _)`) || len(p.Rows) != 1 || p.Rows[0][0] != "oneauth/api.TestParse" {
		t.Errorf("the first path %q, rows %v", p.Program, p.Rows)
	}
	for _, q := range paths {
		if q.Hops == 1 && strings.Contains(q.Program, "calls(?from, ?to)") {
			t.Errorf("one call reached Parse: %q", q.Program)
		}
	}
	if shallow, _ := nl.Paths(ctx, b, shape, links, from, to, nl.PathOptions{Depth: 1, MaxHops: 1}); len(shallow) != 0 {
		t.Errorf("control: a closure one step deep found %+v", shallow)
	}
}

// A path walks a link backwards when the ends are the other way round: from oneauth's functions to
// the goapp functions calling them, through the callee attr and contains read from their targets.
func TestPathsWalkLinksBackwards(t *testing.T) {
	b, shape := codeGraph(t)
	ctx := context.Background()
	links, err := nl.KindGraph(ctx, b, shape)
	if err != nil {
		t.Fatal(err)
	}
	from := nl.End{Kind: "func", Where: `str.glob(?x, "oneauth/*")`}
	to := nl.End{Kind: "func", Where: `str.glob(?x, "goapp/*")`}
	paths, err := nl.Paths(ctx, b, shape, links, from, to, nl.PathOptions{})
	if err != nil || len(paths) == 0 {
		t.Fatalf("paths %+v, %v", paths, err)
	}
	p := paths[0]
	if !strings.Contains(p.Program, `attr(?n1, "callee", ?from), node(?n1, "callsite"), contains(?to, ?n1)`) || p.Rows[0] != [2]string{"oneauth/acct.Get", "goapp/svc.Auth"} {
		t.Errorf("the first path %q, rows %v", p.Program, p.Rows)
	}
}
