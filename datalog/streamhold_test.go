package datalog

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	"github.com/panyam/jaala/ns"
)

// oneConn is a store with one connection that every read takes, looking up edge and streaming calls:
// Declaire's SQLite store (declaire#370). With cursor set, TuplesSeq holds the connection while it
// yields, as an open cursor does; otherwise it reads a page, releases it, then yields. A read waiting
// for the connection gives up when its context ends.
type oneConn struct {
	*ns.MemSource
	conn   chan struct{}
	cursor bool
}

func newOneConn(cursor bool) *oneConn {
	m := ns.NewMemSource().Declare("calls", "from", "to").Declare("edge", "from", "kind", "to")
	for _, c := range [][2]string{{"a", "b1"}, {"a", "x"}, {"c", "b2"}} {
		m.Add("calls", ns.Tuple{Vals: []ns.Value{ns.S(c[0]), ns.S(c[1])}})
	}
	for _, e := range [][2]string{{"b1", "d"}, {"b2", "e"}, {"x", "f"}} {
		m.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(e[0]), ns.S("calls"), ns.S(e[1])}})
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

func (s *oneConn) LooksUp(rel string) bool { return rel == "edge" }

func (s *oneConn) TuplesSeq(ctx context.Context, rel string) iter.Seq2[ns.Tuple, error] {
	return func(yield func(ns.Tuple, error) bool) {
		if err := s.take(ctx); err != nil {
			yield(ns.Tuple{}, err)
			return
		}
		ts := s.Tuples(rel)
		if s.cursor {
			defer s.give()
		} else {
			s.give()
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

// declaire#370's query, with edge's kind a variable: the goal streams calls and looks edge up for each
// tuple, so a store holding its one connection across TuplesSeq's yields waits on itself (#539).
// Written with "calls" as the kind, as Declaire's was, it plans edge first over this store and streams
// nothing. ns.CheckStreamReentrant says the same of the store.
func TestAStoreHoldingWhatALookupNeedsWhileItStreamsStopsTheQuery(t *testing.T) {
	q := `calls(?a, ?b), str.prefix(?b, "b"), edge(?b, ?k, ?c) => ?a, ?c limit 2`
	run := func(cursor bool) ([]Row, error) {
		src := newOneConn(cursor)
		ctx, cancel := context.WithTimeout(bg, 500*time.Millisecond)
		defer cancel()
		return SemiNaive{}.Eval(ctx, mustParse(t, q), MustBase(std(src), src))
	}
	rows, err := run(false)
	if err != nil || len(rows) != 2 {
		t.Fatalf("control: a store releasing its connection before yielding answered %v, %v; want 2 rows", rows, err)
	}
	if _, err := run(true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a store holding its connection across yields answered %v; want it to wait until the deadline", err)
	}
	ctx, cancel := context.WithTimeout(bg, 500*time.Millisecond)
	defer cancel()
	if err := ns.CheckStreamReentrant(ctx, newOneConn(true), "calls"); err == nil {
		t.Error("ns.CheckStreamReentrant passed the store the query waited on")
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
