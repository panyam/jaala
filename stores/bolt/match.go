package bolt

import (
	"fmt"
	"strconv"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/ns"
)

// A match is one place a relation's facts live: a label, a type, or the graph's Props.
type match struct {
	node  *cypher.Node
	edge  *cypher.Edge
	props *cypher.PropRelation
}

func (r *relation) matches() []match {
	var out []match
	for i := range r.nodes {
		out = append(out, match{node: &r.nodes[i]})
	}
	for i := range r.edges {
		out = append(out, match{edge: &r.edges[i]})
	}
	if r.props != nil {
		out = append(out, match{props: r.props})
	}
	return out
}

func (m match) fixed() map[int]string {
	switch {
	case m.node != nil:
		return m.node.Fixed
	case m.edge != nil:
		return m.edge.Fixed
	}
	return nil
}

// admits is whether a fact at this match could hold vals at positions: a bound constant the label
// or type fixes has to be that constant.
func (m match) admits(positions []int, vals []ns.Value) bool {
	for j, p := range positions {
		if c, ok := m.fixed()[p]; ok && (vals[j].Absent || vals[j].S != c) {
			return false
		}
	}
	return true
}

// A filter is a bound argument the server is asked to match, by the expression holding it.
type filter struct {
	expr, param string
	indexed     bool // the expression reads the node index
}

// filters are the bound positions the server can match: keys, endpoints and a property's name by
// their text, and a property kept as text. A number is left to the engine.
func (m match) filters(r *relation, positions []int) map[int]filter {
	out := map[int]filter{}
	add := func(p int, expr string, indexed bool) {
		out[p] = filter{expr: expr, param: "b" + strconv.Itoa(p), indexed: indexed}
	}
	for _, p := range positions {
		switch {
		case m.node != nil:
			if p == m.node.Key {
				add(p, "x.id", true)
			} else if prop, ok := propAt(m.node.Props, p); ok && r.textual(p) {
				add(p, "x."+quote(prop), false)
			}
		case m.edge != nil:
			if p == m.edge.From {
				add(p, "a.id", true)
			} else if p == m.edge.To {
				add(p, "b.id", true)
			} else if prop, ok := propAt(m.edge.Props, p); ok && r.textual(p) {
				add(p, "e."+quote(prop), false)
			}
		case m.props != nil:
			if p == m.props.Node {
				add(p, "x.id", true)
			} else if p == m.props.Key {
				add(p, "k", false)
			}
		}
	}
	return out
}

func propAt(props map[string]int, i int) (string, bool) {
	for p, at := range props {
		if at == i {
			return p, true
		}
	}
	return "", false
}

// query writes the read of this match: with batched, one row per value in $vals, filtered by
// filters and carrying the value's index as i. build turns a row into a fact.
func (m match) query(r *relation, filters map[int]filter, batched bool) (string, map[string]any, func(*neo4j.Record) (ns.Tuple, error)) {
	params := map[string]any{}
	where := func(only func(filter) bool) string {
		w := ""
		for _, f := range sortedFilters(filters) {
			if !only(f) {
				continue
			}
			cond := f.expr + " = v." + f.param
			if !f.indexed {
				cond = "(v." + f.param + " IS NULL OR " + cond + ")"
			}
			if w != "" {
				w += " AND "
			}
			w += cond
		}
		if w == "" {
			return ""
		}
		return " WHERE " + w
	}
	all := func(filter) bool { return true }
	prefix, ret := "", ""
	if batched {
		prefix, ret = "UNWIND $vals AS v ", "v.i AS i, "
	}
	switch {
	case m.node != nil:
		n := m.node
		q := prefix + "MATCH (x:" + nodeLabel + ":" + quote(n.Label) + ")" + where(all) + " RETURN " + ret + "x.id AS id, properties(x) AS p"
		return q, params, func(rec *neo4j.Record) (ns.Tuple, error) {
			props, id := recProps(rec), recString(rec, "id")
			t := ns.Tuple{Vals: make([]ns.Value, r.schema.Arity)}
			t.Vals[n.Key] = ns.S(id)
			for i, c := range n.Fixed {
				t.Vals[i] = ns.S(c)
			}
			for p, i := range n.Props {
				if i == n.Key {
					continue
				}
				v, err := get(props, p)
				if err != nil {
					return t, err
				}
				t.Vals[i] = v
			}
			t.ID, t.Cites = meta(props, "_id:"+n.Label, "_cites:"+n.Label)
			return t, nil
		}
	case m.edge != nil:
		e := m.edge
		q := prefix + "MATCH (a:" + nodeLabel + ")-[e:" + quote(e.Type) + "]->(b:" + nodeLabel + ")" + where(all) +
			" RETURN " + ret + "a.id AS from, b.id AS to, properties(e) AS p"
		return q, params, func(rec *neo4j.Record) (ns.Tuple, error) {
			props := recProps(rec)
			t := ns.Tuple{Vals: make([]ns.Value, r.schema.Arity)}
			t.Vals[e.From], t.Vals[e.To] = ns.S(recString(rec, "from")), ns.S(recString(rec, "to"))
			for i, c := range e.Fixed {
				t.Vals[i] = ns.S(c)
			}
			for p, i := range e.Props {
				v, err := get(props, p)
				if err != nil {
					return t, err
				}
				t.Vals[i] = v
			}
			t.ID, t.Cites = meta(props, "_id", "_cites")
			return t, nil
		}
	}
	pr := m.props
	params["labelled"] = r.labelledProps()
	onNode := func(f filter) bool { return f.indexed }
	onKey := func(f filter) bool { return !f.indexed }
	kw := where(onKey)
	if kw != "" {
		kw = " AND" + kw[len(" WHERE"):]
	}
	q := prefix + "MATCH (x:" + nodeLabel + ")" + where(onNode) + " UNWIND keys(x) AS k WITH " + vars(batched) + "x, k" +
		" WHERE k <> 'id' AND NOT k STARTS WITH '_' AND NOT k IN $labelled" + kw +
		" RETURN " + ret + "x.id AS id, k AS k, properties(x) AS p"
	return q, params, func(rec *neo4j.Record) (ns.Tuple, error) {
		props, k := recProps(rec), recString(rec, "k")
		t := ns.Tuple{Vals: make([]ns.Value, r.schema.Arity)}
		t.Vals[pr.Node], t.Vals[pr.Key] = ns.S(recString(rec, "id")), ns.S(k)
		v, err := get(props, k)
		if err != nil {
			return t, err
		}
		t.Vals[pr.Value] = v
		t.ID, t.Cites = meta(props, "_id."+k, "_cites."+k)
		return t, nil
	}
}

func vars(batched bool) string {
	if batched {
		return "v, "
	}
	return ""
}

func sortedFilters(fs map[int]filter) []filter {
	var out []filter
	for p := 0; len(out) < len(fs); p++ {
		if f, ok := fs[p]; ok {
			out = append(out, f)
		}
	}
	return out
}

func recProps(rec *neo4j.Record) map[string]any {
	p, _ := rec.Get("p")
	m, _ := p.(map[string]any)
	return m
}

func recString(rec *neo4j.Record, key string) string {
	v, _ := rec.Get(key)
	s, _ := v.(string)
	return s
}

// meta reads a fact's id and citations from the properties named id and cites.
func meta(props map[string]any, id, cites string) (string, []string) {
	s, _ := props[id].(string)
	var cs []string
	if list, ok := props[cites].([]any); ok {
		for _, c := range list {
			cs = append(cs, fmt.Sprint(c))
		}
	}
	return s, cs
}
