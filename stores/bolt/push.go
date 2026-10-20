package bolt

import (
	"context"
	"encoding/hex"
	"fmt"
	"iter"
	"slices"
	"strconv"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/ns"
)

// ordKey is the property ordering a node by its id as jaala orders text, by its bytes: the hex of
// its id's UTF-8, which every server orders as jaala does, since Neo4j orders strings by UTF-16 unit
// (😀 before U+FFFD) where jaala and Memgraph order by code point.
func ordKey(id string) string { return hex.EncodeToString([]byte(id)) }

// An occurrence is one place a conjunction's variable sits: the expression holding its value, and
// what the server can compare there exactly as jaala does.
type occurrence struct {
	expr string
	kind occKind
	// node is the node variable when the occurrence is a node's id, so its _ord orders it.
	node string
	// value names the property and its companions when the occurrence is one, to read it back.
	prop, owner string
}

type occKind int

const (
	keyOcc     occKind = iota // a node's id: text, never absent
	textOcc                   // a property kept as text, which may be absent
	numericOcc                // a property that may hold a number (typed one, or untyped)
)

// A compiled conjunction: its MATCH clauses and conditions, how each atom's tuple is read back from
// a row, and where each variable sits.
type compiled struct {
	matches []string
	where   []string
	params  map[string]any
	returns []string
	atoms   []func(*neo4j.Record) (ns.Tuple, error)
	cites   []string // per atom, the expression holding its fact's citations
	vars    map[int][]occurrence
	empty   bool // an atom names a fact no label or type of its relation can hold
	inexact bool // a constant or join the server can't match exactly as jaala does was left out
}

// compile writes c as Cypher, or reports it can't: an atom whose relation the store doesn't hold,
// a fixed argument (a node's kind) left to a variable in a relation with several labels, or a Props
// key that isn't a constant.
func (s *Store) compile(c ns.Conjunction) (*compiled, bool) {
	k := &compiled{params: map[string]any{}, vars: map[int][]occurrence{}}
	declared := map[string]bool{}
	anon := 0
	node := func(a ns.PushArg) (string, bool) {
		switch {
		case a.Var > 0:
			return "n" + strconv.Itoa(a.Var), true
		case a.Const != nil:
			if a.Const.Absent || a.Const.Num != nil {
				return "", false
			}
			anon++
			n := "c" + strconv.Itoa(anon)
			k.where = append(k.where, n+".id = "+k.param(a.Const.S))
			return n, true
		}
		anon++
		return "w" + strconv.Itoa(anon), true
	}
	pattern := func(n string) string {
		if declared[n] {
			return "(" + n + ")"
		}
		declared[n] = true
		return "(" + n + ":" + s.base + ")"
	}
	fixedVars := map[int]bool{}
	for i, atom := range c.Atoms {
		r, ok := s.rels[atom.Relation]
		if !ok || len(atom.Args) != r.schema.Arity {
			return nil, false
		}
		var m *match
		ms := r.matches()
		for j := range ms {
			fits := true
			for at, fixed := range ms[j].fixed() {
				a := atom.Args[at]
				switch {
				case a.Const != nil:
					fits = fits && !a.Const.Absent && a.Const.S == fixed
				case len(ms) > 1:
					return nil, false
				case a.Var > 0:
					fixedVars[a.Var] = true
				}
			}
			if fits {
				m = &ms[j]
				break
			}
		}
		if m == nil {
			k.empty = true
			continue
		}
		key := "a" + strconv.Itoa(i)
		switch {
		case m.node != nil:
			n, ok := node(atom.Args[m.node.Key])
			if !ok {
				return nil, false
			}
			k.matches = append(k.matches, "MATCH "+pattern(n))
			k.where = append(k.where, n+":"+quote(m.node.Label))
			k.occur(atom.Args[m.node.Key], occurrence{expr: n + ".id", kind: keyOcc, node: n})
			for p, at := range m.node.Props {
				if at != m.node.Key {
					k.arg(atom.Args[at], n, p, r.textual(at))
				}
			}
			k.returns = append(k.returns, n+".id AS i"+strconv.Itoa(i), "properties("+n+") AS "+key)
			k.cites = append(k.cites, n+"."+quote("_cites:"+m.node.Label))
			nd, idx := m.node, i
			k.atoms = append(k.atoms, func(rec *neo4j.Record) (ns.Tuple, error) {
				return nodeTuple(r, nd, recString(rec, "i"+strconv.Itoa(idx)), recMap(rec, "a"+strconv.Itoa(idx)))
			})
		case m.edge != nil:
			from, ok1 := node(atom.Args[m.edge.From])
			to, ok2 := node(atom.Args[m.edge.To])
			if !ok1 || !ok2 {
				return nil, false
			}
			e := "e" + strconv.Itoa(i)
			k.matches = append(k.matches, "MATCH "+pattern(from)+"-["+e+":"+quote(m.edge.Type)+"]->"+pattern(to))
			k.occur(atom.Args[m.edge.From], occurrence{expr: from + ".id", kind: keyOcc, node: from})
			k.occur(atom.Args[m.edge.To], occurrence{expr: to + ".id", kind: keyOcc, node: to})
			for p, at := range m.edge.Props {
				k.arg(atom.Args[at], e, p, r.textual(at))
			}
			k.returns = append(k.returns, from+".id AS f"+strconv.Itoa(i), to+".id AS t"+strconv.Itoa(i), "properties("+e+") AS "+key)
			k.cites = append(k.cites, e+"._cites")
			ed, idx := m.edge, i
			k.atoms = append(k.atoms, func(rec *neo4j.Record) (ns.Tuple, error) {
				return edgeTuple(r, ed, recString(rec, "f"+strconv.Itoa(idx)), recString(rec, "t"+strconv.Itoa(idx)), recMap(rec, "a"+strconv.Itoa(idx)))
			})
		default:
			p := m.props
			kc := atom.Args[p.Key].Const
			if kc == nil || kc.Absent || kc.Num != nil && !r.textual(p.Key) {
				return nil, false
			}
			if reserved(kc.S) != nil || slices.Contains(r.labelled, kc.S) {
				k.empty = true
				continue
			}
			n, ok := node(atom.Args[p.Node])
			if !ok {
				return nil, false
			}
			k.matches = append(k.matches, "MATCH "+pattern(n))
			k.where = append(k.where, n+"."+quote(kc.S)+" IS NOT NULL")
			k.occur(atom.Args[p.Node], occurrence{expr: n + ".id", kind: keyOcc, node: n})
			k.arg(atom.Args[p.Value], n, kc.S, r.textual(p.Value))
			k.returns = append(k.returns, n+".id AS i"+strconv.Itoa(i), "properties("+n+") AS "+key)
			k.cites = append(k.cites, n+"."+quote("_cites."+kc.S))
			pr, name, idx := p, kc.S, i
			k.atoms = append(k.atoms, func(rec *neo4j.Record) (ns.Tuple, error) {
				return propTuple(r, pr, recString(rec, "i"+strconv.Itoa(idx)), name, recMap(rec, "a"+strconv.Itoa(idx)))
			})
		}
	}
	for v := range fixedVars {
		if len(k.vars[v]) > 0 {
			return nil, false // a kind read elsewhere too: rare, and the engine joins it
		}
	}
	for _, occs := range k.vars {
		rep := occs[0]
		for _, o := range occs[1:] {
			k.join(rep, o)
		}
	}
	for _, cmp := range c.Compare {
		k.compare(cmp)
	}
	return k, true
}

func (k *compiled) param(v any) string {
	name := "p" + strconv.Itoa(len(k.params))
	k.params[name] = v
	return "$" + name
}

// occur records where a variable sits, keeping a node's id first, since it is never absent and
// compares exactly with anything.
func (k *compiled) occur(a ns.PushArg, o occurrence) {
	if a.Var == 0 {
		return
	}
	occs := k.vars[a.Var]
	if o.kind == keyOcc && len(occs) > 0 && occs[0].kind != keyOcc {
		occs = append([]occurrence{o}, occs...)
	} else {
		occs = append(occs, o)
	}
	k.vars[a.Var] = occs
}

// arg places a property's argument: a variable's occurrence, or a constant's condition when the
// server matches it as jaala does (text, or absent), and otherwise leaves it to the engine.
func (k *compiled) arg(a ns.PushArg, owner, prop string, text bool) {
	expr := owner + "." + quote(prop)
	kind := numericOcc
	if text {
		kind = textOcc
	}
	switch {
	case a.Var > 0:
		k.occur(a, occurrence{expr: expr, kind: kind, prop: prop, owner: owner})
	case a.Const != nil:
		switch c := *a.Const; {
		case c.Absent:
			k.where = append(k.where, expr+" IS NULL")
		case c.Num == nil || text:
			// Text, or a number in a text argument, which jaala reads by its text.
			k.where = append(k.where, expr+" = "+k.param(c.S))
		default:
			// A number meets a number by value, folded when the vocabulary folds, and NaN meets NaN:
			// the server can't, so it returns more.
			k.inexact = true
		}
	}
}

// join writes the condition that o holds rep's value, when the server compares the two as jaala does.
// Two node ids are one node already. Text meets text, or a number never; two properties that may
// both hold numbers can't be joined exactly (NaN, and units when the vocabulary folds), so the server
// returns more.
func (k *compiled) join(rep, o occurrence) {
	switch {
	case rep.kind == keyOcc && o.kind == keyOcc:
		if rep.node != o.node {
			k.where = append(k.where, rep.expr+" = "+o.expr)
		}
	case rep.kind == numericOcc && o.kind == numericOcc:
		k.inexact = true
	case rep.kind == keyOcc || o.kind == keyOcc:
		k.where = append(k.where, rep.expr+" = "+o.expr)
	default:
		k.where = append(k.where, "("+rep.expr+" = "+o.expr+" OR ("+rep.expr+" IS NULL AND "+o.expr+" IS NULL))")
	}
}

// compare writes a comparison the server evaluates as jaala does: = or != between text and text. A
// comparison is the engine's to check again, so the rest are left to it.
func (k *compiled) compare(c ns.PushComparison) {
	if c.Op != "=" && c.Op != "!=" {
		return
	}
	side := func(a ns.PushArg) (string, bool, bool) { // expression, may be absent, ok
		if a.Const != nil {
			if a.Const.Absent || a.Const.Num != nil {
				return "", false, false
			}
			return k.param(a.Const.S), false, true
		}
		occs := k.vars[a.Var]
		if len(occs) == 0 || occs[0].kind == numericOcc {
			return "", false, false
		}
		return occs[0].expr, occs[0].kind == textOcc, true
	}
	l, lnull, ok1 := side(c.Left)
	r, rnull, ok2 := side(c.Right)
	if !ok1 || !ok2 {
		return
	}
	if c.Op == "=" {
		if lnull && rnull {
			k.where = append(k.where, "("+l+" = "+r+" OR ("+l+" IS NULL AND "+r+" IS NULL))")
		} else {
			k.where = append(k.where, l+" = "+r)
		}
		return
	}
	switch {
	case lnull && rnull:
		k.where = append(k.where, "NOT ("+l+" = "+r+" OR ("+l+" IS NULL AND "+r+" IS NULL))")
	case lnull || rnull:
		k.where = append(k.where, "("+l+" IS NULL OR "+r+" IS NULL OR "+l+" <> "+r+")")
	default:
		k.where = append(k.where, l+" <> "+r)
	}
}

func (k *compiled) text(prefix string, where []string) string {
	q := prefix + strings.Join(k.matches, " ")
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	return q
}

func recMap(rec *neo4j.Record, key string) map[string]any {
	v, _ := rec.Get(key)
	m, _ := v.(map[string]any)
	return m
}

// pageable is whether c's page orders by node ids only, which _ord orders as jaala does, with its
// cursor's values text.
func (k *compiled) pageable(p *ns.Page) bool {
	for _, v := range p.Columns {
		if occs := k.vars[v]; len(occs) == 0 || occs[0].kind != keyOcc {
			return false
		}
	}
	for i, o := range p.Order {
		if occs := k.vars[o.Var]; len(occs) == 0 || occs[0].kind != keyOcc || !slices.Contains(p.Columns, o.Var) {
			return false
		}
		if i < len(p.After) && (p.After[i].Absent || p.After[i].Num != nil) {
			return false
		}
	}
	return len(p.After) == 0 || len(p.After) == len(p.Order)
}

// Pushes reports whether the store answers c as one query: every atom over a relation it holds,
// with one label or type each, no Partial, and a page only over node ids. Filters aren't sent to a
// store without FilterSource, so none arrive. Options.LookupsOnly refuses every conjunction.
func (s *Store) Pushes(c ns.Conjunction) bool {
	if s.lookupsOnly || c.Partial || c.PrefixesOnly || len(c.Filter) > 0 {
		return false
	}
	k, ok := s.compile(c)
	if !ok {
		return false
	}
	return c.Page == nil || k.pageable(c.Page)
}

// Push answers c with one query, a MATCH per atom and the conditions the server matches as jaala
// does; what it leaves out (a number's join or constant, an ordering comparison) only lets more rows
// through, which the engine checks. With Inputs, the bindings are unwound on the server and a row
// matching several comes back once. With a Page, a first query takes the page's rows in jaala's
// order (by _ord) and a second returns every match behind them.
func (s *Store) Push(ctx context.Context, c ns.Conjunction) iter.Seq2[[]ns.Tuple, error] {
	return func(yield func([]ns.Tuple, error) bool) {
		k, ok := s.compile(c)
		if !ok {
			yield(nil, fmt.Errorf("bolt: the store doesn't push this conjunction"))
			return
		}
		if k.empty {
			return
		}
		where := slices.Clone(k.where)
		prefix := ""
		params := k.params
		if len(c.Inputs) > 0 {
			prefix = "UNWIND $bindings AS b "
			for j, v := range c.Inputs {
				occs := k.vars[v]
				if len(occs) > 0 && occs[0].kind != numericOcc {
					where = append(where, "(b["+strconv.Itoa(j)+"] IS NULL OR "+occs[0].expr+" = b["+strconv.Itoa(j)+"])")
				}
			}
			var bs [][]any
			for _, bnd := range c.Bindings {
				row := make([]any, len(bnd))
				for j, v := range bnd {
					if !v.Absent && v.Num == nil {
						row[j] = v.S
					}
				}
				bs = append(bs, row)
			}
			params = maps(params, "bindings", bs)
		}
		if c.Page != nil {
			rows, err := s.pageRows(ctx, k, c.Page, prefix, where, params)
			if err != nil {
				yield(nil, err)
				return
			}
			if len(rows) == 0 {
				return
			}
			var cols []string
			for _, v := range c.Page.Columns {
				cols = append(cols, k.vars[v][0].expr)
			}
			where = append(where, "["+strings.Join(cols, ", ")+"] IN $pagerows")
			params = maps(params, "pagerows", rows)
		}
		q := k.text(prefix, where) + " RETURN " + strings.Join(k.returns, ", ")
		seen := map[string]bool{}
		dedupe := len(c.Inputs) > 0
		s.stream(ctx, q, params, func(rec *neo4j.Record) bool {
			row := make([]ns.Tuple, len(k.atoms))
			for i, build := range k.atoms {
				t, err := build(rec)
				if err != nil {
					return yield(nil, err) && false
				}
				row[i] = t
			}
			if dedupe {
				key := rowKey(row)
				if seen[key] {
					return true
				}
				seen[key] = true
			}
			return yield(row, nil)
		}, func(err error) { yield(nil, err) })
	}
}

// pageRows are the page's answer rows, each its Columns' node ids, in jaala's order: the Order keys'
// _ord (reversed by Desc), then the Columns', after the cursor.
func (s *Store) pageRows(ctx context.Context, k *compiled, p *ns.Page, prefix string, where []string, params map[string]any) ([][]any, error) {
	ord := func(v int) string { return k.vars[v][0].node + "._ord" }
	if len(p.After) > 0 {
		// Strictly after the cursor: the first key past it, or equal and the next past it, and so on.
		var alts []string
		for i := range p.Order {
			var parts []string
			for j := 0; j < i; j++ {
				parts = append(parts, ord(p.Order[j].Var)+" = "+paramIn(params, ordKey(p.After[j].S)))
			}
			op := " > "
			if p.Order[i].Desc {
				op = " < "
			}
			parts = append(parts, ord(p.Order[i].Var)+op+paramIn(params, ordKey(p.After[i].S)))
			alts = append(alts, "("+strings.Join(parts, " AND ")+")")
		}
		where = append(slices.Clone(where), "("+strings.Join(alts, " OR ")+")")
	}
	var with, by, ids []string
	seen := map[int]bool{}
	add := func(v int) {
		if !seen[v] {
			seen[v] = true
			with = append(with, ord(v)+" AS o"+strconv.Itoa(v), k.vars[v][0].expr+" AS k"+strconv.Itoa(v))
		}
	}
	for _, o := range p.Order {
		add(o.Var)
		key := "o" + strconv.Itoa(o.Var)
		if o.Desc {
			key += " DESC"
		}
		by = append(by, key)
	}
	for _, v := range p.Columns {
		add(v)
		by = append(by, "o"+strconv.Itoa(v))
		ids = append(ids, "k"+strconv.Itoa(v))
	}
	q := k.text(prefix, where) + " WITH DISTINCT " + strings.Join(with, ", ") +
		" ORDER BY " + strings.Join(by, ", ") + " LIMIT " + strconv.Itoa(p.Limit) +
		" RETURN [" + strings.Join(ids, ", ") + "] AS row"
	var rows [][]any
	var ferr error
	s.stream(ctx, q, params, func(rec *neo4j.Record) bool {
		v, _ := rec.Get("row")
		row, _ := v.([]any)
		rows = append(rows, row)
		return true
	}, func(err error) { ferr = err })
	return rows, ferr
}

func paramIn(params map[string]any, v any) string {
	name := "q" + strconv.Itoa(len(params))
	params[name] = v
	return "$" + name
}

func maps(m map[string]any, k string, v any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for a, b := range m {
		out[a] = b
	}
	out[k] = v
	return out
}

func rowKey(row []ns.Tuple) string {
	var b strings.Builder
	for _, t := range row {
		b.WriteString(factKey(t.Vals))
		b.WriteString("\x00")
	}
	return b.String()
}

// Summarizes reports whether the store reduces c's matches as s asks: a count and groups or values
// over a conjunction whose every constant and join the server matches exactly as jaala does, since
// the engine can't check a summary's matches again. Extremes, a page, Inputs and Partial are
// refused, and the engine asks otherwise.
func (s *Store) Summarizes(c ns.Conjunction, sm ns.Summary) bool {
	if s.lookupsOnly || len(sm.Extremes) > 0 || c.Page != nil || len(c.Inputs) > 0 || c.Partial || c.PrefixesOnly || len(c.Filter) > 0 {
		return false
	}
	k, ok := s.compile(c)
	if !ok || k.inexact {
		return false
	}
	for _, v := range slices.Concat(sm.Group, sm.Values) {
		if len(k.vars[v]) == 0 {
			return false
		}
	}
	return true
}

// Summarize answers c as rows grouped by the values at s's Group and Values: each value with its
// unit and spelling, so only identical values share a row, how many matches it covers, and their
// facts' citations.
func (s *Store) Summarize(ctx context.Context, c ns.Conjunction, sm ns.Summary) iter.Seq2[ns.SummaryRow, error] {
	return func(yield func(ns.SummaryRow, error) bool) {
		k, ok := s.compile(c)
		if !ok {
			yield(ns.SummaryRow{}, fmt.Errorf("bolt: the store doesn't summarize this conjunction"))
			return
		}
		if k.empty {
			return
		}
		cols := slices.Concat(sm.Group, sm.Values)
		var ret []string
		for j, v := range cols {
			o := k.vars[v][0]
			j := strconv.Itoa(j)
			if o.kind == keyOcc {
				ret = append(ret, o.expr+" AS v"+j)
			} else {
				ret = append(ret, o.expr+" AS v"+j, o.owner+"."+quote(unitProp(o.prop))+" AS u"+j, o.owner+"."+quote(textProp(o.prop))+" AS t"+j)
			}
		}
		ret = append(ret, "count(*) AS m", "collect(["+strings.Join(k.cites, ", ")+"]) AS cs")
		q := k.text("", k.where) + " RETURN " + strings.Join(ret, ", ")
		s.stream(ctx, q, k.params, func(rec *neo4j.Record) bool {
			var row ns.SummaryRow
			for j, v := range cols {
				o := k.vars[v][0]
				js := strconv.Itoa(j)
				var val ns.Value
				if o.kind == keyOcc {
					val = ns.S(recString(rec, "v"+js))
				} else {
					props := map[string]any{}
					for name, key := range map[string]string{"x": "v", unitProp("x"): "u", textProp("x"): "t"} {
						if x, ok := rec.Get(key + js); ok && x != nil {
							props[name] = x
						}
					}
					var err error
					if val, err = get(props, "x"); err != nil {
						return yield(ns.SummaryRow{}, err) && false
					}
				}
				if j < len(sm.Group) {
					row.Group = append(row.Group, val)
				} else {
					row.Values = append(row.Values, val)
				}
			}
			m, _ := rec.Get("m")
			row.Matches = int(m.(int64))
			cs, _ := rec.Get("cs")
			row.Cites = flatten(cs)
			return yield(row, nil)
		}, func(err error) { yield(ns.SummaryRow{}, err) })
	}
}

// flatten gathers the citations a collect of per-atom lists holds, each once.
func flatten(v any) []string {
	var out []string
	var walk func(any)
	walk = func(x any) {
		switch y := x.(type) {
		case []any:
			for _, z := range y {
				walk(z)
			}
		case string:
			out = append(out, y)
		}
	}
	walk(v)
	slices.Sort(out)
	return slices.Compact(out)
}

// nodeTuple, edgeTuple and propTuple read a fact back from a node's or relationship's properties.
func nodeTuple(r *relation, n *cypher.Node, id string, props map[string]any) (ns.Tuple, error) {
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

func edgeTuple(r *relation, e *cypher.Edge, from, to string, props map[string]any) (ns.Tuple, error) {
	t := ns.Tuple{Vals: make([]ns.Value, r.schema.Arity)}
	t.Vals[e.From], t.Vals[e.To] = ns.S(from), ns.S(to)
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

func propTuple(r *relation, p *cypher.PropRelation, id, key string, props map[string]any) (ns.Tuple, error) {
	t := ns.Tuple{Vals: make([]ns.Value, r.schema.Arity)}
	t.Vals[p.Node], t.Vals[p.Key] = ns.S(id), ns.S(key)
	v, err := get(props, key)
	if err != nil {
		return t, err
	}
	t.Vals[p.Value] = v
	t.ID, t.Cites = meta(props, "_id."+key, "_cites."+key)
	return t, nil
}
