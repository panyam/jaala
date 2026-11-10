package cypherarm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/gbnf"
	"github.com/panyam/jaala/internal/guide"
)

// A model writing Cypher is offered a decline in its own terms: the prefix says "query", and
// Cypher's grammar takes a decline beside a query. control: a query is still accepted.
func TestAModelWritingCypherMayDecline(t *testing.T) {
	v, _ := guide.Deps(t)
	s, err := nl.NewSession(context.Background(), "q", v, nil, nl.Options{FrontEnd: cypher.NewFrontEnd(depsGraph), Abstain: &nl.Abstain{Declined: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.Prefix(), "reply none: and the fact that is missing, in place of a query.\n# Counting, adding up or ordering the facts is part of the query") {
		t.Errorf("the prefix doesn't offer a decline in Cypher's terms:\n%s", s.Prefix())
	}
	g, err := gbnf.Parse(s.Grammar())
	if err != nil {
		t.Fatal(err)
	}
	if !g.Accepts("none: the graph holds no maintainers") || !g.Accepts("MATCH (p:Package) RETURN p.name") {
		t.Errorf("Cypher's grammar under Declined refuses a decline or a query")
	}
}
