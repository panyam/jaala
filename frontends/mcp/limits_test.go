package mcp_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/mcp"
	"github.com/panyam/jaala/frontends/nl"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/mcpkit/testutil"
)

// slowRoads is roads with slow(?x), a generator that answers nothing until its context ends, or for
// three seconds.
func slowRoads(t *testing.T) (*ns.Vocabulary, *datalog.Base) {
	t.Helper()
	v, b := roads(t)
	err := v.AddPredicate("slow", ns.Builtin{Arity: 1, Modes: [][]bool{{false}}, Gen: func(ctx context.Context, _ ns.Source, _ []ns.Arg, _ func([]ns.Value, []string) error) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second): // bounded, so a call no timeout stops fails rather than hangs
			return errors.New("slow ran its course")
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	return v, b
}

// quick is a host argument asking for a call's own, shorter timeout.
type quick struct {
	Quick bool `json:"quick,omitempty"`
}

// A call stops at the server's timeout, or a Run's shorter one, and says how long the server gives
// it. A Run can't lengthen it. control: a query that finishes answers.
func TestACallStopsAtItsTimeout(t *testing.T) {
	v, b := slowRoads(t)
	serve := func(timeout, run time.Duration) *testutil.TestClient {
		srv, err := mcp.NewWith(v, pack, mcp.Options{Base: func(context.Context) (*datalog.Base, error) { return b, nil }, Timeout: timeout},
			mcp.Extension[quick]{Prepare: func(_ context.Context, _ string, q quick) (mcp.Run, error) {
				if q.Quick {
					return mcp.Run{Timeout: run}, nil
				}
				return mcp.Run{}, nil
			}})
		if err != nil {
			t.Fatal(err)
		}
		return testutil.NewTestClient(t, srv)
	}
	for _, c := range []struct {
		name         string
		timeout, run time.Duration
		quick        bool
		says         string
	}{
		{"server", 50 * time.Millisecond, 0, false, "after 50ms"},
		{"run shorter", 10 * time.Second, 30 * time.Millisecond, true, "after 30ms"},
		{"run longer", 40 * time.Millisecond, 10 * time.Second, true, "after 40ms"},
	} {
		cl := serve(c.timeout, c.run)
		start := time.Now()
		res := call(t, cl, "query", map[string]any{"program": `slow(?x) => ?x`, "quick": c.quick})
		if !res.IsError || !strings.Contains(res.Content[0].Text, "This server stops a call "+c.says) {
			t.Errorf("%s: %+v", c.name, res.Content)
		}
		if took := time.Since(start); took > 5*time.Second {
			t.Errorf("%s: the call took %s", c.name, took)
		}
		if res := call(t, cl, "query", map[string]any{"program": `road("a", ?t) => ?t`, "quick": c.quick}); res.IsError {
			t.Errorf("control: %s: %+v", c.name, res.Content)
		}
	}
}

// A server given Options.Vocabulary answers initialize and lists its tools without it, resolves it on
// the first call that reads it, once, and tries again after it fails; query never waits for it.
func TestAVocabularyIsResolvedOnFirstUse(t *testing.T) {
	v, b := roads(t)
	calls, fail := 0, true
	srv, err := mcp.New(nil, pack, mcp.Options{
		Base: func(context.Context) (*datalog.Base, error) { return b, nil },
		Vocabulary: func(context.Context) (*ns.Vocabulary, error) {
			calls++
			if fail {
				fail = false
				return nil, errors.New("the workspace is still opening")
			}
			return v, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := testutil.NewTestClient(t, srv)
	if !strings.Contains(c.Client.Instructions, "Roads between towns.") || !strings.Contains(toolNames(c), "schema") || calls != 0 {
		t.Fatalf("initialize asked for the vocabulary %d times; instructions %q", calls, c.Client.Instructions)
	}
	if res := call(t, c, "query", map[string]any{"program": `road("a", ?t) => ?t`}); res.IsError || calls != 0 {
		t.Errorf("query asked for the vocabulary %d times: %+v", calls, res.Content)
	}
	if res := call(t, c, "schema", map[string]any{}); !res.IsError || !strings.Contains(res.Content[0].Text, "still opening") || calls != 1 {
		t.Errorf("a failing vocabulary: %d calls, %+v", calls, res.Content)
	}
	for range 2 {
		if text := c.ToolCall("schema", map[string]any{}); !strings.Contains(text, "road(from, to)") {
			t.Errorf("schema: %s", text)
		}
	}
	if calls != 2 {
		t.Errorf("resolved %d times, want once after the failure", calls)
	}
}

// A pack resolved later is checked then: a broken example fails the first call that reads the
// vocabulary, where New would refuse it. New takes a vocabulary or Options.Vocabulary, not both or
// neither.
func TestALazyVocabularyIsCheckedOnFirstUse(t *testing.T) {
	v, b := roads(t)
	base := func(context.Context) (*datalog.Base, error) { return b, nil }
	lazy := func(context.Context) (*ns.Vocabulary, error) { return v, nil }
	broken := pack
	broken.Examples = []nl.Example{{Question: "q", Program: `reach("a", ?t) => ?t`}}
	if _, err := mcp.New(v, broken, mcp.Options{Base: base}); err == nil {
		t.Errorf("control: New takes a broken pack with its vocabulary")
	}
	srv, err := mcp.New(nil, broken, mcp.Options{Base: base, Vocabulary: lazy})
	if err != nil {
		t.Fatal(err)
	}
	if res := call(t, testutil.NewTestClient(t, srv), "schema", map[string]any{}); !res.IsError || !strings.Contains(res.Content[0].Text, "example 1") {
		t.Errorf("a broken pack's first call: %+v", res.Content)
	}
	if _, err := mcp.New(v, pack, mcp.Options{Base: base, Vocabulary: lazy}); err == nil {
		t.Errorf("both a vocabulary and Options.Vocabulary: no error")
	}
	if _, err := mcp.New(nil, pack, mcp.Options{Base: base}); err == nil {
		t.Errorf("neither: no error")
	}
}
