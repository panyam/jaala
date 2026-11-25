package nl_test

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

// A look at relation.argument, as values takes it, shows the relation and points at values (#569).
// One naming no argument of the relation lists the ones it takes. control: a dotted word whose
// first part is no relation, or is a module, answers as before.
func TestALookAtARelationsArgumentShowsTheRelation(t *testing.T) {
	ctx := context.Background()
	v, b := guide.Deps(t)
	d := nl.DefaultPrompts()
	s, _ := nl.NewSession(ctx, mitQ, v, nil, nl.Options{Base: b, Actions: &nl.Actions{}})
	pkg := s.Act(ctx, nl.ActionLook, "pkg")
	if got := s.Act(ctx, nl.ActionLook, "pkg.license"); got != pkg+"\npkg.license is one of that relation's arguments; values pkg.license lists some of its values." {
		t.Errorf("look pkg.license: %q", got)
	}
	if got := s.Act(ctx, nl.ActionLook, "pkg.owner"); got != pkg+"\nvalues pkg.owner: pkg's arguments are name, license" {
		t.Errorf("look pkg.owner: %q", got)
	}
	if got := s.Act(ctx, nl.ActionLook, "zebra.license"); !strings.HasSuffix(got, "\n"+d.LookNothing) || strings.Contains(got, "pkg(") {
		t.Errorf("control: look zebra.license: %q", got)
	}
	if got := s.Act(ctx, nl.ActionLook, "str.nothing"); !strings.HasSuffix(got, "\n"+d.LookNothing) {
		t.Errorf("control: look str.nothing, under a module: %q", got)
	}
}
