package bolt

import (
	"context"
	"iter"
	"strconv"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/ns"
)

// Walks reports whether the store walks rel from one endpoint of its types to the other, either way
// round: rel is a type's relation, from and to are its endpoints, and every bound position is one its
// types fix (edge(?a, "calls", ?b) walks CALLS). A bound property is refused, since a walk follows
// only the facts matching it and the server can't match a number as jaala does; the engine then asks
// level by level. Options.LookupsOnly refuses every walk.
func (s *Store) Walks(rel string, from, to int, bound []int) bool {
	r, ok := s.rels[rel]
	if s.lookupsOnly || !ok || len(r.edges) == 0 || from == to {
		return false
	}
	for _, e := range r.edges {
		if !(from == e.From && to == e.To || from == e.To && to == e.From) {
			return false
		}
		for _, p := range bound {
			if _, fixed := e.Fixed[p]; !fixed {
				return false
			}
		}
	}
	return true
}

// Walk returns every fact of w's types out of each node reached from w.Start, in fewer than
// w.MaxDepth steps when it is set: the server finds the reached nodes itself, Neo4j by a
// variable-length match it prunes to a breadth-first search when only the distinct ends are read,
// Memgraph by its BFS, and then returns the facts leaving each, so a walk costs one query however
// deep it goes. A start that isn't text names no node and reaches nothing.
func (s *Store) Walk(ctx context.Context, w ns.Walk) iter.Seq2[ns.Tuple, error] {
	return func(yield func(ns.Tuple, error) bool) {
		r, ok := s.rels[w.Relation]
		if !ok {
			return
		}
		byType := map[string]*cypher.Edge{}
		var types []string
		for i := range r.edges {
			e := &r.edges[i]
			admits := true
			for p, v := range w.Bound {
				if c, fixed := e.Fixed[p]; fixed && (v.Absent || v.S != c) {
					admits = false
				}
			}
			if admits {
				byType[e.Type] = e
				types = append(types, e.Type)
			}
		}
		var start []string
		for _, v := range w.Start {
			if !v.Absent && (v.Num == nil || r.textual(w.From)) {
				start = append(start, v.S)
			}
		}
		if len(types) == 0 || len(start) == 0 || w.MaxDepth < 0 {
			return
		}
		forward := w.From == byType[types[0]].From
		q := s.walkQuery(types, forward, w.MaxDepth)
		s.stream(ctx, q, map[string]any{"start": start}, func(rec *neo4j.Record) bool {
			e := byType[recString(rec, "ty")]
			if e == nil {
				return true
			}
			t, err := edgeTuple(r, e, recString(rec, "f"), recString(rec, "t"), recProps(rec))
			return yield(t, err) && err == nil
		}, func(err error) { yield(ns.Tuple{}, err) })
	}
}

// walkQuery reads the facts leaving every node reached from $start along types, in fewer than depth
// steps (depth 0 for any number), forward from a fact's From to its To or backward. The two servers
// write reachability differently: Neo4j's variable-length match counts zero steps, Memgraph's BFS
// doesn't, so its starts are added back.
func (s *Store) walkQuery(types []string, forward bool, depth int) string {
	quoted := make([]string, len(types))
	for i, t := range types {
		quoted[i] = quote(t)
	}
	rel := ":" + strings.Join(quoted, "|")
	step := func(inner string) string {
		if forward {
			return "-[" + inner + "]->"
		}
		return "<-[" + inner + "]-"
	}
	var reach string
	if s.memgraph() {
		bound := ""
		if depth > 0 {
			bound = " .." + strconv.Itoa(depth-1)
		}
		reach = "MATCH (s:" + s.base + ") WHERE s.id IN $start"
		if depth != 1 {
			reach += " OPTIONAL MATCH (s)" + step(rel+" *BFS"+bound) + "(n)" +
				" WITH collect(DISTINCT s) + collect(DISTINCT n) AS ns UNWIND ns AS n WITH DISTINCT n"
		} else {
			reach += " WITH s AS n"
		}
	} else {
		hops := "*0.."
		if depth > 0 {
			hops += strconv.Itoa(depth - 1)
		}
		reach = "MATCH (s:" + s.base + ") WHERE s.id IN $start MATCH (s)" + step(rel+hops) + "(n) WITH DISTINCT n"
	}
	ends := "n.id AS f, m.id AS t"
	if !forward {
		ends = "m.id AS f, n.id AS t"
	}
	return reach + " MATCH (n)" + step("e"+rel) + "(m) RETURN " + ends + ", type(e) AS ty, properties(e) AS p"
}
