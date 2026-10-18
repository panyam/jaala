package cypher_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/cypher"
)

var codeStore = cypher.Store{Node: "node", Edge: "edge", Attr: "attr"}

// The helper reads Declaire's kinds from the data and writes the graph codeBase spells out by hand, so
// the same queries answer over it.
func TestAStoreWritesTheGraphOfItsKinds(t *testing.T) {
	v, b, want := codeBase(t)
	nodes, edges, err := codeStore.Kinds(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(nodes, ",") != "File,Function" || strings.Join(edges, ",") != "CALLS,CONTAINS" {
		t.Fatalf("kinds = %v, %v, want File,Function and CALLS,CONTAINS", nodes, edges)
	}
	g := codeStore.Graph([]string{"Function", "File"}, edges)
	if !reflect.DeepEqual(g, want) {
		t.Fatalf("graph = %+v\nwant %+v", g, want)
	}
	if err := g.Validate(v); err != nil {
		t.Fatal(err)
	}
	p, err := cypher.Compile(v, g, `MATCH (a:Function {name: 'main'})-[:CALLS]->(b:Function) RETURN b.name ORDER BY b.name`)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := rowsOf(t, p, b)
	if err != nil || len(rows) != 2 || rows[0][0] != "help" || rows[1][0] != "parse" {
		t.Errorf("main calls = %v (%v), want help and parse", rows, err)
	}
}

func TestAStoreNamesKindsAsCypherDoesUnlessTheHostSaysOtherwise(t *testing.T) {
	for kind, want := range map[string]string{"func": "Func", "http_route": "HttpRoute", "httpRoute": "HttpRoute", "Function": "Function", "v2": "V2"} {
		if got := cypher.DefaultLabel(kind); got != want {
			t.Errorf("DefaultLabel(%q) = %q, want %q", kind, got, want)
		}
	}
	for kind, want := range map[string]string{"calls": "CALLS", "dispatches_to": "DISPATCHES_TO", "dispatchesTo": "DISPATCHES_TO", "CALLS": "CALLS", "routes-to": "ROUTES_TO"} {
		if got := cypher.DefaultType(kind); got != want {
			t.Errorf("DefaultType(%q) = %q, want %q", kind, got, want)
		}
	}
	s := codeStore
	s.Label = func(k string) string { return "K_" + k }
	g := s.Graph([]string{"func"}, []string{"calls"})
	if g.Nodes[0].Label != "K_func" || g.Edges[0].Type != "CALLS" {
		t.Errorf("overriding Label gave %q and %q, want K_func and the default CALLS", g.Nodes[0].Label, g.Edges[0].Type)
	}
}

// Two kinds one label names would make a label mean two things, which Validate refuses. control: the
// kinds apart validate.
func TestTwoKindsNamedAlikeAreRefused(t *testing.T) {
	v, _, _ := codeBase(t)
	if err := codeStore.Graph([]string{"Function", "File"}, nil).Validate(v); err != nil {
		t.Fatalf("control: %v", err)
	}
	if err := codeStore.Graph([]string{"http_route", "httpRoute"}, nil).Validate(v); err == nil {
		t.Error("two kinds labelled HttpRoute were accepted")
	}
}
