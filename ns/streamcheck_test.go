package ns_test

import (
	"context"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/panyam/jaala/ns"
)

// oneConn is a store with one connection, which every read holds: Declaire's SQLite store
// (declaire#370). With cursor set, TuplesSeq keeps the connection while it yields, as an open cursor
// does; otherwise it reads a page, releases the connection, then yields. A read waiting for the
// connection gives up when its context ends, so a failed check leaves nothing blocked.
type oneConn struct {
	*ns.MemSource
	conn   chan struct{}
	cursor bool
}

func newOneConn(cursor bool) *oneConn {
	m := ns.NewMemSource().Declare("edge", "from", "to")
	for _, e := range [][2]string{{"a", "b"}, {"b", "c"}, {"c", "d"}} {
		m.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(e[0]), ns.S(e[1])}})
	}
	return &oneConn{MemSource: m, conn: make(chan struct{}, 1), cursor: cursor}
}

func (s *oneConn) take(ctx context.Context) error {
	select {
	case s.conn <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *oneConn) give() { <-s.conn }

func (s *oneConn) TuplesSeq(ctx context.Context, rel string) iter.Seq2[ns.Tuple, error] {
	return func(yield func(ns.Tuple, error) bool) {
		if err := s.take(ctx); err != nil {
			yield(ns.Tuple{}, err)
			return
		}
		ts := s.Tuples(rel)
		if !s.cursor {
			s.give()
		} else {
			defer s.give()
		}
		for _, t := range ts {
			if !yield(t, nil) {
				return
			}
		}
	}
}

func (s *oneConn) Lookup(ctx context.Context, rel string, bound map[int]ns.Value) ([]ns.Tuple, error) {
	if err := s.take(ctx); err != nil {
		return nil, err
	}
	defer s.give()
	var out []ns.Tuple
	for _, t := range s.Tuples(rel) {
		if matches(t, bound) {
			out = append(out, t)
		}
	}
	return out, nil
}

func shortly(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func TestAStoreHoldingItsConnectionAcrossYieldsFailsTheCheck(t *testing.T) {
	err := ns.CheckStreamReentrant(shortly(t), newOneConn(true), "edge")
	if err == nil || !strings.Contains(err.Error(), "while TuplesSeq yields") {
		t.Fatalf("got %v; want the check to fail naming the held read", err)
	}
	if !strings.Contains(err.Error(), "a second stream of edge") {
		t.Errorf("got %v; want it to name the read that blocked", err)
	}
}

func TestAStoreReleasingItsConnectionBeforeYieldingPassesTheCheck(t *testing.T) {
	if err := ns.CheckStreamReentrant(shortly(t), newOneConn(false), "edge"); err != nil {
		t.Fatal(err)
	}
}

// lookupOnly is a store whose streams share the connection with each other but not with a Lookup,
// so only the Lookup the check makes between tuples finds it held: a control that the check makes one.
type lookupOnly struct {
	*oneConn
	streams *int
}

func (s lookupOnly) TuplesSeq(ctx context.Context, rel string) iter.Seq2[ns.Tuple, error] {
	return func(yield func(ns.Tuple, error) bool) {
		if *s.streams == 0 {
			if err := s.take(ctx); err != nil {
				yield(ns.Tuple{}, err)
				return
			}
			defer s.give()
		}
		*s.streams++
		defer func() { *s.streams-- }()
		for _, t := range s.Tuples(rel) {
			if !yield(t, nil) {
				return
			}
		}
	}
}

func TestTheCheckLooksUpBetweenTuples(t *testing.T) {
	src := lookupOnly{newOneConn(true), new(int)}
	err := ns.CheckStreamReentrant(shortly(t), src, "edge")
	if err == nil || !strings.Contains(err.Error(), "a Lookup of edge") {
		t.Fatalf("got %v; want the Lookup between tuples to block", err)
	}
}

func TestTheEnginesOwnSourcesPassTheCheck(t *testing.T) {
	mem := newOneConn(false).MemSource
	fed, err := ns.NewFederation(
		ns.Member{Name: "a", Source: newOneConn(false)},
		ns.Member{Name: "b", Source: mem},
	)
	if err != nil {
		t.Fatal(err)
	}
	var add ns.Changes
	add.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S("d"), ns.S("e")}})
	for name, src := range map[string]ns.Source{
		"mem":                mem,
		"federation":         fed,
		"overlay":            ns.Overlay(mem, add),
		"overlay of a store": ns.Overlay(newOneConn(false), add),
	} {
		if err := ns.CheckStreamReentrant(shortly(t), src, "edge"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// matches says t holds bound's values at their positions.
func matches(t ns.Tuple, bound map[int]ns.Value) bool {
	for i, v := range bound {
		if t.Vals[i].S != v.S {
			return false
		}
	}
	return true
}
