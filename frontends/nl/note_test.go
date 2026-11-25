package nl_test

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

// A Candidate's Note says what a name is when it isn't a value: find and look show it alone, or after
// the values, and the question's grounded lines carry it. control: a Candidate without one lists its
// values as before.
func TestACandidatesNoteFollowsItsValues(t *testing.T) {
	v, b := guide.Deps(t)
	ctx := context.Background()
	note := `an attr key, attr(?id, "load_errors", ?v)`
	g := &grounder{cands: []nl.Candidate{{Name: "load_errors", Note: note}}}
	s, _ := nl.NewSession(ctx, mitQ, v, g, nl.Options{Base: b, Actions: &nl.Actions{}})
	if got := s.Act(ctx, nl.ActionFind, "load_errors"); !strings.HasSuffix(got, "\nload_errors: "+note) {
		t.Errorf("find with a note alone: %q", got)
	}
	if got := s.Act(ctx, nl.ActionLook, "load_errors"); !strings.HasSuffix(got, "\nload_errors: "+note) {
		t.Errorf("look with a note alone: %q", got)
	}

	g.cands = []nl.Candidate{{Name: "calls", Values: []string{"a.calls", "b.calls"}, Note: "a relation too"}}
	if got := s.Act(ctx, nl.ActionFind, "calls"); !strings.HasSuffix(got, "\ncalls: \"a.calls\", \"b.calls\"; a relation too") {
		t.Errorf("find with values and a note: %q", got)
	}

	g.cands = []nl.Candidate{{Name: "calls", Values: []string{"a.calls"}}}
	if got := s.Act(ctx, nl.ActionFind, "calls"); !strings.HasSuffix(got, "\ncalls: \"a.calls\"") {
		t.Errorf("control: find without a note: %q", got)
	}
}
