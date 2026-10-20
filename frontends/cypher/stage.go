package cypher

import (
	"fmt"
	"slices"
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
	edgePaths map[string]bool // path variables relationships() reads (#280)
	rules     []rule
	inRule    bool              // rendering a rule, where a parameter can't go (datalog.Bind binds the goal)
	fixed     map[*nodeVar]frag // the rule being rendered's node keys an alternative's equality fixes
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
	rels   map[string]*relVar // relationship variables (#441)
	rorder []string
	values map[string]string // value variables (a WITH's or a YIELD's) to their jaala variable
	vorder []string
	shared []frag     // literals every disjunct holds
	opts   []*optPart // the stage's OPTIONAL MATCHes, in order (#282)
	// optValues are the value variables an OPTIONAL MATCH binds: a path's.
	optValues map[string]bool
	inPart    int     // the OPTIONAL MATCH whose WHERE is compiling, inside whose braces its nodes are bound
	scope     *[]frag // where a property read through Props or a computed value goes
	kv        map[*[]frag]map[string]string
}

// An optPart is one OPTIONAL MATCH (#282): the literals its optional { } holds (its nodes' labels,
// its relationships and its WHERE), and seen, a variable its braces bind to 1, which count over one of
// its nodes sums, so a row with nothing matched counts 0 as Cypher's count(b) skips a null b.
type optPart struct {
	pos   Pos
	conds []expr
	lits  []frag
	seen  string
}

func (w *world) stage() *compiler {
	return &compiler{world: w, used: map[string]bool{}, nodes: map[string]*nodeVar{}, rels: map[string]*relVar{},
		values: map[string]string{}}
}

// carried is a variable a WITH hands on: a node, with the labels it has, a relationship, as its fact
// with the types it may have (#445), or a value; absent says an OPTIONAL MATCH bound the node, so it
// may be absent in the next stage too.
type carried struct {
	name   string
	node   bool
	labels []string
	absent bool
	rel    []Edge
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
		items = append(c.starItems(r.pos), r.items...)
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
				cr.node, cr.labels, cr.absent = true, append([]string(nil), n.labels...), n.opt > 0 || n.absent
			}
			if rv := c.rels[v.name]; rv != nil && rv.jv != "" && !rv.opt {
				cr.rel = rv.es
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
	for _, r := range c.rorder {
		if c.rels[r].jv != "" {
			items = append(items, returnItem{e: varRef{pos: pos, name: r}, text: r})
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
					endpoint: true, carried: true, absent: it.absent}
				c.name(n.jv, it.name)
				c.nodes[it.name] = n
				c.order = append(c.order, n)
				atom = append(atom, keyArg{n})
			} else if it.rel != nil {
				rv := &relVar{name: it.name, jv: c.fresh(it.name), es: it.rel, byFact: true}
				c.name(rv.jv, it.name)
				c.rels[it.name] = rv
				c.rorder = append(c.rorder, it.name)
				atom = append(atom, "?"+rv.jv)
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
		opt      int // the OPTIONAL MATCH it's in, from 1
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
		if u := rc.unwind; u != nil {
			// UNWIND l AS x: a row per value, after the patterns that bind what l reads (#283)
			c.cur = u.pos
			l, err := c.term(u.e) // a term, as the atom's argument must be

			if err != nil {
				return projected{}, err
			}
			if c.nodes[u.name] != nil || c.values[u.name] != "" {
				return projected{}, errAt(u.pos, "%s is already bound", u.name)
			}
			c.addValue(u.name)
			calls = append(calls, at(u.pos, frag{"list.member(", l, ", _, ?" + c.values[u.name] + ")"}))
			continue
		}
		m := rc.match
		var part *optPart
		condsTo := &conds
		if m.optional {
			if len(c.order) == 0 {
				return projected{}, gapOptional.at(m.pos, "an OPTIONAL MATCH before any MATCH: start with a MATCH")
			}
			part = &optPart{pos: m.pos}
			c.opts = append(c.opts, part)
			condsTo = &part.conds
		}
		for _, pat := range m.patterns {
			ns := make([]*nodeVar, len(pat.nodes))
			for i, np := range pat.nodes {
				old := c.nodes[np.v]
				if part != nil && np.v != "" && old != nil && old.opt != len(c.opts) {
					for _, l := range np.labels {
						if !contains(old.labels, l) {
							return projected{}, gapOptional.at(np.pos, "a label an OPTIONAL MATCH adds to %s, which a match before it bound", np.v)
						}
					}
				}
				n, err := c.declareNode(np, condsTo)
				if err != nil {
					return projected{}, err
				}
				if part != nil && (np.v == "" || old == nil) {
					n.opt = len(c.opts)
				}
				ns[i] = n
			}
			if err := c.declarePath(&pat); err != nil {
				return projected{}, err
			}
			if part != nil && pat.path != "" {
				if c.optValues == nil {
					c.optValues = map[string]bool{}
				}
				c.optValues[pat.path] = true
			}
			for i, r := range pat.rels {
				if err := c.declareRel(r); err != nil {
					return projected{}, err
				}
				if rv := c.rels[r.v]; rv != nil && part != nil {
					rv.opt, rv.jv = true, "" // its fact would bind only inside the braces
				} else if rv != nil && r.path != "" {
					rv.byFact = true
				}
				// A relationship binds its ends, but one inside an OPTIONAL MATCH binds only the ends that
				// match declared: a node an earlier MATCH declared still needs its own atom outside
				// the braces (FuzzCompile found MATCH (a) OPTIONAL MATCH ()--(a) with none).
				for _, n := range ns[i : i+2] {
					if part == nil || n.opt == len(c.opts) {
						n.endpoint = true
					}
				}
				at := relAt{r: r, from: ns[i], to: ns[i+1]}
				if part != nil {
					at.opt = len(c.opts)
				}
				rels = append(rels, at)
			}
		}
		if m.where != nil && part != nil {
			for _, e := range conjuncts(m.where) {
				if h, ok := e.(hasLabel); ok {
					if n := c.nodes[h.v]; n == nil || n.opt != len(c.opts) {
						return projected{}, gapOptional.at(h.pos, "a label an OPTIONAL MATCH's WHERE tests on %s, which a match before it bound", h.v)
					}
					if err := c.declareLabels(h); err != nil {
						return projected{}, err
					}
					continue
				}
				part.conds = append(part.conds, e)
			}
		} else if m.where != nil {
			if err := addWhere(m.where); err != nil {
				return projected{}, err
			}
		}
	}
	for _, n := range c.order {
		for _, l := range n.labels {
			// A carried node was matched already, so its label atom is needed only to read a column,
			// and one that may be absent reads it optionally, so the row stays when it is.
			f := at(n.pos, frag{nodeAtm{n: n, label: l, lazy: n.carried}})
			if n.absent {
				f = frag{optBraces{lits: []frag{f}}}
			}
			c.into(n.opt, f)
		}
	}
	for _, r := range rels {
		c.cur = r.r.pos
		f, err := c.relLiteral(r.r, r.from, r.to)
		if err != nil {
			return projected{}, err
		}
		c.into(r.opt, at(r.r.pos, f))
		if rv := c.rels[r.r.v]; rv != nil && rv.byFact && r.r.path != "" {
			// the path's one relationship, read after the walk binding it, as Naive runs a call where written
			l := c.fresh(r.r.v + "_rels")
			c.shared = append(c.shared, frag{pathFuncs["relationships"] + "(?" + c.values[r.r.path] + ", ?" + l + "), list.at(?" + l + ", 0, ?" + rv.jv + ")"})
		}
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
		if r, ok := ref.(varRef); ok && (c.values[r.name] != "" || c.rels[r.name] != nil) {
			rest = append(rest, e)
			continue
		}
		if r, ok := ref.(propRef); ok && c.rels[r.v] != nil {
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
	returnedFrom := len(c.shared)
	ret, err := c.returning(s.proj)
	if err != nil {
		return projected{}, err
	}
	if err := c.optionalConditions(); err != nil {
		return projected{}, err
	}
	disjuncts, derr := c.dnf(rest)
	if derr != nil {
		return projected{}, derr
	}
	settled := map[*nodeVar]bool{}
	for _, p := range eqs {
		settled[p.n] = true
	}
	bodies := make([][]frag, len(disjuncts))
	fixes := make([]map[*nodeVar]fixedKey, len(disjuncts))
	for i, d := range disjuncts {
		var fix map[*nodeVar]fixedKey
		if len(disjuncts) > 1 {
			fix = c.keysFixed(d, settled)
		}
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
			for n, k := range fix {
				if k.e == cd.e {
					k.lits = append([]frag(nil), lits[from:]...)
					lits = lits[:from]
					fix[n] = k
				}
			}
		}
		bodies[i] = lits
		fixes[i] = fix
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
			c.into(n.opt, at(n.pos, f))
		}
	}
	c.closeOptionals(returnedFrom)
	out := projected{cols: ret.cols, order: ret.order, limit: ret.limit, skip: ret.skip}
	if len(bodies) == 1 {
		out.lits = append(append([]frag{}, c.shared...), bodies[0]...)
		return out, nil
	}
	// Several alternatives are a rule each over the variables the columns read and every node of the
	// match, so two bindings differing only in a node the columns don't read stay two tuples, which
	// count(*) and collect count (#439).
	name := c.helper("match")
	var vars []string
	seen := map[string]bool{}
	add := func(v string) {
		if !seen[v] {
			seen[v] = true
			vars = append(vars, v)
		}
	}
	for _, col := range ret.cols {
		for _, v := range varsIn(c.render(col.text)) {
			add(v)
		}
	}
	for _, n := range c.order {
		add("?" + n.jv)
	}
	head := name + "(" + strings.Join(vars, ", ") + ")"
	keys := map[string]*nodeVar{}
	for _, n := range c.order {
		keys["?"+n.jv] = n
	}
	written := frag{name + "("}
	for i, v := range vars {
		if i > 0 {
			written = append(written, ", ")
		}
		if n := keys[v]; n != nil {
			written = append(written, keyVal{n})
		} else {
			written = append(written, v)
		}
	}
	written = append(written, ")")
	for i, body := range bodies {
		lits := append(append([]frag{}, c.shared...), body...)
		var fixed map[*nodeVar]frag
		for _, n := range sortedFixes(fixes[i]) {
			k := fixes[i][n]
			if readsKey(lits, n) {
				lits = append(lits, k.lits...)
				continue
			}
			if fixed == nil {
				fixed = map[*nodeVar]frag{}
			}
			fixed[n] = k.val
		}
		c.rules = append(c.rules, rule{head: written, body: lits, fixed: fixed})
	}
	out.lits = []frag{{head}}
	return out, nil
}

// declarePath makes a pattern's path variable a value its one relationship's walk binds (#280): a
// pattern of one relationship, of one step or a variable length, read with graph.paths as a trail, or
// one shortest path per pair. A longer pattern, and a selector other than a shortest path, are gaps.
func (c *compiler) declarePath(pat *pattern) *Error {
	if pat.path == "" {
		return nil // a match answers its nodes, which any selector keeps
	}
	if _, ok := c.nodes[pat.path]; ok || c.values[pat.path] != "" {
		return errAt(pat.pathPos, "%s is already bound", pat.path)
	}
	if len(pat.rels) != 1 {
		return gapPaths.at(pat.pathPos, "a path variable over a pattern of %d relationships: bind a path of one relationship, which may have a variable length", len(pat.rels))
	}
	if pat.selector != "" && pat.selector != "shortest" {
		return gapPaths.at(pat.pathPos, "%s paths as values: write shortestPath or SHORTEST 1", strings.ToUpper(pat.selector))
	}
	r := &pat.rels[0]
	r.path, r.selector, r.edges = pat.path, pat.selector, c.edgePaths[pat.path]
	if r.v != "" {
		// the path's one relationship is the fact its walk names, so the walk keeps each edge's (#445)
		if r.varLen {
			return gapPaths.at(r.pos, "a variable bound to a variable-length relationship: read relationships(%s)", pat.path)
		}
		es, _, err := c.edges(*r)
		if err != nil {
			return err
		}
		for _, e := range es {
			if en, err := c.v.Lookup(e.Relation); err != nil || en.Kind != ns.EntryBase {
				return errAt(r.pos, "%s reads its relationship's id, and %s is derived, so its relationships have none: read nodes(%s)", r.v, e.Type, pat.path)
			}
		}
		r.edges = true
	}
	c.addValue(pat.path)
	return nil
}

// into adds f to the literals every disjunct holds, or to the k'th OPTIONAL MATCH's braces.
func (c *compiler) into(k int, f frag) {
	if k > 0 {
		c.opts[k-1].lits = append(c.opts[k-1].lits, f)
		return
	}
	c.shared = append(c.shared, f)
}

// optionalConditions compiles each OPTIONAL MATCH's WHERE into its braces, where a property it reads
// is read too, so a row whose optional part fails it keeps its row with the part absent.
func (c *compiler) optionalConditions() *Error {
	defer func() { c.scope, c.inPart = &c.shared, 0 }()
	for k, part := range c.opts {
		c.inPart = k + 1
		if len(part.conds) == 0 {
			continue
		}
		ds, err := c.dnf(part.conds)
		if err != nil {
			return err
		}
		if len(ds) != 1 {
			return gapOptional.at(part.pos, "an OR in an OPTIONAL MATCH's WHERE")
		}
		c.scope = &part.lits
		for _, cd := range ds[0] {
			c.cur = startOf(cd.e)
			f, err := c.condition(cd.e, cd.neg)
			if err != nil {
				return err
			}
			part.lits = append(part.lits, at(startOf(cd.e), f))
		}
	}
	return nil
}

// closeOptionals writes each OPTIONAL MATCH's braces into the literals every disjunct holds, and, of
// what RETURN read (c.shared from from), wraps each literal reading an optional part in braces of its
// own: a property read through Props, or a value computed from the part, is absent when the part
// is, as Cypher's null is, rather than dropping the row.
func (c *compiler) closeOptionals(from int) {
	if len(c.opts) == 0 && !slices.ContainsFunc(c.order, func(n *nodeVar) bool { return n.absent }) {
		return
	}
	optVars := map[string]bool{}
	for _, n := range c.order {
		if n.opt == 0 && !n.absent {
			continue
		}
		optVars["?"+n.jv] = true
		for _, s := range n.slots {
			if s.jv != "" {
				optVars["?"+s.jv] = true
			}
		}
	}
	for _, v := range c.vorder {
		if c.optValues[v] {
			optVars["?"+c.values[v]] = true
		}
	}
	shared := append([]frag(nil), c.shared[:from]...)
	var after []frag
	for _, f := range c.shared[from:] {
		if !c.readsOptional(f, optVars) {
			shared = append(shared, f)
			continue
		}
		for _, v := range varsIn(c.render(f)) {
			optVars[v] = true // what it binds is absent with it, for what reads it next
		}
		after = append(after, frag{optBraces{lits: []frag{f}}})
	}
	for _, part := range c.opts {
		lits := part.lits
		if part.seen != "" {
			lits = append(lits, frag{"?" + part.seen + " = 1 * 1"})
		}
		if len(lits) > 0 {
			shared = append(shared, at(part.pos, frag{optBraces{lits: lits}}))
		}
	}
	c.shared = append(shared, after...)
}

// readsOptional reports whether f reads a node an OPTIONAL MATCH declared, or one of vars.
func (c *compiler) readsOptional(f frag, vars map[string]bool) bool {
	for _, el := range f {
		switch el := el.(type) {
		case keyArg:
			if el.n.opt > 0 || el.n.absent {
				return true
			}
		case keyVal:
			if el.n.opt > 0 || el.n.absent {
				return true
			}
		case nodeAtm:
			if el.n.opt > 0 || el.n.absent {
				return true
			}
		case origin:
			if c.readsOptional(el.f, vars) {
				return true
			}
		case frag:
			if c.readsOptional(el, vars) {
				return true
			}
		case string:
			for _, v := range varsIn(el) {
				if vars[v] {
					return true
				}
			}
		}
	}
	return false
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
		w.inRule, w.fixed = true, r.fixed
		text := w.render(r.head) + " :- " + w.joinLits(r.body)
		if r.at.Line > 0 {
			text = w.mark(r.at, text)
		}
		b.WriteString(text + ";\n")
		w.inRule, w.fixed = false, nil
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

// A fixedKey is a node key one alternative's equality holds to a text constant: the constant, and the
// literals the equality compiled to, which the rule keeps when something in it reads the key's value.
type fixedKey struct {
	e    expr
	val  frag
	lits []frag
}

// keysFixed finds the node keys alternative d equates with one text constant, `h.id = "x"` from an
// IN or an OR, so its rule writes the constant in the key's place (#392): written as a comparison
// after the atoms, nothing bound a variable-length match's end, and its reach was derived over the
// whole graph. A node an equality every row holds settled already, one two equalities name, and a
// stand-in for an outer node are left as written.
func (c *compiler) keysFixed(d []cond, settled map[*nodeVar]bool) map[*nodeVar]fixedKey {
	var out map[*nodeVar]fixedKey
	twice := map[*nodeVar]bool{}
	for _, cd := range d {
		b, ok := cd.e.(binary)
		if cd.neg || !ok || b.op != "=" {
			continue
		}
		ref, val := b.l, b.r
		if _, ok := val.(strLit); !ok {
			ref, val = b.r, b.l
		}
		lit, ok := val.(strLit)
		if !ok {
			continue
		}
		var n *nodeVar
		switch r := ref.(type) {
		case varRef:
			n = c.nodes[r.name]
		case propRef:
			m := c.nodes[r.v]
			if m == nil {
				continue
			}
			if _, identity, _, err := c.resolveProp(m, r); err == nil && identity {
				n = m
			}
		}
		if n == nil || settled[n] || n.subst != nil || n.of != nil || twice[n] {
			continue
		}
		if _, ok := out[n]; ok {
			delete(out, n)
			twice[n] = true
			continue
		}
		vf, err := c.value(lit)
		if err != nil {
			continue
		}
		if out == nil {
			out = map[*nodeVar]fixedKey{}
		}
		out[n] = fixedKey{e: cd.e, val: at(startOf(b), vf)}
	}
	return out
}

// sortedFixes is fix's nodes in the order the stage matched them, so a rule is written the same way
// every time.
func sortedFixes(fix map[*nodeVar]fixedKey) []*nodeVar {
	var out []*nodeVar
	for n := range fix {
		out = append(out, n)
	}
	slices.SortFunc(out, func(a, b *nodeVar) int { return strings.Compare(a.jv, b.jv) })
	return out
}

// readsKey reports whether any of lits reads n's key as a value (a comparison or a call's
// argument), which a constant in its place would turn into a test of constants alone.
func readsKey(lits []frag, n *nodeVar) bool {
	for _, f := range lits {
		for _, el := range f {
			switch el := el.(type) {
			case keyVal:
				if el.n == n {
					return true
				}
			case frag:
				if readsKey([]frag{el}, n) {
					return true
				}
			case origin:
				if readsKey([]frag{el.f}, n) {
					return true
				}
			case braces:
				if readsKey(el.lits, n) {
					return true
				}
			}
		}
	}
	return false
}
