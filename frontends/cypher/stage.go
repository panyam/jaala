package cypher

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/ns"
)

// A world is what every stage of one Compile shares: the vocabulary and view, the helper names and
// rules written so far, and the parameters.
type world struct {
	v         *ns.Vocabulary
	g         Graph
	params    map[string]string // $name, bound by Program.Bind, to its goal variable
	paramVals map[string]ns.Value
	helpers   map[string]bool
	rules     []rule
	inRule    bool // rendering a rule, where a parameter can't go (datalog.Bind binds the goal)
	err       *Error
	// cur is where in the Cypher the literals being compiled come from, for the rules a helper writes;
	// origins are the places render has marked, by the index its markers carry (#353).
	cur     Pos
	origins []Pos
	marks   [3]byte // chooseMarks'
	marking bool
	// names are jaala variables by the Cypher they stand for: ?p for p, ?p_loc for p.loc.
	names map[string]string
}

// A compiler compiles one stage: the clauses up to a WITH or the RETURN. Each stage is a rule (the
// last, the goal), so its variables are its own; what the WITH before it hands on arrives as an
// atom over that WITH's rule.
type compiler struct {
	*world
	used   map[string]bool // jaala variable names taken in this stage
	n      int
	nodes  map[string]*nodeVar
	order  []*nodeVar
	rels   map[string]Pos    // relationship variables, which nothing may read (#279)
	values map[string]string // value variables (a WITH's or a YIELD's) to their jaala variable
	vorder []string
	shared []frag  // literals every disjunct holds
	scope  *[]frag // where a property read through Props or a computed value goes
	kv     map[*[]frag]map[string]string
}

func (w *world) stage() *compiler {
	return &compiler{world: w, used: map[string]bool{}, nodes: map[string]*nodeVar{}, rels: map[string]Pos{},
		values: map[string]string{}}
}

// carried is a variable a WITH hands on: a node, with the labels it has, or a value.
type carried struct {
	name   string
	node   bool
	labels []string
}

type stageIn struct {
	rel   string
	items []carried
}

// projected is a stage compiled: its columns, the literals every row satisfies, and the answer's
// order and page.
type projected struct {
	cols  []column
	lits  []frag
	order []string
	limit *int
	skip  *int
}

func (w *world) compile(st statement) (Program, *Error) {
	var outs []projected
	for i, q := range st.parts {
		out, err := w.query(q)
		if err != nil {
			return Program{}, err
		}
		if len(st.parts) > 1 && (out.order != nil || out.limit != nil || out.skip != nil) {
			return Program{}, gapLater.at(q.stages[len(q.stages)-1].proj.pos, "ORDER BY, SKIP or LIMIT in a UNION part")
		}
		if i > 0 && strings.Join(names(out.cols), ", ") != strings.Join(names(outs[0].cols), ", ") {
			return Program{}, errAt(q.pos, "every UNION part returns the same columns: %s, not %s",
				strings.Join(names(outs[0].cols), ", "), strings.Join(names(out.cols), ", "))
		}
		outs = append(outs, out)
	}
	if len(outs) == 1 {
		return w.write(outs[0])
	}
	// A union is one relation, a clause per part; a part that aggregates gets a rule of its own first,
	// since an aggregating head must be its relation's only rule.
	name := w.helper("union")
	used := map[string]bool{}
	var vars []string
	for _, col := range outs[0].cols {
		vars = append(vars, "?"+freshIn(used, col.name))
	}
	head := name + "(" + strings.Join(vars, ", ") + ")"
	for _, out := range outs {
		cols := frag{}
		agg := false
		for i, col := range out.cols {
			if i > 0 {
				cols = append(cols, ", ")
			}
			cols = append(cols, col.text)
			agg = agg || col.agg
		}
		if !agg {
			w.rules = append(w.rules, rule{head: append(append(frag{name + "("}, cols...), ")"), body: out.lits})
			continue
		}
		part := w.helper("part")
		w.rules = append(w.rules, rule{head: append(append(frag{part + "("}, cols...), ")"), body: out.lits})
		w.rules = append(w.rules, rule{head: frag{head}, body: []frag{{part + "(" + strings.Join(vars, ", ") + ")"}}})
	}
	goal := projected{lits: []frag{{head}}}
	for i, col := range outs[0].cols {
		goal.cols = append(goal.cols, column{name: col.name, text: frag{vars[i]}})
	}
	return w.write(goal)
}

func names(cols []column) []string {
	var out []string
	for _, c := range cols {
		out = append(out, c.name)
	}
	return out
}

func freshIn(used map[string]bool, base string) string {
	base = sanitize(base)
	name := base
	for i := 1; used[name]; i++ {
		name = fmt.Sprintf("%s%d", base, i)
	}
	used[name] = true
	return name
}

// query compiles one UNION part, a rule per WITH, and returns its last stage, which RETURN closes.
func (w *world) query(q query) (projected, *Error) {
	var in *stageIn
	var where expr
	for _, s := range q.stages {
		c := w.stage()
		out, err := c.compileStage(in, s, where)
		if err != nil {
			return projected{}, err
		}
		if !s.proj.with {
			return out, nil
		}
		if out.order != nil || out.limit != nil || out.skip != nil {
			return projected{}, gapLater.at(s.proj.pos, "ORDER BY, SKIP or LIMIT inside a WITH: keep them on the RETURN")
		}
		items, err := c.carry(s.proj)
		if err != nil {
			return projected{}, err
		}
		name := w.helper("with")
		head := frag{name + "("}
		for i, col := range out.cols {
			if i > 0 {
				head = append(head, ", ")
			}
			head = append(head, col.text)
		}
		w.rules = append(w.rules, rule{head: append(head, ")"), body: out.lits})
		in, where = &stageIn{rel: name, items: items}, s.proj.where
	}
	panic("cypher: a query with no RETURN parsed")
}

// carry names what a WITH hands on: each item's alias, or the variable it is, a node staying a node.
func (c *compiler) carry(r returnClause) ([]carried, *Error) {
	items := r.items
	if r.star {
		items = c.starItems(r.pos)
	}
	var out []carried
	seen := map[string]bool{}
	for _, it := range items {
		name := it.alias
		v, isVar := it.e.(varRef)
		if name == "" {
			if !isVar {
				return nil, errAt(it.e.at(), "WITH %s needs a name: write WITH %s AS name", it.text, it.text)
			}
			name = v.name
		}
		if seen[name] {
			return nil, errAt(it.e.at(), "WITH names %s twice", name)
		}
		seen[name] = true
		cr := carried{name: name}
		if isVar {
			if n, ok := c.nodes[v.name]; ok {
				cr.node, cr.labels = true, append([]string(nil), n.labels...)
			}
		}
		out = append(out, cr)
	}
	return out, nil
}

func (c *compiler) starItems(pos Pos) []returnItem {
	var items []returnItem
	for _, n := range c.order {
		if !strings.HasPrefix(n.name, "\x00") {
			items = append(items, returnItem{e: varRef{pos: pos, name: n.name}, text: n.name})
		}
	}
	for _, v := range c.vorder {
		items = append(items, returnItem{e: varRef{pos: pos, name: v}, text: v})
	}
	return items
}

// compileStage compiles s, starting from what the WITH before it handed on (in) and filtered by that
// WITH's where.
func (c *compiler) compileStage(in *stageIn, s stage, where expr) (projected, *Error) {
	c.scope = &c.shared
	var conds []expr
	if in != nil {
		atom := frag{in.rel + "("}
		for i, it := range in.items {
			if i > 0 {
				atom = append(atom, ", ")
			}
			if it.node {
				n := &nodeVar{name: it.name, jv: c.fresh(it.name), labels: it.labels, slots: map[string]*slot{},
					endpoint: true, carried: true}
				c.name(n.jv, it.name)
				c.nodes[it.name] = n
				c.order = append(c.order, n)
				atom = append(atom, keyArg{n})
			} else {
				c.addValue(it.name)
				atom = append(atom, "?"+c.values[it.name])
			}
		}
		c.shared = append(c.shared, append(atom, ")"))
	}
	type relAt struct {
		r        relPat
		from, to *nodeVar
	}
	var rels []relAt
	var calls []frag // after the patterns, which bind what a call reads: Naive runs a call where it's written
	addWhere := func(e expr) *Error {
		for _, e := range conjuncts(e) {
			if h, ok := e.(hasLabel); ok {
				if err := c.declareLabels(h); err != nil {
					return err
				}
				continue
			}
			conds = append(conds, e)
		}
		return nil
	}
	if where != nil {
		if err := addWhere(where); err != nil {
			return projected{}, err
		}
	}
	for _, rc := range s.clauses {
		if rc.call != nil {
			c.cur = rc.call.pos
			f, err := c.callClause(*rc.call)
			if err != nil {
				return projected{}, err
			}
			calls = append(calls, at(rc.call.pos, f))
			if rc.call.where != nil {
				if err := addWhere(rc.call.where); err != nil {
					return projected{}, err
				}
			}
			continue
		}
		m := rc.match
		for _, pat := range m.patterns {
			ns := make([]*nodeVar, len(pat.nodes))
			for i, np := range pat.nodes {
				n, err := c.declareNode(np, &conds)
				if err != nil {
					return projected{}, err
				}
				ns[i] = n
			}
			for i, r := range pat.rels {
				if err := c.declareRel(r); err != nil {
					return projected{}, err
				}
				ns[i].endpoint, ns[i+1].endpoint = true, true
				rels = append(rels, relAt{r, ns[i], ns[i+1]})
			}
		}
		if m.where != nil {
			if err := addWhere(m.where); err != nil {
				return projected{}, err
			}
		}
	}
	for _, n := range c.order {
		for _, l := range n.labels {
			// A carried node was matched already, so its label atom is needed only to read a column.
			c.shared = append(c.shared, at(n.pos, frag{nodeAtm{n: n, label: l, lazy: n.carried}}))
		}
	}
	for _, r := range rels {
		c.cur = r.r.pos
		f, err := c.relLiteral(r.r, r.from, r.to)
		if err != nil {
			return projected{}, err
		}
		c.shared = append(c.shared, at(r.r.pos, f))
	}
	c.shared = append(c.shared, calls...)

	// Equalities every row holds become constants in atoms where nothing else reads the value.
	var rest []expr
	var eqs []pending
	for _, e := range conds {
		b, ok := e.(binary)
		if !ok || b.op != "=" {
			rest = append(rest, e)
			continue
		}
		ref, val := b.l, b.r
		if !isConst(val) {
			ref, val = b.r, b.l
		}
		if !isConst(val) {
			rest = append(rest, e)
			continue
		}
		if r, ok := ref.(varRef); ok && c.values[r.name] != "" {
			rest = append(rest, e)
			continue
		}
		vf, err := c.value(val)
		if err != nil {
			return projected{}, err
		}
		switch r := ref.(type) {
		case varRef:
			n, err := c.nodeRef(r)
			if err != nil {
				return projected{}, err
			}
			eqs = append(eqs, pending{n: n, val: vf, at: startOf(b)})
			continue
		case propRef:
			n, err := c.nodeRef(varRef{pos: r.pos, name: r.v})
			if err != nil {
				return projected{}, err
			}
			label, identity, kvRead, err := c.resolveProp(n, r)
			if err != nil {
				return projected{}, err
			}
			switch {
			case identity:
				eqs = append(eqs, pending{n: n, val: vf, at: startOf(b)})
			case !kvRead:
				eqs = append(eqs, pending{n: n, s: c.slotFor(n, label, r.key), key: r.key, val: vf, at: startOf(b)})
			default:
				c.shared = append(c.shared, at(startOf(b), c.kvAtom(n, r.key, vf)))
			}
			continue
		}
		rest = append(rest, e)
	}

	// The answer, then the conditions, so a value they read is known before the equalities settle.
	ret, err := c.returning(s.proj)
	if err != nil {
		return projected{}, err
	}
	disjuncts, derr := c.dnf(rest)
	if derr != nil {
		return projected{}, derr
	}
	bodies := make([][]frag, len(disjuncts))
	for i, d := range disjuncts {
		var lits []frag
		c.scope = &lits
		for _, cd := range d {
			c.cur = startOf(cd.e)
			from := len(lits)
			f, err := c.condition(cd.e, cd.neg)
			if err != nil {
				return projected{}, err
			}
			lits = append(lits, f)
			for i := from; i < len(lits); i++ {
				lits[i] = at(startOf(cd.e), lits[i])
			}
		}
		bodies[i] = lits
	}
	c.scope = &c.shared

	c.settle(eqs)
	for _, n := range c.order {
		if len(n.labels) == 0 && !n.endpoint {
			c.cur = n.pos
			f, err := c.anyNode(n)
			if err != nil {
				return projected{}, err
			}
			c.shared = append(c.shared, at(n.pos, f))
		}
	}
	out := projected{cols: ret.cols, order: ret.order, limit: ret.limit, skip: ret.skip}
	if len(bodies) == 1 {
		out.lits = append(append([]frag{}, c.shared...), bodies[0]...)
		return out, nil
	}
	// Several alternatives are a rule each over the variables the columns read.
	name := c.helper("match")
	var vars []string
	seen := map[string]bool{}
	for _, col := range ret.cols {
		for _, v := range varsIn(c.render(col.text)) {
			if !seen[v] {
				seen[v] = true
				vars = append(vars, v)
			}
		}
	}
	head := name + "(" + strings.Join(vars, ", ") + ")"
	for _, body := range bodies {
		c.rules = append(c.rules, rule{head: frag{head}, body: append(append([]frag{}, c.shared...), body...)})
	}
	out.lits = []frag{{head}}
	return out, nil
}

func (c *compiler) addValue(name string) {
	c.values[name] = c.fresh(name)
	c.vorder = append(c.vorder, name)
	c.name(c.values[name], name)
}

// callClause is CALL proc(args) YIELD ...: an atom over the predicate or relation proc names, its
// arguments first, each YIELD at the argument its label names, and the rest _.
func (c *compiler) callClause(cc callClause) (frag, *Error) {
	labels, ar, ok := procedure(c.v, cc.name)
	if !ok {
		return nil, errAt(cc.pos, "no procedure %s in this vocabulary: CALL reads one of its predicates or relations", cc.name)
	}
	if len(cc.args) > ar {
		return nil, errAt(cc.pos, "%s takes %d arguments in all, not %d", cc.name, ar, len(cc.args))
	}
	args := make([]any, ar)
	for i := range args {
		args[i] = "_"
	}
	for i, a := range cc.args {
		f, err := c.term(a)
		if err != nil {
			return nil, err
		}
		args[i] = f
	}
	label := func(i int) string {
		if i < len(labels) {
			return labels[i]
		}
		return ""
	}
	if b, ok := c.v.Predicate(cc.name); ok && len(b.Modes) > 0 {
		// The arguments are what the call binds, so a mode needing a later position can't run.
		runs := false
		for _, m := range b.Modes {
			ok := true
			for i, bound := range m {
				ok = ok && (!bound || i < len(cc.args))
			}
			runs = runs || ok
		}
		if !runs {
			var need []string
			for i, bound := range b.Modes[0] {
				if bound {
					need = append(need, label(i))
				}
			}
			return nil, errAt(cc.pos, "CALL %s needs its %s as arguments", cc.name, strings.Join(need, ", "))
		}
	}
	var outs []string
	for i := len(cc.args); i < ar; i++ {
		outs = append(outs, label(i))
	}
	yields := cc.yields
	if cc.star {
		for i := len(cc.args); i < ar; i++ {
			if label(i) == "" {
				return nil, errAt(cc.pos, "YIELD * needs every output of %s named, and its argument %d isn't", cc.name, i)
			}
			yields = append(yields, yieldItem{pos: cc.pos, name: label(i)})
		}
	}
	for k, y := range yields {
		at := -1
		for i := len(cc.args); i < ar; i++ {
			if strings.EqualFold(label(i), y.name) {
				at = i
			}
		}
		if at < 0 && len(labels) == 0 && len(cc.args)+k < ar {
			at = len(cc.args) + k
		}
		if at < 0 {
			return nil, errAt(y.pos, "%s yields %s, not %s", cc.name, strings.Join(outs, ", "), y.name)
		}
		name := y.alias
		if name == "" {
			name = y.name
		}
		if _, ok := c.nodes[name]; ok || c.values[name] != "" {
			return nil, errAt(y.pos, "YIELD %s names a variable the query has already", name)
		}
		c.addValue(name)
		args[at] = "?" + c.values[name]
	}
	f := frag{cc.name + "("}
	for i, a := range args {
		if i > 0 {
			f = append(f, ", ")
		}
		f = append(f, a)
	}
	return append(f, ")"), nil
}

// procedure is what CALL can read at name: a predicate, a derived relation or a base relation, with
// its arguments' names and its arity.
func procedure(v *ns.Vocabulary, name string) ([]string, int, bool) {
	if b, ok := v.Predicate(name); ok {
		return b.Labels, b.Arity, true
	}
	if sig, ok := v.Signature(name); ok {
		var labels []string
		for _, a := range sig {
			labels = append(labels, a.Name)
		}
		return labels, len(sig), true
	}
	if s, ok := v.Schema(name); ok {
		return s.Labels, s.Arity, true
	}
	return nil, 0, false
}

// constant writes a parameter's value as the constant jaala reads, for Params.
func (c *compiler) constant(name string, pos Pos) (frag, *Error) {
	v, ok := c.paramVals[name]
	switch {
	case !ok:
		return nil, errAt(pos, "no value for $%s in Params", name)
	case v.Absent:
		return nil, errAt(pos, "$%s is absent, which a query can't write as a constant: test the property with IS NULL", name)
	case v.Num != nil && v.S != "":
		if _, err := strconv.ParseFloat(v.S, 64); err == nil {
			return frag{v.S}, nil
		}
		return frag{strconv.FormatFloat(*v.Num, 'f', -1, 64)}, nil
	case v.Num != nil:
		return frag{strconv.FormatFloat(*v.Num, 'f', -1, 64)}, nil
	}
	return c.value(strLit{pos: pos, s: v.S})
}

func (w *world) write(goal projected) (Program, *Error) {
	prog := Program{Params: map[string]datalog.Var{}}
	for _, col := range goal.cols {
		prog.Columns = append(prog.Columns, col.name)
	}
	var b strings.Builder
	for _, r := range w.rules {
		w.inRule = true
		text := w.render(r.head) + " :- " + w.joinLits(r.body)
		if r.at.Line > 0 {
			text = w.mark(r.at, text)
		}
		b.WriteString(text + ";\n")
		w.inRule = false
	}
	cols := make([]string, len(goal.cols))
	for i, col := range goal.cols {
		cols[i] = w.render(col.text)
	}
	b.WriteString(w.joinLits(goal.lits))
	if w.err != nil {
		return Program{}, w.err
	}
	b.WriteString(" => ")
	b.WriteString(strings.Join(cols, ", "))
	if len(goal.order) > 0 {
		b.WriteString(" order by " + strings.Join(goal.order, ", "))
	}
	if goal.limit != nil {
		fmt.Fprintf(&b, " limit %d", *goal.limit)
	}
	if goal.skip != nil {
		fmt.Fprintf(&b, " offset %d", *goal.skip)
	}
	prog.Text, prog.spans = w.unmark(b.String())
	prog.names = w.names
	for name, jv := range w.params {
		prog.Params[name] = datalog.Var(jv)
	}
	return prog, nil
}

// joinLits writes a body's literals, leaving out a carried node's label atom nothing read through.
func (w *world) joinLits(lits []frag) string {
	var parts []string
	for _, l := range lits {
		if s := w.render(l); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}
