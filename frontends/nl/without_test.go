package nl_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

// An empty answer shows what the rest of the goal gives the culprit's variables: a filter that
// matched nothing, an equality the engine folded into the atom it names, and an atom's wrong
// constant. Nothing shows when the culprit shares no variable with the rest, when the rest pins each
// shared variable to a constant (it would echo the constants back), when its text isn't in the goal
// as written, or under a front end. control: a draft that answers carries none.
func TestAnEmptyAnswerShowsTheRestWithoutItsCulprit(t *testing.T) {
	v, b := guide.Deps(t)
	ctx := context.Background()
	d := nl.DefaultPrompts()
	without := func(o nl.Options, draft string) (string, bool) {
		s, err := nl.NewSession(ctx, "q", v, nil, o)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = s.Draw(ctx, nl.Try{Text: draft})
		var ea *nl.EmptyAnswer
		if !errors.As(err, &ea) {
			return "", false
		}
		return ea.Without, true
	}
	o := nl.Options{Base: b}
	for _, c := range []struct{ draft, literal, vars, value string }{
		{`pkg(?p, ?l), str.prefix(?l, "mit") => ?p`, `str.prefix(?l, "mit")`, "?l", `"MIT"`},
		{`pkg(?p, ?l), ?l = "mit" => ?p`, `?l = "mit"`, "?l", `"MIT"`},
		{`imports(?a, ?b), pkg(?b, "nope") => ?a`, `pkg(?b, "nope")`, "?b", `"util"`},
	} {
		got, empty := without(o, c.draft)
		head := strings.Replace(strings.Replace(d.Without, "%[1]s", c.literal, 1), "%[2]s", c.vars, 1)
		if !empty || !strings.HasPrefix(got, head) || !strings.Contains(got, "\n  "+c.value) {
			t.Errorf("%s: empty %v, without %q", c.draft, empty, got)
		}
	}
	for _, c := range []struct {
		name, draft string
		o           nl.Options
	}{
		{"no shared variable", `pkg(?p, _), pkg(?q, "nope") => ?p`, o},
		{"the rest pins each variable to a constant", `pkg(?p, ?l), ?l = "mit", ?p = "zz" => ?p`, o},
		{"a rule's literal", `lic(?p) :- pkg(?p, "nope"); lic(?p) => ?p`, o},
		{"a front end", `pkg(?p, ?l), str.prefix(?l, "mit") => ?p`, nl.Options{Base: b, FrontEnd: plainFront{}}},
	} {
		if got, empty := without(c.o, c.draft); !empty || got != "" {
			t.Errorf("%s: empty %v, without %q", c.name, empty, got)
		}
	}
	if _, empty := without(o, `pkg(?p, ?l), str.prefix(?l, "MIT") => ?p`); empty {
		t.Errorf("control: a draft that answers came back empty")
	}
}
