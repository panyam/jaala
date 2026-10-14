package datalog

import (
	"errors"
	"fmt"
	"sort"

	"github.com/panyam/jaala/ns"
)

// A Table is an answer as columns and rows of values, which EvalTable returns (#195). It holds what
// Eval's []Row does without a map per row, so a large answer costs its values. Rows, Cites, Witness
// and Why are indexed by row; a row's values, and its reasons, by column.
type Table struct {
	// Columns are the answer's columns as written: a variable, or "func(var)" for an aggregate, the
	// keys Row.Bind uses.
	Columns []Var
	Rows    [][]ns.Value
	// Cites are each row's provenance, as Row.Cites.
	Cites [][]string
	// Witness is each row's witness when the Eval asked for witnesses (see Witnesses), else nil.
	Witness [][]*Witness
	// Why is, for each row with an absent aggregate column, the reason per column (see Row.Why), and
	// nil for a row without one. It is nil when no row has one.
	Why [][]string
}

// Len is the number of rows, 0 for a nil Table.
func (t *Table) Len() int {
	if t == nil {
		return 0
	}
	return len(t.Rows)
}

// Row is row i as Eval returns it.
func (t *Table) Row(i int) Row {
	r := Row{Bind: make(map[Var]ns.Value, len(t.Columns)), Cites: t.Cites[i]}
	for j, c := range t.Columns {
		r.Bind[c] = t.Rows[i][j]
	}
	if t.Witness != nil {
		r.Witness = t.Witness[i]
	}
	if t.Why != nil && t.Why[i] != nil {
		for j, why := range t.Why[i] {
			if why != "" {
				if r.Why == nil {
					r.Why = map[Var]string{}
				}
				r.Why[t.Columns[j]] = why
			}
		}
	}
	return r
}

// rowsOf is Eval's answer from EvalTable's.
func rowsOf(t *Table, err error) ([]Row, error) {
	if t == nil {
		return nil, err
	}
	rows := make([]Row, len(t.Rows))
	for i := range rows {
		rows[i] = t.Row(i)
	}
	return rows, err
}

// answerGoal solves the goal into its answer: grouped and reduced when it aggregates, projected
// otherwise, then put in order, filtered by after and paged by offset and limit (#194).
func answerGoal(b *Base, pos, negs []Literal, having []Compare, sel, cols []Term, written Query, o evalOptions) (*Table, error) {
	labels := selKeys(cols)
	at := make(map[Var]int, len(labels))
	for i, l := range labels {
		if _, ok := at[l]; !ok {
			at[l] = i
		}
	}
	fixed := map[int]ns.Value{}
	for i, c := range cols {
		if vals := o.bind[c.Var]; len(vals) == 1 && c.Var != "" && c.Agg == nil {
			fixed[i] = vals[0] // as bound, not canonicalized (#148)
		}
	}
	ord := &order{canonic: b.canonical()}
	for _, ob := range written.OrderBy {
		ord.by = append(ord.by, at[colLabel(ob.Term)])
		ord.desc = append(ord.desc, ob.Desc)
	}
	for _, k := range selKeys(sel) {
		ord.deflt = append(ord.deflt, at[k])
	}
	var rows []*answerRow
	total := 0
	if hasAggregate(sel) || len(having) > 0 {
		ag := newAggregator(sel, having, true)
		lits := deferComparisons(pos, b.reg)
		summarized := false
		if g := goalGroup(b, pos, negs); g != nil {
			var err error
			if summarized, err = summarizeGoal(b, g, ag); err != nil {
				return nil, err
			}
		}
		if !summarized {
			if err := prefetch(b, lits, negs); err != nil {
				return nil, err
			}
			err := solve(lits, 0, newBinding(), b, func(bnd *binding) error {
				ok, err := passesNegations(bnd, negs, b)
				if ok {
					ag.add(bnd)
				}
				return err
			})
			if err != nil {
				return nil, err
			}
		}
		b.run.held = len(ag.groups)
		grouped, err := ag.rows()
		if err != nil {
			return nil, err
		}
		for _, g := range grouped {
			r := &answerRow{vals: make([]ns.Value, len(cols)), cites: g.Cites}
			for i, l := range labels {
				if v, ok := fixed[i]; ok {
					r.vals[i] = v
				} else {
					r.vals[i] = g.Bind[l]
				}
				if why := g.Why[l]; why != "" {
					if r.why == nil {
						r.why = make([]string, len(cols))
					}
					r.why[i] = why
				}
			}
			if written.After == nil || ord.after(r, written.After) {
				rows = append(rows, r)
			}
		}
		total = len(grouped)
		sortRows(rows, ord)
	} else {
		p := &projector{cols: labels, fixed: fixed, ord: ord, rows: map[string]*answerRow{}, cursor: written.After,
			witness: b.witnessing(), base: b, negs: negs}
		switch {
		case written.Limit > 0 && len(written.OrderBy) > 0:
			p.size = written.Limit + written.Offset
			p.page = &pageHeap{ord: ord}
		case written.Limit > 0:
			p.stopAt = written.Limit
		}
		if o.total != nil {
			p.total = map[string]bool{}
		}
		lits := deferComparisons(pos, b.reg)
		emit := func(bnd *binding) error {
			ok, err := passesNegations(bnd, negs, b)
			if err != nil || !ok {
				return err
			}
			return p.add(bnd)
		}
		paged := false
		if g := goalGroup(b, pos, negs); g != nil && written.Limit > 0 && o.total == nil {
			var err error
			if paged, err = pageGoal(b, g, cols, fixed, written, emit); err != nil {
				return nil, err
			}
		}
		if !paged {
			if p.stopAt == 0 { // a limit without an order by stops at its first rows, before most lookups
				if err := prefetch(b, lits, negs); err != nil {
					return nil, err
				}
			}
			err := solve(lits, 0, newBinding(), b, emit)
			if err != nil && !errors.Is(err, errEnough) {
				return nil, err
			}
		}
		b.run.held = p.held
		rows = p.answer()
		total = len(rows)
		if p.total != nil {
			total = len(p.total)
		}
	}
	if o.total != nil {
		*o.total = total
	}
	rows = rows[min(written.Offset, len(rows)):]
	if written.Limit > 0 && written.Limit < len(rows) {
		rows = rows[:written.Limit]
	}
	tab := &Table{Columns: labels, Rows: make([][]ns.Value, len(rows)), Cites: make([][]string, len(rows))}
	if o.witness {
		tab.Witness = make([][]*Witness, len(rows))
	}
	for i, r := range rows {
		tab.Rows[i], tab.Cites[i] = r.vals, r.cites
		if o.witness {
			tab.Witness[i] = r.wit
		}
		if r.why != nil {
			if tab.Why == nil {
				tab.Why = make([][]string, len(rows))
			}
			tab.Why[i] = r.why
		}
	}
	return tab, nil
}

// sortRows puts rows in the answer's order: stable, since grouped rows arrive in the default order and
// the order by columns break no tie the default order hasn't.
func sortRows(rows []*answerRow, ord *order) {
	sort.SliceStable(rows, func(i, j int) bool { return ord.compare(rows[i], rows[j]) < 0 })
}

// cursorOf is the after clause's values for a row the host passed to After: its values in the order
// by columns.
func cursorOf(q Query, row map[Var]ns.Value) ([]ns.Value, error) {
	if len(q.OrderBy) == 0 {
		return nil, fmt.Errorf("query: after needs an order by: it continues from the last row in that order")
	}
	out := make([]ns.Value, len(q.OrderBy))
	for i, ob := range q.OrderBy {
		v, ok := row[colLabel(ob.Term)]
		if !ok {
			return nil, fmt.Errorf("query: After's row has no %s, a column the order by reads", ob.Term)
		}
		out[i] = v
	}
	return out, nil
}
