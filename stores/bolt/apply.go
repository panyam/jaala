package bolt

import (
	"context"
	"fmt"
	"slices"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/panyam/jaala/ns"
)

// chunk is how many facts one statement writes.
const chunk = 1000

// A write is one statement over rows, run in order inside Apply's transaction. A check is a read
// that must find nothing: a row it returns names a fact the store can't hold beside what it has.
type write struct {
	query string
	rows  []map[string]any
	check func(id string) error
}

// Apply writes c to the server in one transaction: every relation's removals, then its additions,
// so a fact c replaces (a weight moving from 7 to 8) is removed before its new value is added. A
// relation the store doesn't lay out, a fact no label or type of its relation admits, a key or
// endpoint that isn't text, a Props key the store reserves, and a second value for a node's label or
// property (a label's relation holds one fact per key, and Props one value per key) are refused,
// and nothing is written. Adding a fact the store already holds writes nothing new, and removing one
// it doesn't hold is no error.
//
// A host committing a session applies its overlay's changes (ns.Overlay(store, c), then
// store.Apply(ctx, c)), so the store answers as the overlay did.
func (s *Store) Apply(ctx context.Context, c ns.Changes) error {
	var removes, adds, checks []write
	for _, rel := range c.Relations() {
		r, ok := s.rels[rel]
		if !ok {
			return fmt.Errorf("bolt: the store doesn't hold %q", rel)
		}
		rm, err := s.writes(r, c.Removed(rel), false)
		if err != nil {
			return err
		}
		ad, err := s.writes(r, c.Added(rel), true)
		if err != nil {
			return err
		}
		removes = append(removes, rm...)
		for _, w := range ad {
			if w.check != nil {
				checks = append(checks, w)
			} else {
				adds = append(adds, w)
			}
		}
	}
	sess := s.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: s.db, AccessMode: neo4j.AccessModeWrite})
	defer sess.Close(ctx)
	_, err := sess.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		for _, group := range [][]write{removes, checks, adds} {
			for _, w := range group {
				for start := 0; start < len(w.rows); start += chunk {
					rows := w.rows[start:min(start+chunk, len(w.rows))]
					res, err := tx.Run(ctx, w.query, map[string]any{"rows": rows})
					if err != nil {
						return nil, err
					}
					if w.check != nil && res.Next(ctx) {
						id, _ := res.Record().Get("id")
						return nil, w.check(fmt.Sprint(id))
					}
					if _, err := res.Consume(ctx); err != nil {
						return nil, err
					}
				}
			}
		}
		return nil, nil
	})
	if err != nil {
		return fmt.Errorf("bolt: %w", err)
	}
	return nil
}

// writes are the statements adding (or removing) ts in r, grouped by the label or type each fact
// goes to.
func (s *Store) writes(r *relation, ts []ns.Tuple, add bool) ([]write, error) {
	if len(ts) == 0 {
		return nil, nil
	}
	ms := r.matches()
	rows := make([][]map[string]any, len(ms))
	seen := make([]map[string]string, len(ms)) // a node fact's key (and property) to its values, to refuse a second
	for _, t := range ts {
		if len(t.Vals) != r.schema.Arity {
			return nil, fmt.Errorf("bolt: %s takes %d values, and a change gives %d", r.name, r.schema.Arity, len(t.Vals))
		}
		at := -1
		for k, m := range ms {
			if m.holds(t.Vals) {
				at = k
				break
			}
		}
		if at < 0 {
			return nil, fmt.Errorf("bolt: no label or type of %s holds %s", r.name, show(t.Vals))
		}
		row, slot, err := s.row(r, ms[at], t, add)
		if err != nil {
			return nil, err
		}
		if add && slot != "" {
			if seen[at] == nil {
				seen[at] = map[string]string{}
			}
			key := row["key"].(string)
			if prev, ok := seen[at][slot]; ok && prev != key {
				return nil, fmt.Errorf("bolt: a change gives %s two facts for %s, and the store keeps one", r.name, slot)
			}
			seen[at][slot] = key
		}
		rows[at] = append(rows[at], row)
	}
	var out []write
	for k, m := range ms {
		if len(rows[k]) == 0 {
			continue
		}
		for _, w := range m.statements(r, add) {
			w.rows = rows[k]
			out = append(out, w)
		}
	}
	return out, nil
}

// holds is whether a fact of vals belongs at this match: every constant the label or type fixes is
// in place.
func (m match) holds(vals []ns.Value) bool {
	for i, c := range m.fixed() {
		if vals[i].Absent || vals[i].S != c {
			return false
		}
	}
	return true
}

// row is a fact's parameters for its match's statements, and the slot a node's label or property
// keeps it in ("" for a relationship, which holds any number).
func (s *Store) row(r *relation, m match, t ns.Tuple, add bool) (map[string]any, string, error) {
	key := func(i int) (string, error) {
		v := t.Vals[i]
		if v.Absent || (v.Num != nil && !r.textual(i)) {
			return "", fmt.Errorf("bolt: %s's argument %d keys a node, and %s isn't text", r.name, i, show(t.Vals))
		}
		return v.S, nil
	}
	props := map[string]any{}
	metaProps := func(keyName, idName, citesName string) {
		props[keyName] = factKey(t.Vals)
		if add {
			if t.ID != "" {
				props[idName] = t.ID
			}
			if len(t.Cites) > 0 {
				props[citesName] = slices.Clone(t.Cites)
			}
		}
	}
	row := map[string]any{"key": factKey(t.Vals)}
	switch {
	case m.node != nil:
		n := m.node
		id, err := key(n.Key)
		if err != nil {
			return nil, "", err
		}
		row["id"], row["ord"] = id, ordKey(id)
		if add {
			for p, i := range n.Props {
				if i != n.Key {
					put(props, p, t.Vals[i], r.textual(i))
				}
			}
			metaProps("_key:"+n.Label, "_id:"+n.Label, "_cites:"+n.Label)
			row["props"] = props
		} else {
			row["nulls"] = nulls(propNames(n.Props, n.Key), "_key:"+n.Label, "_id:"+n.Label, "_cites:"+n.Label)
		}
		return row, id, nil
	case m.edge != nil:
		e := m.edge
		from, err := key(e.From)
		if err != nil {
			return nil, "", err
		}
		to, err := key(e.To)
		if err != nil {
			return nil, "", err
		}
		row["from"], row["to"], row["ford"], row["tord"] = from, to, ordKey(from), ordKey(to)
		if add {
			for p, i := range e.Props {
				put(props, p, t.Vals[i], r.textual(i))
			}
			if t.ID != "" {
				props["_id"] = t.ID
			}
			if len(t.Cites) > 0 {
				props["_cites"] = slices.Clone(t.Cites)
			}
			row["props"] = props
		}
		return row, "", nil
	}
	p := m.props
	id, err := key(p.Node)
	if err != nil {
		return nil, "", err
	}
	k := t.Vals[p.Key]
	if k.Absent || k.Num != nil && !r.textual(p.Key) {
		return nil, "", fmt.Errorf("bolt: %s names a property by %s, which isn't text", r.name, show(t.Vals))
	}
	if err := reserved(k.S); err != nil {
		return nil, "", err
	}
	if slices.Contains(r.labelled, k.S) {
		return nil, "", fmt.Errorf("bolt: property %q is a label's column, so %s can't hold it too", k.S, r.name)
	}
	row["id"], row["k"], row["ord"] = id, k.S, ordKey(id)
	if add && t.Vals[p.Value].Absent {
		return nil, "", fmt.Errorf("bolt: %s gives %s no value, and a node keeps a property only by its value", r.name, show(t.Vals))
	}
	if add {
		put(props, k.S, t.Vals[p.Value], r.textual(p.Value))
		metaProps("_key."+k.S, "_id."+k.S, "_cites."+k.S)
		row["props"] = props
	} else {
		row["nulls"] = nulls([]string{k.S}, "_key."+k.S, "_id."+k.S, "_cites."+k.S)
	}
	return row, id + "\x00" + k.S, nil
}

func propNames(props map[string]int, key int) []string {
	var out []string
	for p, i := range props {
		if i != key {
			out = append(out, p)
		}
	}
	return out
}

// nulls is the map that, added to a node's properties, removes ps with their companions and the
// named extras.
func nulls(ps []string, extra ...string) map[string]any {
	m := map[string]any{}
	for _, p := range ps {
		m[p], m[unitProp(p)], m[textProp(p)] = nil, nil, nil
	}
	for _, e := range extra {
		m[e] = nil
	}
	return m
}

// statements write a match's rows: an addition's check and its write, or a removal.
func (m match) statements(r *relation, add bool) []write {
	n := "(x:" + r.base + " {id: r.id})"
	switch {
	case m.node != nil:
		l := quote(m.node.Label)
		k := "x." + quote("_key:"+m.node.Label)
		if !add {
			return []write{{query: "UNWIND $rows AS r MATCH (x:" + r.base + ":" + l + " {id: r.id}) WHERE " + k + " = r.key REMOVE x:" + l + " SET x += r.nulls"}}
		}
		return []write{
			{query: "UNWIND $rows AS r MATCH (x:" + r.base + ":" + l + " {id: r.id}) WHERE " + k + " <> r.key RETURN r.id AS id LIMIT 1",
				check: func(id string) error {
					return fmt.Errorf("bolt: %s already holds a fact for %s, and the store keeps one per key", r.name, id)
				}},
			{query: "UNWIND $rows AS r MERGE " + n + " SET x:" + l + " SET x += r.props, x._ord = r.ord"},
		}
	case m.edge != nil:
		t := quote(m.edge.Type)
		if !add {
			return []write{{query: "UNWIND $rows AS r MATCH (a:" + r.base + " {id: r.from})-[e:" + t + " {_key: r.key}]->(b:" + r.base + " {id: r.to}) DELETE e"}}
		}
		return []write{{query: "UNWIND $rows AS r MERGE (a:" + r.base + " {id: r.from}) MERGE (b:" + r.base + " {id: r.to}) MERGE (a)-[e:" + t + " {_key: r.key}]->(b) SET e += r.props, a._ord = r.ford, b._ord = r.tord"}}
	}
	if !add {
		return []write{{query: "UNWIND $rows AS r MATCH " + n + " WHERE properties(x)['_key.' + r.k] = r.key SET x += r.nulls"}}
	}
	return []write{
		{query: "UNWIND $rows AS r MATCH " + n + " WHERE properties(x)['_key.' + r.k] IS NOT NULL AND properties(x)['_key.' + r.k] <> r.key RETURN r.id + ' ' + r.k AS id LIMIT 1",
			check: func(id string) error {
				return fmt.Errorf("bolt: %s already holds a value for %s, and the store keeps one per property", r.name, id)
			}},
		{query: "UNWIND $rows AS r MERGE " + n + " SET x += r.props, x._ord = r.ord"},
	}
}

func show(vals []ns.Value) string {
	out := "("
	for i, v := range vals {
		if i > 0 {
			out += ", "
		}
		if v.Absent {
			out += "absent"
		} else {
			out += fmt.Sprintf("%q", v.S)
		}
	}
	return out + ")"
}
