package nl_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/internal/guide"
)

// codeGraph is guide.Code with its Shape.
func codeGraph(t *testing.T) (*datalog.Base, nl.Shape) {
	t.Helper()
	return guide.Code(t), nl.Shape{Kinds: "node", Edges: []string{"calls", "contains"}, Refs: []nl.Ref{{Relation: "attr", Key: "callee"}}}
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
	rows := map[string]string{}
	for _, q := range paths {
		if prev, ok := rows[fmt.Sprint(q.Rows)]; ok {
			t.Errorf("two paths answered %v: %q and %q", q.Rows, prev, q.Program)
		}
		rows[fmt.Sprint(q.Rows)] = q.Program
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

// The paths action, offered with a pack's Shape over a Base, answers with the joins that answered and
// their rows, and keeps their values, so a later guess at one's spelling is repaired; its ends come as FROM => TO. Without a kind on an
// end, or with no join answering, it says so. The pack's Links are used as given. control: without
// a Shape, paths isn't offered.
func TestThePathsActionListsTheJoinsThatAnswered(t *testing.T) {
	b, shape := codeGraph(t)
	ctx := context.Background()
	d := nl.DefaultPrompts()
	s, err := nl.NewSession(ctx, "q", b.Vocabulary(), nil, nl.Options{Base: b, Pack: &nl.Pack{Shape: &shape}, Actions: &nl.Actions{}})
	if err != nil {
		t.Fatal(err)
	}
	prefix, grammar := s.TextActions()
	var tools []string
	for _, tl := range s.Tools() {
		tools = append(tools, tl.Action)
	}
	if !strings.Contains(strings.Join(tools, " "), "paths") || !strings.Contains(prefix, "#   paths FROM => TO") || !strings.Contains(grammar, `"paths "`) {
		t.Errorf("tools %v; prefix or grammar leaves out paths", tools)
	}
	ends := `func str.glob(?x, "goapp/*") => func str.glob(?x, "oneauth/*")`
	r := s.Act(ctx, nl.ActionPaths, ends)
	head := fmt.Sprintf(d.PathsFound, `func str.glob(?x, "goapp/*")`, `func str.glob(?x, "oneauth/*")`)
	if !strings.HasPrefix(r, head) || !strings.Contains(r, `1. node(?from, "func"), str.glob(?from, "goapp/*"), contains(?from, ?n1)`) || !strings.Contains(r, `"goapp/svc.Auth", "oneauth/acct.Get"`) {
		t.Errorf("paths sent back %q", r)
	}
	guess := `node(?f, "func"), ?f = "oneauth/acct.get" => ?f`
	if vd := s.NewConversation(nil, true).Judge(ctx, nl.Try{Text: guess}); vd.Program == nil || vd.Program.Text != strings.Replace(guess, "acct.get", "acct.Get", 1) {
		t.Errorf("a spelling of an id paths showed wasn't repaired: %+v", vd)
	}
	if r := s.Act(ctx, nl.ActionPaths, `func ?x = "nope" => func`); r != fmt.Sprintf(d.PathsNone, `func ?x = "nope"`, "func", "3") {
		t.Errorf("no join answering sent back %q", r)
	}
	if r := s.Act(ctx, nl.ActionPaths, "func"); !strings.HasPrefix(r, "paths takes FROM => TO") {
		t.Errorf("paths without its arrow sent back %q", r)
	}
	only := []nl.Link{{Relation: "calls", From: "func", To: "func"}}
	given, _ := nl.NewSession(ctx, "q", b.Vocabulary(), nil, nl.Options{Base: b, Pack: &nl.Pack{Shape: &shape, Links: only}, Actions: &nl.Actions{}})
	if r := given.Act(ctx, nl.ActionPaths, ends); !strings.HasPrefix(r, "No join") {
		t.Errorf("the pack's Links, holding calls alone, still found %q", r)
	}
	plain, _ := nl.NewSession(ctx, "q", b.Vocabulary(), nil, nl.Options{Base: b, Pack: &nl.Pack{}, Actions: &nl.Actions{}})
	if vd := plain.NewConversation(nil, true).Judge(ctx, nl.Try{Text: guess}); vd.Program != nil {
		t.Errorf("control: with nothing seen, the guess was repaired")
	}
	for _, tl := range plain.Tools() {
		if tl.Action == nl.ActionPaths {
			t.Errorf("control: paths offered without a Shape")
		}
	}
}

// A join that fails to check is explained against the program jaala wrote, never against the
// model's argument (#530: Locate's offset in the longer program ran past the argument's end and
// Explain panicked). The caret sits under the program's text.
func TestAFailedJoinIsExplainedAgainstItsProgram(t *testing.T) {
	b, shape := codeGraph(t)
	ctx := context.Background()
	s, err := nl.NewSession(ctx, "q", b.Vocabulary(), nil, nl.Options{Base: b, Pack: &nl.Pack{Shape: &shape}, Actions: &nl.Actions{}})
	if err != nil {
		t.Fatal(err)
	}
	r := s.Act(ctx, nl.ActionPaths, `func => func ?x = = "a"`)
	if !strings.Contains(r, `node(?from, "func")`) || !strings.Contains(r, "^") {
		t.Errorf("a failed join sent back %q", r)
	}
}

// Explain keeps just the message for text the error doesn't point into, rather than slicing past
// its end.
func TestExplainKeepsTheMessageForTextTheErrorIsNotIn(t *testing.T) {
	_, err := datalog.Parse(`node(?from, "func"), calls(?from, ?to), ?to = = "a" => ?from`)
	if err == nil {
		t.Fatal("control: the program parsed")
	}
	if got := nl.Explain("short", err); got != err.Error() {
		t.Errorf("Explain over other text gave %q", got)
	}
}

// An end whose literals never mention ?x is refused, so the model ties it to the end (#530: x01's
// ?p ran unbound, and the join answered every package importing every package).
func TestAnEndThatNeverMentionsXIsRefused(t *testing.T) {
	for _, arg := range []string{
		`package ?p = "github.com/go-delve/delve" => package ?p = "github.com/go-delve/delve"`,
		`func => func ?y = "a"`,
	} {
		if _, _, err := nl.ParseEnds(arg); err == nil || !strings.Contains(err.Error(), "?x") {
			t.Errorf("%s: %v", arg, err)
		}
	}
	if _, _, err := nl.ParseEnds(`func str.glob(?x, "a/*") => func`); err != nil {
		t.Errorf("control: an end over ?x was refused: %v", err)
	}
}

// A join comes back without a limit, so a model answering with it isn't held to the rows shown
// (#530: x01's answer kept limit 5 against 20 expected rows). The rows shown are still capped.
func TestAJoinComesBackWithoutALimit(t *testing.T) {
	b, shape := codeGraph(t)
	ctx := context.Background()
	links, err := nl.KindGraph(ctx, b, shape)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := nl.Paths(ctx, b, shape, links, nl.End{Kind: "func"}, nl.End{Kind: "func"}, nl.PathOptions{Rows: 2})
	if err != nil || len(paths) == 0 {
		t.Fatalf("paths %+v, %v", paths, err)
	}
	for _, p := range paths {
		if strings.Contains(p.Program, "limit") {
			t.Errorf("a join kept its limit: %q", p.Program)
		}
		if len(p.Rows) > 2 {
			t.Errorf("%d rows shown, want at most 2", len(p.Rows))
		}
	}
	if len(paths[0].Rows) != 2 {
		t.Errorf("control: the first join showed %d rows, want 2", len(paths[0].Rows))
	}
}

// ParseEnds says which of its three refusals it made, as an *EndsError, so a Session writes it from
// the pack's Prompts (#532); its own text is unchanged for a caller outside a Session. A Session
// writes the pack's PathsUsage and PathsNoKind, which only a tool call can reach: a reply read as a
// text action needs " => " and a kind on each side.
func TestParseEndsSaysWhichRefusal(t *testing.T) {
	for _, c := range []struct {
		arg     string
		problem nl.EndsProblem
		text    string
	}{
		{"func", nl.EndsNoArrow, `paths takes FROM => TO, each a kind and literals over ?x, as func str.glob(?x, "a/*") => func ?x = "b.F"`},
		{" => func", nl.EndsNoKind, `paths: an end needs a kind first, in " => func"`},
		{`func ?y = "a" => func`, nl.EndsUnbound, `paths: the literals after func never mention ?x, the end's node, so nothing ties them to it; write them over ?x, as func ?x = "an id"`},
	} {
		_, _, err := nl.ParseEnds(c.arg)
		var ee *nl.EndsError
		if !errors.As(err, &ee) || ee.Problem != c.problem || err.Error() != c.text {
			t.Errorf("%q: %#v", c.arg, err)
		}
	}
	b, shape := codeGraph(t)
	ctx := context.Background()
	s, err := nl.NewSession(ctx, "q", b.Vocabulary(), nil, nl.Options{Base: b, Pack: &nl.Pack{Shape: &shape, Prompts: &nl.Prompts{PathsUsage: "H-USAGE", PathsNoKind: "H-NOKIND %s"}}, Actions: &nl.Actions{}})
	if err != nil {
		t.Fatal(err)
	}
	if r := s.Act(ctx, nl.ActionPaths, " => func"); r != `H-NOKIND " => func"` {
		t.Errorf("an end with no kind sent back %q", r)
	}
	if r := s.Act(ctx, nl.ActionPaths, "func"); r != "H-USAGE" {
		t.Errorf("an argument with no arrow sent back %q", r)
	}
}
