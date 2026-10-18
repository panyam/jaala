package cypher

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/ns"
)

// A Program is a Cypher query compiled to jaala: the program's text, parsed, the answer's columns as
// Cypher names them, and the goal variable each $parameter is, for datalog.Bind (Program.Bind).
type Program struct {
	Text    string
	Query   datalog.Query
	Columns []string
	Params  map[string]datalog.Var
}

// Bind is the datalog.Bind option giving each named $parameter its value. A name the query doesn't
// use is an error, as an unused Bind variable is.
func (p Program) Bind(values map[string]ns.Value) (datalog.Option, error) {
	vals := map[datalog.Var][]ns.Value{}
	for name, val := range values {
		v, ok := p.Params[name]
		if !ok {
			return nil, fmt.Errorf("cypher: the query has no parameter $%s", name)
		}
		vals[v] = []ns.Value{val}
	}
	return datalog.Bind(vals), nil
}

// Compile reads a Cypher query and writes the jaala program answering it over g, a graph view of v.
// It refuses, with an *Error, a query outside the subset a program expresses today (see the package
// doc), naming the gap that would let it, and a label, type or property g doesn't have. It parses
// the program it writes but doesn't check it against v; evaluating or datalog.Validate does.
func Compile(v *ns.Vocabulary, g Graph, text string) (Program, error) {
	if err := g.Validate(v); err != nil {
		return Program{}, err
	}
	st, err := parse(text)
	if err != nil {
		return Program{}, err
	}
	c := &compiler{v: v, g: g, used: map[string]bool{}, nodes: map[string]*nodeVar{}, rels: map[string]Pos{},
		params: map[string]string{}, helpers: map[string]bool{}}
	prog, cerr := c.compile(st)
	if cerr != nil {
		return Program{}, cerr
	}
	q, perr := datalog.Parse(prog.Text)
	if perr != nil {
		return Program{}, fmt.Errorf("cypher: the program written for this query doesn't parse (a bug in the compiler): %w\n%s", perr, prog.Text)
	}
	prog.Query = q
	return prog, nil
}

type compiler struct {
	v       *ns.Vocabulary
	g       Graph
	used    map[string]bool // jaala variable names taken
	nodes   map[string]*nodeVar
	order   []*nodeVar
	rels    map[string]Pos // relationship variables, which nothing may read (#279)
	params  map[string]string
	helpers map[string]bool
	rules   []rule
	n       int // fresh names

	shared []frag  // literals every disjunct holds
	scope  *[]frag // where a property read through Props or a computed value goes
	kv     map[*[]frag]map[string]string
	inRule bool // rendering a rule's body or head, where a parameter can't go (datalog.Bind binds the goal)
	err    *Error
}

type nodeVar struct {
	name     string // the Cypher variable, or a fresh name for an anonymous node
	jv       string
	pos      Pos
	labels   []string
	slots    map[string]*slot // by label, then property: label + "\x00" + key
	subst    string           // the constant (or parameter) the key equals, written in its place
	valued   bool             // the key is read as a value, so it stays a variable
	endpoint bool             // a relationship reads it
	anchor   bool             // a rule reads it (a pattern test), so a parameter can't stand in for it
}

type slot struct {
	jv    string // set once read
	konst frag   // the constant written in its place when unread
}

// A frag is a piece of program text whose node keys are written last, once Compile knows which
// keys a constant replaces.
type frag []any

type (
	keyArg  struct{ n *nodeVar } // a node's key as an atom's argument: a constant may replace it
	keyVal  struct{ n *nodeVar } // a node's key read as a value: always its variable
	nodeAtm struct {
		n     *nodeVar
		label string
		not   bool
	}
	paramEl struct {
		name string
		pos  Pos
	}
)

type rule struct {
	head frag
	body []frag
}

func (c *compiler) fail(err *Error) {
	if c.err == nil {
		c.err = err
	}
}

func (c *compiler) render(f frag) string {
	var b strings.Builder
	for _, el := range f {
		switch el := el.(type) {
		case string:
			b.WriteString(el)
		case frag:
			b.WriteString(c.render(el))
		case keyArg:
			b.WriteString(c.key(el.n))
		case keyVal:
			b.WriteString("?" + el.n.jv)
		case nodeAtm:
			if el.not {
				b.WriteString("not ")
			}
			b.WriteString(c.nodeAtom(el.n, el.label, el.not))
		case paramEl:
			if c.inRule {
				c.fail(errAt(el.pos, "$%s is read inside an OR, a pattern test or a variable-length relationship, which a parameter can't reach yet: write its value in the query", el.name))
			}
			b.WriteString("?" + c.params[el.name])
		default:
			panic(fmt.Sprintf("cypher: frag element %T", el))
		}
	}
	return b.String()
}

func (c *compiler) key(n *nodeVar) string {
	if n.subst != "" && !n.valued {
		return n.subst
	}
	return "?" + n.jv
}

func (c *compiler) nodeAtom(n *nodeVar, label string, negated bool) string {
	nd, _ := c.g.node(label)
	ar, _ := arity(c.v, nd.Relation)
	args := make([]string, ar)
	for i := range args {
		args[i] = "_"
	}
	for i, k := range nd.Fixed {
		args[i] = quote(k)
	}
	if !negated {
		for prop, i := range nd.Props {
			s := n.slots[label+"\x00"+prop]
			switch {
			case s == nil:
			case s.jv != "":
				args[i] = "?" + s.jv
			case s.konst != nil:
				args[i] = c.render(s.konst)
			}
		}
	}
	args[nd.Key] = c.key(n)
	return nd.Relation + "(" + strings.Join(args, ", ") + ")"
}

func (c *compiler) fresh(base string) string {
	base = sanitize(base)
	if !c.used[base] {
		c.used[base] = true
		return base
	}
	for {
		c.n++
		name := fmt.Sprintf("%s%d", base, c.n)
		if !c.used[name] {
			c.used[name] = true
			return name
		}
	}
}

var identRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func sanitize(s string) string {
	if identRe.MatchString(s) && s != "_" {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if r < 128 && identRe.MatchString(string(r)) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if strings.Trim(b.String(), "_") == "" {
		return "v" + b.String() // ?_ alone is the wildcard
	}
	return b.String()
}

func (c *compiler) helper(base string) string {
	name := "cypher_" + base
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s%d", name, i)
		if !c.helpers[cand] && !c.v.Has(cand) && !c.v.IsModule(cand) {
			c.helpers[cand] = true
			return cand
		}
	}
}

func (c *compiler) compile(st statement) (Program, *Error) {
	// Nodes and relationships first, so a property read anywhere knows its node's labels.
	var conds []expr
	type relAt struct {
		r        relPat
		from, to *nodeVar
	}
	var rels []relAt
	for _, m := range st.matches {
		for _, pat := range m.patterns {
			ns := make([]*nodeVar, len(pat.nodes))
			for i, np := range pat.nodes {
				n, err := c.declareNode(np, &conds)
				if err != nil {
					return Program{}, err
				}
				ns[i] = n
			}
			for i, r := range pat.rels {
				if err := c.declareRel(r); err != nil {
					return Program{}, err
				}
				ns[i].endpoint, ns[i+1].endpoint = true, true
				rels = append(rels, relAt{r, ns[i], ns[i+1]})
			}
		}
		if m.where != nil {
			for _, e := range conjuncts(m.where) {
				if h, ok := e.(hasLabel); ok {
					if err := c.declareLabels(h); err != nil {
						return Program{}, err
					}
					continue
				}
				conds = append(conds, e)
			}
		}
	}
	c.scope = &c.shared
	for _, n := range c.order {
		for _, l := range n.labels {
			c.shared = append(c.shared, frag{nodeAtm{n: n, label: l}})
		}
	}
	for _, r := range rels {
		f, err := c.relLiteral(r.r, r.from, r.to)
		if err != nil {
			return Program{}, err
		}
		c.shared = append(c.shared, f)
	}

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
		vf, err := c.value(val)
		if err != nil {
			return Program{}, err
		}
		switch r := ref.(type) {
		case varRef:
			n, err := c.nodeRef(r)
			if err != nil {
				return Program{}, err
			}
			eqs = append(eqs, pending{n: n, val: vf})
			continue
		case propRef:
			n, err := c.nodeRef(varRef{pos: r.pos, name: r.v})
			if err != nil {
				return Program{}, err
			}
			label, identity, kvRead, err := c.resolveProp(n, r)
			if err != nil {
				return Program{}, err
			}
			switch {
			case identity:
				eqs = append(eqs, pending{n: n, val: vf})
				continue
			case !kvRead:
				eqs = append(eqs, pending{n: n, s: c.slotFor(n, label, r.key), key: r.key, val: vf})
				continue
			default:
				c.shared = append(c.shared, c.kvAtom(n, quote(r.key), vf))
				continue
			}
		}
		rest = append(rest, e)
	}

	// The answer, then the conditions, so a value they read is known before the equalities settle.
	ret, err := c.returning(st)
	if err != nil {
		return Program{}, err
	}
	disjuncts, derr := c.dnf(rest)
	if derr != nil {
		return Program{}, derr
	}
	bodies := make([][]frag, len(disjuncts))
	for i, d := range disjuncts {
		var lits []frag
		c.scope = &lits
		for _, cd := range d {
			f, err := c.condition(cd.e, cd.neg)
			if err != nil {
				return Program{}, err
			}
			lits = append(lits, f)
		}
		bodies[i] = lits
	}
	c.scope = &c.shared

	c.settle(eqs)
	for _, n := range c.order {
		if len(n.labels) == 0 && !n.endpoint {
			f, err := c.anyNode(n)
			if err != nil {
				return Program{}, err
			}
			c.shared = append(c.shared, f)
		}
	}
	return c.write(ret, bodies)
}

// A pending equality is a node's key or one of its columns equal to a constant or a parameter in every
// row, which settle writes into the atoms when it is the only one and nothing else reads the value.
type pending struct {
	n   *nodeVar
	s   *slot // nil for the key
	key string
	val frag
}

func (c *compiler) settle(eqs []pending) {
	var nodes []*nodeVar
	var slots []*slot
	byNode, bySlot := map[*nodeVar][]pending{}, map[*slot][]pending{}
	for _, p := range eqs {
		if p.s == nil {
			if byNode[p.n] == nil {
				nodes = append(nodes, p.n)
			}
			byNode[p.n] = append(byNode[p.n], p)
		} else {
			if bySlot[p.s] == nil {
				slots = append(slots, p.s)
			}
			bySlot[p.s] = append(bySlot[p.s], p)
		}
	}
	for _, n := range nodes {
		ps := byNode[n]
		if len(ps) == 1 && !n.valued && !(n.anchor && hasParam(ps[0].val)) {
			n.subst = c.render(ps[0].val)
			continue
		}
		n.valued = true
		for _, p := range ps {
			c.shared = append(c.shared, frag{keyVal{n}, " = ", p.val})
		}
	}
	for _, s := range slots {
		ps := bySlot[s]
		if len(ps) == 1 && s.jv == "" {
			s.konst = ps[0].val
			continue
		}
		if s.jv == "" {
			s.jv = c.fresh(ps[0].n.jv + "_" + ps[0].key)
		}
		for _, p := range ps {
			c.shared = append(c.shared, frag{"?" + s.jv, " = ", p.val})
		}
	}
}

func hasParam(f frag) bool {
	for _, el := range f {
		if _, ok := el.(paramEl); ok {
			return true
		}
	}
	return false
}

func isConst(e expr) bool {
	switch e := e.(type) {
	case strLit, numLit, paramRef:
		return true
	case minus:
		_, ok := e.e.(numLit)
		return ok
	}
	return false
}

func readsVar(e expr) bool {
	switch e := e.(type) {
	case varRef, propRef, paramRef:
		return true
	case binary:
		return readsVar(e.l) || readsVar(e.r)
	case minus:
		return readsVar(e.e)
	case call:
		for _, a := range e.args {
			if readsVar(a) {
				return true
			}
		}
	}
	return false
}

func conjuncts(e expr) []expr {
	if b, ok := e.(binary); ok && b.op == "AND" {
		return append(conjuncts(b.l), conjuncts(b.r)...)
	}
	return []expr{e}
}

func (c *compiler) declareNode(np nodePat, conds *[]expr) (*nodeVar, *Error) {
	if _, ok := c.rels[np.v]; ok && np.v != "" {
		return nil, errAt(np.pos, "%s names a relationship, not a node", np.v)
	}
	n := c.nodes[np.v]
	if np.v == "" || n == nil {
		name := np.v
		if name == "" {
			name = fmt.Sprintf("\x00anon%d", len(c.order))
		}
		jv := np.v
		if jv == "" {
			jv = "_n"
		}
		n = &nodeVar{name: name, jv: c.fresh(jv), pos: np.pos, slots: map[string]*slot{}}
		if np.v != "" {
			c.nodes[np.v] = n
		}
		c.order = append(c.order, n)
	}
	for _, l := range np.labels {
		if _, ok := c.g.node(l); !ok {
			return nil, errAt(np.pos, "no label %s in this graph%s", l, oneOf("labels", c.g.labelNames()))
		}
		if !contains(n.labels, l) {
			n.labels = append(n.labels, l)
		}
	}
	for _, p := range np.props {
		*conds = append(*conds, binary{pos: p.pos, op: "=", l: propRef{pos: p.pos, v: n.name, key: p.key}, r: p.val})
	}
	if np.v == "" {
		c.nodes[n.name] = n
	}
	return n, nil
}

// declareLabels adds a WHERE's n:Label, true of every row, to n's labels as a MATCH's would be.
func (c *compiler) declareLabels(h hasLabel) *Error {
	n, err := c.nodeRef(varRef{pos: h.pos, name: h.v})
	if err != nil {
		return err
	}
	for _, l := range h.labels {
		if _, ok := c.g.node(l); !ok {
			return errAt(h.pos, "no label %s in this graph%s", l, oneOf("labels", c.g.labelNames()))
		}
		if !contains(n.labels, l) {
			n.labels = append(n.labels, l)
		}
	}
	return nil
}

func (c *compiler) declareRel(r relPat) *Error {
	if r.v == "" {
		return nil
	}
	if _, ok := c.nodes[r.v]; ok {
		return errAt(r.pos, "%s names a node, not a relationship", r.v)
	}
	c.rels[r.v] = r.pos
	return nil
}

func oneOf(what string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf(" (its %s: %s)", what, strings.Join(names, ", "))
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func quote(s string) string { return `"` + s + `"` }

func (c *compiler) nodeRef(r varRef) (*nodeVar, *Error) {
	if n, ok := c.nodes[r.name]; ok {
		return n, nil
	}
	if _, ok := c.rels[r.name]; ok {
		return nil, gapEdgeVars.at(r.pos, "%s is a relationship: return or compare its endpoints instead", r.name)
	}
	return nil, errAt(r.pos, "unknown variable %s: a MATCH has to bind it", r.name)
}

// resolveProp finds where n's property lives: a column of one of n's labels' relations (label), n's
// key itself (identity), or the Props relation (kv). An unlabeled node takes the one label holding
// the property, and becomes a node of it.
func (c *compiler) resolveProp(n *nodeVar, r propRef) (label string, identity, kv bool, err *Error) {
	for _, l := range n.labels {
		nd, _ := c.g.node(l)
		if i, ok := nd.Props[r.key]; ok {
			return l, i == nd.Key, false, nil
		}
	}
	if c.g.Props != nil {
		return "", false, true, nil
	}
	if len(n.labels) == 0 {
		var owners []string
		for _, nd := range c.g.Nodes {
			if _, ok := nd.Props[r.key]; ok {
				owners = append(owners, nd.Label)
			}
		}
		if len(owners) == 1 {
			nd, _ := c.g.node(owners[0])
			if nd.Props[r.key] == nd.Key {
				return owners[0], true, false, nil
			}
			n.labels = append(n.labels, owners[0])
			c.shared = append(c.shared, frag{nodeAtm{n: n, label: owners[0]}})
			return owners[0], false, false, nil
		}
		if len(owners) > 1 {
			return "", false, false, errAt(r.pos, "%s.%s: give %s a label, since %s all have a %s", r.v, r.key, r.v, strings.Join(owners, ", "), r.key)
		}
		return "", false, false, errAt(r.pos, "no node in this graph has a property %s", r.key)
	}
	var known []string
	for _, l := range n.labels {
		nd, _ := c.g.node(l)
		known = append(known, sortedKeys(nd.Props)...)
	}
	return "", false, false, errAt(r.pos, "no property %s on %s:%s%s", r.key, r.v, strings.Join(n.labels, ":"), oneOf("properties", known))
}

func (c *compiler) slotFor(n *nodeVar, label, key string) *slot {
	k := label + "\x00" + key
	s := n.slots[k]
	if s == nil {
		s = &slot{}
		n.slots[k] = s
	}
	return s
}

// prop is a property read as a value: the variable holding it.
func (c *compiler) prop(r propRef) (frag, *Error) {
	n, err := c.nodeRef(varRef{pos: r.pos, name: r.v})
	if err != nil {
		return nil, err
	}
	label, identity, kv, err := c.resolveProp(n, r)
	if err != nil {
		return nil, err
	}
	switch {
	case identity:
		n.valued = true
		return frag{keyVal{n}}, nil
	case kv:
		return frag{"?" + c.kvRead(n, r.key)}, nil
	}
	s := c.slotFor(n, label, r.key)
	if s.jv == "" {
		s.jv = c.fresh(n.jv + "_" + r.key)
	}
	return frag{"?" + s.jv}, nil
}

// kvRead reads n's property key through the Props relation, once per scope: a property read inside
// one side of an OR is read only there, so a node without it still passes the other side.
func (c *compiler) kvRead(n *nodeVar, key string) string {
	if c.kv == nil {
		c.kv = map[*[]frag]map[string]string{}
	}
	m := c.kv[c.scope]
	if m == nil {
		m = map[string]string{}
		c.kv[c.scope] = m
	}
	k := n.name + "\x00" + key
	if jv, ok := m[k]; ok {
		return jv
	}
	jv := c.fresh(n.jv + "_" + key)
	m[k] = jv
	*c.scope = append(*c.scope, c.kvAtom(n, quote(key), "?"+jv))
	return jv
}

func (c *compiler) kvAtom(n *nodeVar, key string, val any) frag {
	p := c.g.Props
	ar, _ := arity(c.v, p.Relation)
	args := make([]any, 0, 2*ar+2)
	args = append(args, p.Relation+"(")
	for i := 0; i < ar; i++ {
		if i > 0 {
			args = append(args, ", ")
		}
		switch i {
		case p.Node:
			args = append(args, keyArg{n})
		case p.Key:
			args = append(args, key)
		case p.Value:
			args = append(args, val)
		default:
			args = append(args, "_")
		}
	}
	return append(frag(args), ")")
}

func (c *compiler) anyNode(n *nodeVar) (frag, *Error) {
	if len(c.g.Nodes) == 0 {
		return nil, errAt(n.pos, "this graph has no labels to read a node from")
	}
	name := c.helper("node")
	for _, nd := range c.g.Nodes {
		tmp := &nodeVar{jv: "n", slots: map[string]*slot{}}
		c.rules = append(c.rules, rule{head: frag{name + "(?n)"}, body: []frag{{c.nodeAtom(tmp, nd.Label, false)}}})
	}
	return frag{name, "(", keyArg{n}, ")"}, nil
}

// edges are the relationship types r reads, each with its constants: the Fixed arguments and r's
// own properties. A type without one of r's properties holds no match and drops out.
func (c *compiler) edges(r relPat) ([]Edge, [][]any, *Error) {
	types := r.types
	if len(types) == 0 {
		types = c.g.typeNames()
		if len(types) == 0 {
			return nil, nil, errAt(r.pos, "this graph has no relationship types")
		}
	}
	var es []Edge
	var argss [][]any
	for _, ty := range types {
		e, ok := c.g.edge(ty)
		if !ok {
			return nil, nil, errAt(r.pos, "no relationship type %s in this graph%s", ty, oneOf("types", c.g.typeNames()))
		}
		ar, _ := arity(c.v, e.Relation)
		args := make([]any, ar)
		for i := range args {
			args[i] = "_"
		}
		for i, k := range e.Fixed {
			args[i] = quote(k)
		}
		held := true
		for _, p := range r.props {
			i, ok := e.Props[p.key]
			if !ok {
				held = false
				break
			}
			if !isConst(p.val) {
				return nil, nil, errAt(p.pos, "a relationship's property in a pattern takes a value written in the query")
			}
			f, err := c.value(p.val)
			if err != nil {
				return nil, nil, err
			}
			args[i] = f
		}
		if held {
			es = append(es, e)
			argss = append(argss, args)
		}
	}
	if len(es) == 0 {
		return nil, nil, errAt(r.props[0].pos, "no relationship of type %s has a property %s", strings.Join(types, "|"), r.props[0].key)
	}
	return es, argss, nil
}

func hasParamIn(argss [][]any) bool {
	for _, args := range argss {
		for _, a := range args {
			if f, ok := a.(frag); ok && hasParam(f) {
				return true
			}
		}
	}
	return false
}

func edgeAtom(e Edge, args []any, from, to any) frag {
	f := frag{e.Relation + "("}
	for i, a := range args {
		if i > 0 {
			f = append(f, ", ")
		}
		switch i {
		case e.From:
			f = append(f, from)
		case e.To:
			f = append(f, to)
		default:
			f = append(f, a)
		}
	}
	return append(f, ")")
}

// relLiteral is the literal matching r from from to to: an atom over the type's relation, or a call
// of a helper relation when r has several types, either direction, or a variable length.
func (c *compiler) relLiteral(r relPat, from, to *nodeVar) (frag, *Error) {
	if r.v != "" && r.varLen {
		return nil, gapPaths.at(r.pos, "a variable bound to a variable-length relationship")
	}
	es, argss, err := c.edges(r)
	if err != nil {
		return nil, err
	}
	src, dst := any(keyArg{from}), any(keyArg{to})
	if r.dir == -1 {
		src, dst = dst, src
	}
	if !r.varLen && len(es) == 1 && r.dir != 0 {
		return edgeAtom(es[0], argss[0], src, dst), nil
	}
	// step(x, y) is one hop: the type's relation itself when there is one type read one way, or a
	// helper relation of every type and direction r reads.
	step := func(x, y any) frag {
		if r.dir == 1 {
			return edgeAtom(es[0], argss[0], x, y)
		}
		return edgeAtom(es[0], argss[0], y, x)
	}
	if len(es) > 1 || r.dir == 0 || hasParamIn(argss) {
		name := c.helper("step")
		for i, e := range es {
			c.rules = append(c.rules, rule{head: frag{name + "(?x, ?y)"}, body: []frag{edgeAtom(e, argss[i], "?x", "?y")}})
			if r.dir == 0 {
				c.rules = append(c.rules, rule{head: frag{name + "(?x, ?y)"}, body: []frag{edgeAtom(e, argss[i], "?y", "?x")}})
			}
		}
		step = func(x, y any) frag { return frag{name, "(", x, ", ", y, ")"} }
		src, dst = keyArg{from}, keyArg{to}
		if r.dir == -1 {
			src, dst = dst, src
		}
	} else {
		src, dst = keyArg{from}, keyArg{to}
	}
	if !r.varLen {
		return step(src, dst), nil
	}
	if r.min < 1 {
		return nil, gapShortest.at(r.pos, "a zero-length match (*0..): start the range at 1")
	}
	if r.max != -1 && r.max < r.min {
		return nil, errAt(r.pos, "the range *%d..%d is empty", r.min, r.max)
	}
	const maxHops = 8
	if r.max > maxHops || (r.max == -1 && r.min > maxHops) {
		return nil, gapShortest.at(r.pos, "a bounded match longer than %d hops: write * for any length", maxHops)
	}
	reach := c.helper("reach")
	chain := func(k int) []frag {
		var body []frag
		prev := "?x"
		for i := 1; i <= k; i++ {
			next := fmt.Sprintf("?y%d", i)
			if i == k {
				next = "?y"
			}
			body = append(body, step(prev, next))
			prev = next
		}
		return body
	}
	if r.max == -1 {
		c.rules = append(c.rules,
			rule{head: frag{reach + "(?x, ?y)"}, body: chain(r.min)},
			rule{head: frag{reach + "(?x, ?y)"}, body: []frag{{reach + "(?x, ?z)"}, step("?z", "?y")}})
	} else {
		for k := r.min; k <= r.max; k++ {
			c.rules = append(c.rules, rule{head: frag{reach + "(?x, ?y)"}, body: chain(k)})
		}
	}
	return frag{reach, "(", src, ", ", dst, ")"}, nil
}

// value is e as an operand of a comparison: a constant, a parameter, a variable or arithmetic over
// at least one variable or parameter, since jaala computes only from what a body binds.
func (c *compiler) value(e expr) (frag, *Error) {
	switch e.(type) {
	case binary, minus:
		if !isConst(e) && !readsVar(e) {
			return nil, errAt(e.at(), "arithmetic over constants alone: write the value it comes to")
		}
	}
	switch e := e.(type) {
	case strLit:
		if strings.Contains(e.s, `"`) || strings.HasSuffix(e.s, `\`) {
			return nil, errAt(e.pos, "a string holding a double quote, or ending in a backslash, can't be written in jaala")
		}
		return frag{quote(e.s)}, nil
	case numLit:
		if _, err := strconv.ParseFloat(e.text, 64); err != nil {
			return nil, errAt(e.pos, "%s isn't a number jaala reads", e.text)
		}
		return frag{e.text}, nil
	case paramRef:
		if _, ok := c.params[e.name]; !ok {
			jv := e.name
			if c.used[jv] || sanitize(jv) != jv {
				jv = c.fresh("p_" + e.name)
			} else {
				c.used[jv] = true
			}
			c.params[e.name] = jv
		}
		return frag{paramEl{name: e.name, pos: e.pos}}, nil
	case varRef:
		n, err := c.nodeRef(e)
		if err != nil {
			return nil, err
		}
		n.valued = true
		return frag{keyVal{n}}, nil
	case propRef:
		return c.prop(e)
	case minus:
		f, err := c.value(e.e)
		if err != nil {
			return nil, err
		}
		if n, ok := e.e.(numLit); ok {
			return frag{"-" + n.text}, nil
		}
		return frag{"-(", f, ")"}, nil
	case binary:
		switch e.op {
		case "+", "-", "*", "/":
			if _, ok := e.l.(strLit); ok && e.op == "+" {
				return nil, gapFunctions.at(e.pos, "joining strings with +")
			}
			if _, ok := e.r.(strLit); ok && e.op == "+" {
				return nil, gapFunctions.at(e.pos, "joining strings with +")
			}
			l, err := c.value(e.l)
			if err != nil {
				return nil, err
			}
			r, err := c.value(e.r)
			if err != nil {
				return nil, err
			}
			return frag{"(", l, " " + e.op + " ", r, ")"}, nil
		case "%", "^":
			return nil, gapFunctions.at(e.pos, "%s", e.op)
		}
		return nil, errAt(e.pos, "%s is a test, not a value", e.op)
	case call:
		switch strings.ToLower(e.name) {
		case "id", "elementid":
			if len(e.args) == 1 {
				if v, ok := e.args[0].(varRef); ok {
					return c.value(v)
				}
			}
		case "type", "startnode", "endnode":
			return nil, gapEdgeVars.at(e.pos, "%s()", e.name)
		case "length", "nodes", "relationships":
			return nil, gapPaths.at(e.pos, "%s()", e.name)
		case "labels", "keys", "properties", "range", "size", "head", "last", "tail", "split":
			return nil, gapLists.at(e.pos, "%s()", e.name)
		}
		if aggregates[strings.ToLower(e.name)] {
			return nil, errAt(e.pos, "%s() aggregates, so it can only be returned", e.name)
		}
		return nil, gapFunctions.at(e.pos, "%s()", e.name)
	case listLit:
		return nil, gapLists.at(e.pos, "a list")
	case unsupported:
		return nil, e.err
	}
	return nil, errAt(e.at(), "a test can't be a value here")
}

// term is e as a predicate's argument, which takes a variable or a constant: arithmetic is computed
// into a fresh variable first.
func (c *compiler) term(e expr) (frag, *Error) {
	f, err := c.value(e)
	if err != nil {
		return nil, err
	}
	switch e.(type) {
	case binary, minus:
		if _, ok := e.(minus); ok && isConst(e) {
			return f, nil
		}
		jv := c.fresh("_c")
		*c.scope = append(*c.scope, frag{"?" + jv + " = ", f})
		return frag{"?" + jv}, nil
	}
	return f, nil
}

var flip = map[string]string{"=": "!=", "<>": "=", "!=": "=", "<": ">=", "<=": ">", ">": "<=", ">=": "<"}

// condition compiles one test, negated when neg is set.
func (c *compiler) condition(e expr, neg bool) (frag, *Error) {
	not := ""
	if neg {
		not = "not "
	}
	switch e := e.(type) {
	case binary:
		switch e.op {
		case "=", "<>", "!=", "<", "<=", ">", ">=":
			l, err := c.value(e.l)
			if err != nil {
				return nil, err
			}
			r, err := c.value(e.r)
			if err != nil {
				return nil, err
			}
			op := e.op
			if op == "<>" {
				op = "!="
			}
			if neg {
				op = flip[op]
			}
			return frag{l, " " + op + " ", r}, nil
		case "STARTS WITH", "ENDS WITH", "CONTAINS":
			l, err := c.term(e.l)
			if err != nil {
				return nil, err
			}
			r, err := c.term(e.r)
			if err != nil {
				return nil, err
			}
			pred := map[string]string{"STARTS WITH": "str.prefix", "ENDS WITH": "str.suffix", "CONTAINS": "str.contains"}[e.op]
			return frag{not + pred + "(", l, ", ", r, ")"}, nil
		case "=~":
			s, ok := e.r.(strLit)
			if !ok {
				return nil, errAt(e.r.at(), "=~ takes a regular expression written in the query")
			}
			l, err := c.term(e.l)
			if err != nil {
				return nil, err
			}
			re, err := c.value(strLit{pos: s.pos, s: "^(?:" + s.s + ")$"})
			if err != nil {
				return nil, err
			}
			return frag{not + "str.match(", l, ", ", re, ")"}, nil
		}
	case isNull:
		null := !e.not
		if neg {
			null = !null
		}
		r, ok := e.e.(propRef)
		if !ok {
			return nil, errAt(e.pos, "IS NULL tests a property")
		}
		n, err := c.nodeRef(varRef{pos: r.pos, name: r.v})
		if err != nil {
			return nil, err
		}
		if _, _, kv, err := c.resolveProp(n, r); err != nil {
			return nil, err
		} else if kv {
			n.anchor = n.anchor || null
			if null {
				return append(frag{"not "}, c.kvAtom(n, quote(r.key), "_")...), nil
			}
			return c.kvAtom(n, quote(r.key), "_"), nil
		}
		v, err := c.prop(r)
		if err != nil {
			return nil, err
		}
		if null {
			return frag{"absent(", v, ")"}, nil
		}
		return frag{"not absent(", v, ")"}, nil
	case hasLabel:
		n, err := c.nodeRef(varRef{pos: e.pos, name: e.v})
		if err != nil {
			return nil, err
		}
		var f frag
		for i, l := range e.labels {
			if _, ok := c.g.node(l); !ok {
				return nil, errAt(e.pos, "no label %s in this graph%s", l, oneOf("labels", c.g.labelNames()))
			}
			if i > 0 {
				f = append(f, ", ")
			}
			f = append(f, nodeAtm{n: n, label: l, not: neg})
		}
		if neg && len(e.labels) > 1 {
			return nil, errAt(e.pos, "NOT n:A:B: write NOT n:A OR NOT n:B")
		}
		return f, nil
	case patExpr:
		return c.patternTest(e.p, neg)
	case call:
		if strings.EqualFold(e.name, "exists") && len(e.args) == 1 {
			if r, ok := e.args[0].(propRef); ok {
				return c.condition(isNull{pos: e.pos, e: r, not: true}, neg)
			}
			if p, ok := e.args[0].(patExpr); ok {
				return c.patternTest(p.p, neg)
			}
		}
		if _, err := c.value(e); err != nil {
			return nil, err
		}
	case unsupported:
		return nil, e.err
	}
	return nil, errAt(e.at(), "WHERE needs a test here: a comparison, a string test, IS NULL, a label or a pattern")
}

// patternTest is a pattern in WHERE: a helper relation over the nodes it shares with the MATCH,
// holding where the pattern matches from them, and called (or negated) on them.
func (c *compiler) patternTest(p pattern, neg bool) (frag, *Error) {
	saved, savedShared, savedScope := c.nodes, c.shared, c.scope
	inner := map[string]*nodeVar{}
	for k, v := range c.nodes {
		inner[k] = v
	}
	c.nodes = inner
	defer func() { c.nodes, c.shared, c.scope = saved, savedShared, savedScope }()
	var body []frag
	c.shared, c.scope = nil, &body
	var conds []expr
	var anchors, ns []*nodeVar
	for _, np := range p.nodes {
		if np.v != "" {
			if _, ok := saved[np.v]; !ok {
				return nil, errAt(np.pos, "a pattern in WHERE can't introduce %s: a MATCH has to bind it", np.v)
			}
		}
		decl := np
		if np.v != "" {
			if len(np.props) > 0 {
				return nil, errAt(np.pos, "write %s's properties in the MATCH, not in a pattern in WHERE", np.v)
			}
			decl.labels = nil
		}
		before := len(c.order)
		n, err := c.declareNode(decl, &conds)
		if err != nil {
			return nil, err
		}
		if len(c.order) > before {
			c.order = c.order[:before] // an inner node is the helper's own, not the MATCH's
		} else if !containsNode(anchors, n) {
			anchors = append(anchors, n)
		}
		ns = append(ns, n)
		for _, l := range np.labels {
			if _, ok := c.g.node(l); !ok {
				return nil, errAt(np.pos, "no label %s in this graph%s", l, oneOf("labels", c.g.labelNames()))
			}
			body = append(body, frag{nodeAtm{n: n, label: l}})
		}
	}
	if len(anchors) == 0 {
		return nil, errAt(p.pos, "a pattern in WHERE has to share a variable with the MATCH")
	}
	for i, r := range p.rels {
		if r.v != "" {
			return nil, gapEdgeVars.at(r.pos, "a relationship variable in a WHERE pattern")
		}
		f, err := c.relLiteral(r, ns[i], ns[i+1])
		if err != nil {
			return nil, err
		}
		body = append(body, f)
	}
	for _, e := range conds {
		f, err := c.condition(e, false)
		if err != nil {
			return nil, err
		}
		body = append(body, f)
	}
	body = append(body, c.shared...)
	name := c.helper("exists")
	head := frag{name + "("}
	call := frag{name + "("}
	for i, a := range anchors {
		a.anchor = true
		if i > 0 {
			head, call = append(head, ", "), append(call, ", ")
		}
		head, call = append(head, keyArg{a}), append(call, keyArg{a})
	}
	c.rules = append(c.rules, rule{head: append(head, ")"), body: body})
	call = append(call, ")")
	if neg {
		return append(frag{"not "}, call...), nil
	}
	return call, nil
}

func containsNode(ns []*nodeVar, n *nodeVar) bool {
	for _, m := range ns {
		if m == n {
			return true
		}
	}
	return false
}

type cond struct {
	e   expr
	neg bool
}

const maxDisjuncts = 16

// dnf writes the conjunction of es as a disjunction of conjunctions of tests, negation pushed onto
// the tests, an IN over a list written as equalities.
func (c *compiler) dnf(es []expr) ([][]cond, *Error) {
	out := [][]cond{{}}
	for _, e := range es {
		d, err := c.dnfOf(e, false)
		if err != nil {
			return nil, err
		}
		var next [][]cond
		for _, a := range out {
			for _, b := range d {
				next = append(next, append(append([]cond{}, a...), b...))
			}
		}
		if len(next) > maxDisjuncts {
			return nil, errAt(e.at(), "this WHERE expands to more than %d alternatives", maxDisjuncts)
		}
		out = next
	}
	return out, nil
}

func (c *compiler) dnfOf(e expr, neg bool) ([][]cond, *Error) {
	or := func(a, b [][]cond) [][]cond { return append(append([][]cond{}, a...), b...) }
	and := func(a, b [][]cond) [][]cond {
		var out [][]cond
		for _, x := range a {
			for _, y := range b {
				out = append(out, append(append([]cond{}, x...), y...))
			}
		}
		return out
	}
	switch e := e.(type) {
	case negate:
		return c.dnfOf(e.e, !neg)
	case binary:
		switch e.op {
		case "AND", "OR":
			l, err := c.dnfOf(e.l, neg)
			if err != nil {
				return nil, err
			}
			r, err := c.dnfOf(e.r, neg)
			if err != nil {
				return nil, err
			}
			if (e.op == "AND") != neg {
				return and(l, r), nil
			}
			return or(l, r), nil
		case "XOR":
			x := binary{pos: e.pos, op: "OR",
				l: binary{pos: e.pos, op: "AND", l: e.l, r: negate{pos: e.pos, e: e.r}},
				r: binary{pos: e.pos, op: "AND", l: negate{pos: e.pos, e: e.l}, r: e.r}}
			return c.dnfOf(x, neg)
		case "IN":
			list, ok := e.r.(listLit)
			if !ok {
				return nil, gapLists.at(e.pos, "IN over anything but a list written in the query")
			}
			if len(list.items) == 0 {
				return nil, errAt(e.pos, "IN [] holds for nothing")
			}
			var x expr = binary{pos: e.pos, op: "=", l: e.l, r: list.items[0]}
			for _, it := range list.items[1:] {
				x = binary{pos: e.pos, op: "OR", l: x, r: binary{pos: e.pos, op: "=", l: e.l, r: it}}
			}
			return c.dnfOf(x, neg)
		}
	}
	return [][]cond{{{e: e, neg: neg}}}, nil
}

var aggregates = map[string]bool{"count": true, "min": true, "max": true, "sum": true, "collect": true, "avg": true,
	"stdev": true, "stdevp": true, "percentilecont": true, "percentiledisc": true}

type column struct {
	name string
	text frag // the projection's column
	vars []string
	key  string // the expression, for ORDER BY to find it
}

type returned struct {
	cols  []column
	order []string
	limit *int
	skip  *int
}

func (c *compiler) returning(st statement) (returned, *Error) {
	r := st.ret
	var out returned
	items := r.items
	if r.star {
		for _, n := range c.order {
			if !strings.HasPrefix(n.name, "\x00") {
				items = append(items, returnItem{e: varRef{pos: r.pos, name: n.name}, text: n.name})
			}
		}
		if len(items) == 0 {
			return out, errAt(r.pos, "RETURN * with no named variable")
		}
	}
	for _, it := range items {
		col, err := c.column(it)
		if err != nil {
			return out, err
		}
		out.cols = append(out.cols, col)
	}
	for _, o := range r.order {
		key := exprKey(o.e)
		found := -1
		for i, it := range items {
			if v, ok := o.e.(varRef); ok && it.alias == v.name {
				found = i
				break
			}
			if out.cols[i].key == key {
				found = i
				break
			}
		}
		if found < 0 {
			return out, errAt(o.e.at(), "ORDER BY has to name a column RETURN writes (add it to RETURN, or order by its alias)")
		}
		s := c.render(out.cols[found].text)
		if o.desc {
			s += " desc"
		}
		out.order = append(out.order, s)
	}
	if r.skip != nil && len(r.order) == 0 {
		return out, errAt(r.pos, "SKIP needs an ORDER BY here, so a page continues from a stable order")
	}
	out.skip, out.limit = r.skip, r.limit
	return out, nil
}

func (c *compiler) column(it returnItem) (column, *Error) {
	name := it.alias
	if name == "" {
		name = it.text
	}
	col := column{name: name, key: exprKey(it.e)}
	if fn, ok := it.e.(call); ok && aggregates[strings.ToLower(fn.name)] {
		agg := strings.ToLower(fn.name)
		switch agg {
		case "collect":
			agg = "list"
		case "count", "min", "max", "sum":
		default:
			return col, gapFunctions.at(fn.pos, "%s()", fn.name)
		}
		var arg frag
		switch {
		case fn.star && agg == "count":
			if len(c.order) == 0 {
				return col, errAt(fn.pos, "count(*) with nothing matched")
			}
			n := c.order[0]
			n.valued = true
			arg = frag{keyVal{n}}
		case len(fn.args) != 1:
			return col, errAt(fn.pos, "%s() takes one argument", fn.name)
		default:
			f, err := c.term(fn.args[0])
			if err != nil {
				return col, err
			}
			if !isVar(f) {
				return col, errAt(fn.args[0].at(), "%s() reads a variable or a property, not a constant", fn.name)
			}
			arg = f
		}
		d := ""
		if fn.distinct {
			d = "distinct "
		}
		col.text = frag{agg + "(" + d, arg, ")"}
		return col, nil
	}
	switch it.e.(type) {
	case paramRef:
		return col, errAt(it.e.at(), "RETURN of a parameter: its value is the one the host binds")
	}
	if isConst(it.e) {
		return col, errAt(it.e.at(), "RETURN of a constant: jaala's columns are variables")
	}
	f, err := c.term(it.e)
	if err != nil {
		return col, err
	}
	col.text = f
	return col, nil
}

func isVar(f frag) bool {
	if len(f) != 1 {
		return false
	}
	switch el := f[0].(type) {
	case keyVal:
		return true
	case string:
		return strings.HasPrefix(el, "?")
	}
	return false
}

// exprKey writes e without positions, so ORDER BY can find the RETURN item it repeats.
func exprKey(e expr) string {
	switch e := e.(type) {
	case strLit:
		return strconv.Quote(e.s)
	case numLit:
		return e.text
	case paramRef:
		return "$" + e.name
	case varRef:
		return e.name
	case propRef:
		return e.v + "." + e.key
	case binary:
		return "(" + exprKey(e.l) + " " + e.op + " " + exprKey(e.r) + ")"
	case minus:
		return "-" + exprKey(e.e)
	case negate:
		return "NOT " + exprKey(e.e)
	case call:
		var args []string
		for _, a := range e.args {
			args = append(args, exprKey(a))
		}
		d := ""
		if e.distinct {
			d = "DISTINCT "
		}
		if e.star {
			args = []string{"*"}
		}
		return strings.ToLower(e.name) + "(" + d + strings.Join(args, ", ") + ")"
	}
	return fmt.Sprintf("%T@%d", e, e.at().Offset)
}

func (c *compiler) write(ret returned, bodies [][]frag) (Program, *Error) {
	prog := Program{Params: map[string]datalog.Var{}}
	for _, col := range ret.cols {
		prog.Columns = append(prog.Columns, col.name)
	}
	var b strings.Builder
	writeRule := func(r rule) {
		c.inRule = true
		b.WriteString(c.render(r.head))
		b.WriteString(" :- ")
		for i, l := range r.body {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(c.render(l))
		}
		b.WriteString(";\n")
		c.inRule = false
	}
	for _, r := range c.rules {
		writeRule(r)
	}
	cols := make([]string, len(ret.cols))
	for i, col := range ret.cols {
		cols[i] = c.render(col.text)
	}
	var goal []string
	if len(bodies) == 1 {
		for _, l := range append(append([]frag{}, c.shared...), bodies[0]...) {
			goal = append(goal, c.render(l))
		}
	} else {
		name := c.helper("match")
		var vars []string
		seen := map[string]bool{}
		for _, col := range cols {
			for _, v := range varsIn(col) {
				if !seen[v] {
					seen[v] = true
					vars = append(vars, v)
				}
			}
		}
		head := name + "(" + strings.Join(vars, ", ") + ")"
		for _, body := range bodies {
			writeRule(rule{head: frag{head}, body: append(append([]frag{}, c.shared...), body...)})
		}
		goal = []string{head}
	}
	if c.err != nil {
		return Program{}, c.err
	}
	b.WriteString(strings.Join(goal, ", "))
	b.WriteString(" => ")
	b.WriteString(strings.Join(cols, ", "))
	if len(ret.order) > 0 {
		b.WriteString(" order by " + strings.Join(ret.order, ", "))
	}
	if ret.limit != nil {
		fmt.Fprintf(&b, " limit %d", *ret.limit)
	}
	if ret.skip != nil {
		fmt.Fprintf(&b, " offset %d", *ret.skip)
	}
	prog.Text = b.String()
	for name, jv := range c.params {
		prog.Params[name] = datalog.Var(jv)
	}
	return prog, nil
}

var varRe = regexp.MustCompile(`\?[A-Za-z0-9_]+`)

func varsIn(s string) []string { return varRe.FindAllString(s, -1) }
