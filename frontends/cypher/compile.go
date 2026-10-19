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
	spans   []span            // where in the Cypher each piece of Text came from (Locate)
	names   map[string]string // jaala variables by the Cypher they stand for (Explain)
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

// An Option changes how Compile writes a program.
type Option func(*world)

// Params writes each $parameter as the constant values gives it, everywhere the query reads it,
// rather than as a goal variable for Program.Bind. A parameter a WITH stage, a UNION part, an OR, a
// pattern test or a variable-length relationship reads needs it, since datalog.Bind binds only the
// goal. A parameter values doesn't hold, and a value a query can't write (absent, or text holding a
// double quote), are refused.
func Params(values map[string]ns.Value) Option {
	return func(w *world) { w.paramVals = values }
}

// Compile reads a Cypher query and writes the jaala program answering it over g, a graph view of v.
// It refuses, with an *Error, a query outside the subset a program expresses today (see the package
// doc), naming the gap that would let it, and a label, type or property g doesn't have. It parses
// the program it writes but doesn't check it against v; evaluating or datalog.Validate does.
func Compile(v *ns.Vocabulary, g Graph, text string, opts ...Option) (Program, error) {
	if err := g.Validate(v); err != nil {
		return Program{}, err
	}
	st, err := parse(text)
	if err != nil {
		return Program{}, err
	}
	w := &world{v: v, g: g, params: map[string]string{}, helpers: map[string]bool{}, edgePaths: st.edgePaths}
	for _, o := range opts {
		o(w)
	}
	w.marks, w.marking = chooseMarks(text+decoded(text), g, w.paramVals)
	prog, cerr := w.compile(st)
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

type nodeVar struct {
	name     string // the Cypher variable, or a fresh name for an anonymous node
	jv       string
	pos      Pos
	labels   []string
	slots    map[string]*slot // by label, then property: label + "\x00" + key
	subst    frag             // the constant (or parameter) the key equals, written in its place
	valued   bool             // the key is read as a value, so it stays a variable
	endpoint bool             // a relationship reads it
	anchor   bool             // a rule reads it (a pattern test), so a parameter can't stand in for it
	carried  bool             // a WITH handed it on, so it is matched already
	of       *nodeVar         // inside a subquery, the outer node this one stands for
	opt      int              // the OPTIONAL MATCH that declared it, from 1, so it may be absent (#282)
	absent   bool             // a WITH carried it from an OPTIONAL MATCH, so it may be absent
}

// markValued keeps n's key a variable, and the outer node's it stands for in a subquery.
func markValued(n *nodeVar) {
	for ; n != nil; n = n.of {
		n.valued = true
	}
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
		lazy  bool // written only when a column of it is read
	}
	// braces are a body aggregate's literals, which jaala lowers to rules, so a parameter can't
	// reach them either.
	braces struct{ lits []frag }
	// optBraces are an optional's literals (#282), written `optional { ... }` unless they render to
	// nothing (a lazy label atom no column read), when the optional goes too.
	optBraces struct{ lits []frag }
	// origin is a piece of program written for the Cypher at at, so an error in it is located there.
	origin struct {
		at Pos
		f  frag
	}
	paramEl struct {
		name string
		pos  Pos
	}
)

type rule struct {
	head frag
	body []frag
	at   Pos // the Cypher a helper rule was written for, when it has no origin of its own
	// fixed are node keys this rule alone holds equal to a constant: an alternative's, written in
	// the key's place in its atoms and its head, so demand starts from the constant (#392).
	fixed map[*nodeVar]frag
}

// addRule adds helper rules written for the Cypher being compiled (the world's cur).
func (w *world) addRule(rs ...rule) {
	for _, r := range rs {
		r.at = w.cur
		w.rules = append(w.rules, r)
	}
}

// at marks f as written for the Cypher at pos, unless pos is no place (a node a WITH carried).
func at(pos Pos, f frag) frag {
	if pos.Line == 0 {
		return f
	}
	return frag{origin{pos, f}}
}

// name records that jaala variable jv stands for the Cypher name, for messages read in Cypher.
func (w *world) name(jv, name string) {
	if name == "" || strings.ContainsRune(name, 0) {
		return
	}
	if w.names == nil {
		w.names = map[string]string{}
	}
	if _, ok := w.names[jv]; !ok {
		w.names[jv] = name
	}
}

// chooseMarks picks the three bytes render marks an origin with (its start, the end of its index, and
// its end), from the control characters no text the program can be written from holds: the query,
// the graph's names and constants, and the parameters' values. With none free, which takes a query
// holding most control characters, nothing is marked and Locate places only compile errors.
func chooseMarks(text string, g Graph, params map[string]ns.Value) ([3]byte, bool) {
	var all strings.Builder
	all.WriteString(text)
	add := func(fixed map[int]string, props map[string]int, names ...string) {
		for _, n := range names {
			all.WriteString(n)
		}
		for _, k := range fixed {
			all.WriteString(k)
		}
		for k := range props {
			all.WriteString(k)
		}
	}
	for _, n := range g.Nodes {
		add(n.Fixed, n.Props, n.Label, n.Relation)
	}
	for _, e := range g.Edges {
		add(e.Fixed, e.Props, e.Type, e.Relation)
	}
	if g.Props != nil {
		all.WriteString(g.Props.Relation)
	}
	for k, p := range g.Derived {
		add(p.Fixed, nil, k, p.Relation)
	}
	for k, v := range params {
		all.WriteString(k + v.S + v.BaseUnit)
	}
	used := all.String()
	var marks [3]byte
	n := 0
	for c := byte(1); c < 0x20 && n < 3; c++ {
		if c == '\t' || c == '\n' || c == '\r' || strings.IndexByte(used, c) >= 0 {
			continue
		}
		marks[n] = c
		n++
	}
	return marks, n == 3
}

// mark wraps text in the markers of a new origin at pos.
func (w *world) mark(pos Pos, text string) string {
	if !w.marking {
		return text
	}
	w.origins = append(w.origins, pos)
	return string(w.marks[0]) + strconv.Itoa(len(w.origins)-1) + string(w.marks[1]) + text + string(w.marks[2])
}

// unmark strips the markers from text, returning it and each marked piece's place in it.
func (w *world) unmark(text string) (string, []span) {
	if !w.marking {
		return text, nil
	}
	var b strings.Builder
	var spans []span
	var open []span
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case w.marks[0]:
			j := strings.IndexByte(text[i:], w.marks[1]) + i
			idx, _ := strconv.Atoi(text[i+1 : j])
			open = append(open, span{start: b.Len(), at: w.origins[idx]})
			i = j
		case w.marks[2]:
			s := open[len(open)-1]
			open = open[:len(open)-1]
			s.end = b.Len()
			spans = append(spans, s)
		default:
			b.WriteByte(text[i])
		}
	}
	return b.String(), spans
}

// A span is a piece of a Program's text, from start to end in bytes, written for the Cypher at at.
type span struct {
	start, end int
	at         Pos
}

func (w *world) fail(err *Error) {
	if w.err == nil {
		w.err = err
	}
}

func (c *world) render(f frag) string {
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
			if f, ok := c.fixed[el.n]; ok {
				b.WriteString(c.render(f))
			} else {
				b.WriteString("?" + el.n.jv)
			}
		case nodeAtm:
			if el.lazy && !slotsRead(el.n, el.label) {
				continue
			}
			if el.not {
				b.WriteString("not ")
			}
			b.WriteString(c.nodeAtom(el.n, el.label, el.not))
		case origin:
			if s := c.render(el.f); s != "" {
				b.WriteString(c.mark(el.at, s))
			}
		case braces:
			was := c.inRule
			c.inRule = true
			b.WriteString(c.joinLits(el.lits))
			c.inRule = was
		case optBraces:
			was := c.inRule
			c.inRule = true
			if s := c.joinLits(el.lits); s != "" {
				b.WriteString("optional { " + s + " }")
			}
			c.inRule = was
		case paramEl:
			if c.inRule {
				c.fail(errAt(el.pos, "$%s is read inside a WITH stage, a UNION part, an OR, a pattern test, a variable-length relationship or an OPTIONAL MATCH, which datalog.Bind can't reach: compile with cypher.Params", el.name))
			}
			b.WriteString("?" + c.params[el.name])
		default:
			panic(fmt.Sprintf("cypher: frag element %T", el))
		}
	}
	return b.String()
}

func slotsRead(n *nodeVar, label string) bool {
	for k, s := range n.slots {
		if strings.HasPrefix(k, label+"\x00") && (s.jv != "" || s.konst != nil) {
			return true
		}
	}
	return false
}

func (c *world) key(n *nodeVar) string {
	if n.of != nil {
		return c.key(n.of)
	}
	if f, ok := c.fixed[n]; ok {
		return c.render(f)
	}
	if n.subst != nil && !n.valued {
		return c.render(n.subst)
	}
	return "?" + n.jv
}

func (c *world) nodeAtom(n *nodeVar, label string, negated bool) string {
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

func (c *world) helper(base string) string {
	name := "cypher_" + base
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s%d", name, i)
		if !c.helpers[cand] && !c.v.Has(cand) && !c.v.IsModule(cand) {
			c.helpers[cand] = true
			return cand
		}
	}
}

// A pending equality is a node's key or one of its columns equal to a constant or a parameter in every
// row, which settle writes into the atoms when it is the only one and nothing else reads the value.
type pending struct {
	n   *nodeVar
	s   *slot // nil for the key
	key string
	val frag
	at  Pos // the equality, so a value an argument refuses is located there
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
			n.subst = at(ps[0].at, ps[0].val)
			continue
		}
		n.valued = true
		for _, p := range ps {
			c.shared = append(c.shared, at(p.at, frag{keyVal{n}, " = ", p.val}))
		}
	}
	for _, s := range slots {
		ps := bySlot[s]
		if len(ps) == 1 && s.jv == "" {
			s.konst = at(ps[0].at, ps[0].val)
			continue
		}
		if s.jv == "" {
			s.jv = c.fresh(ps[0].n.jv + "_" + ps[0].key)
			c.name(s.jv, ps[0].n.name+"."+ps[0].key)
		}
		for _, p := range ps {
			c.shared = append(c.shared, at(p.at, frag{"?" + s.jv, " = ", p.val}))
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

// readsVar reports whether e reads something a body binds: a variable, a property, or a parameter
// the host binds (one Params writes as a constant doesn't).
func (c *compiler) readsVar(e expr) bool {
	switch e := e.(type) {
	case varRef, propRef:
		return true
	case paramRef:
		return c.paramVals == nil
	case binary:
		return c.readsVar(e.l) || c.readsVar(e.r)
	case minus:
		return c.readsVar(e.e)
	case call:
		for _, a := range e.args {
			if c.readsVar(a) {
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
	if np.v != "" && c.values[np.v] != "" {
		return nil, errAt(np.pos, "%s is a value, not a node", np.v)
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
		c.name(n.jv, np.v)
		if np.v != "" {
			c.nodes[np.v] = n
		}
		c.order = append(c.order, n)
	}
	for _, l := range np.labels {
		if _, ok := c.g.node(l); !ok {
			return nil, errAt(np.pos, "no label %s in this graph%s", l, oneOf("labels", l, c.g.labelNames()))
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
			return errAt(h.pos, "no label %s in this graph%s", l, oneOf("labels", l, c.g.labelNames()))
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

// oneOf is what an unknown name could have been: the closest of names when got is a plausible typo
// of one (#353), then all of them.
func oneOf(what, got string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	return ns.DidYouMeanValue(names, got) + fmt.Sprintf(" (its %s: %s)", what, strings.Join(names, ", "))
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
	if c.values[r.name] != "" {
		return nil, errAt(r.pos, "%s is a value, not a node", r.name)
	}
	if _, ok := c.rels[r.name]; ok {
		return nil, gapEdgeVars.at(r.pos, "%s is a relationship: return or compare its endpoints instead", r.name)
	}
	return nil, errAt(r.pos, "unknown variable %s: a MATCH has to bind it", r.name)
}

// resolveProp finds where n's property lives: a column of one of n's labels' relations (label), n's
// key itself (identity), or a relation of facts about the node, the property's Derived one or the
// Props relation (kv). An unlabeled node takes the one label holding
// the property, and becomes a node of it.
func (c *compiler) resolveProp(n *nodeVar, r propRef) (label string, identity, kv bool, err *Error) {
	for _, l := range n.labels {
		nd, _ := c.g.node(l)
		if i, ok := nd.Props[r.key]; ok {
			return l, i == nd.Key, false, nil
		}
	}
	if _, ok := c.g.Derived[r.key]; ok || c.g.Props != nil {
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
			c.into(n.opt, frag{nodeAtm{n: n, label: owners[0]}}) // inside its OPTIONAL MATCH's braces, if one declared it
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
	return "", false, false, errAt(r.pos, "no property %s on %s:%s%s", r.key, r.v, strings.Join(n.labels, ":"), oneOf("properties", r.key, known))
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
		markValued(n)
		return frag{keyVal{n}}, nil
	case kv:
		return frag{"?" + c.kvRead(n, r.key)}, nil
	}
	s := c.slotFor(n, label, r.key)
	if s.jv == "" {
		s.jv = c.fresh(n.jv + "_" + r.key)
		c.name(s.jv, n.name+"."+r.key)
	}
	return frag{"?" + s.jv}, nil
}

// kvRead reads n's property key through its relation (kvAtom), once per scope: a property read inside
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
	c.name(jv, n.name+"."+key)
	m[k] = jv
	*c.scope = append(*c.scope, c.kvAtom(n, key, "?"+jv))
	return jv
}

// kvAtom is the fact holding n's property key with value val: an atom over the key's Derived
// relation, or over Props with the key written in.
func (c *compiler) kvAtom(n *nodeVar, key string, val any) frag {
	if d, ok := c.g.Derived[key]; ok {
		ar, _ := arity(c.v, d.Relation)
		args := make([]any, 0, 2*ar+2)
		args = append(args, d.Relation+"(")
		for i := 0; i < ar; i++ {
			if i > 0 {
				args = append(args, ", ")
			}
			k, fixed := d.Fixed[i]
			switch {
			case i == d.Node:
				args = append(args, keyArg{n})
			case i == d.Value:
				args = append(args, val)
			case fixed:
				args = append(args, quote(k))
			default:
				args = append(args, "_")
			}
		}
		return append(frag(args), ")")
	}
	key = quote(key)
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
		c.addRule(rule{head: frag{name + "(?n)"}, body: []frag{{c.nodeAtom(tmp, nd.Label, false)}}})
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
			return nil, nil, errAt(r.pos, "no relationship type %s in this graph%s", ty, oneOf("types", ty, c.g.typeNames()))
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
		return nil, gapPaths.at(r.pos, "a variable bound to a variable-length relationship: bind the path, p = (...), and read relationships(p)")
	}
	es, argss, err := c.edges(r)
	if err != nil {
		return nil, err
	}
	src, dst := any(keyArg{from}), any(keyArg{to})
	if r.dir == -1 {
		src, dst = dst, src
	}
	if !r.varLen && r.path == "" && len(es) == 1 && r.dir != 0 {
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
	helper := ""
	if len(es) > 1 || r.dir == 0 || hasParamIn(argss) {
		name := c.helper("step")
		helper = name
		for i, e := range es {
			c.addRule(rule{head: frag{name + "(?x, ?y)"}, body: []frag{edgeAtom(e, argss[i], "?x", "?y")}})
			if r.dir == 0 {
				c.addRule(rule{head: frag{name + "(?x, ?y)"}, body: []frag{edgeAtom(e, argss[i], "?y", "?x")}})
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
	if !r.varLen && r.path == "" {
		return step(src, dst), nil
	}
	if r.max != -1 && r.max < r.min {
		return nil, errAt(r.pos, "the range *%d..%d is empty", r.min, r.max)
	}
	// A variable length walks one step's relation with graph.closure, and a path variable's with
	// graph.paths: the helper above when there is one, the type's own relation when it has two
	// arguments, from and to, and otherwise a helper of one hop. A path relationships() reads walks a
	// helper of three, the edge's fact between its ends.
	walked := helper
	switch e := es[0]; {
	case r.edges:
		for _, e := range es {
			// a fact's id names an edge, and a relation the vocabulary derives holds no facts (#279)
			if en, err := c.v.Lookup(e.Relation); err != nil || en.Kind != ns.EntryBase {
				return nil, errAt(r.pos, "relationships(%s) reads each relationship's id, and %s is derived, so its relationships have none: read nodes(%s)", r.path, e.Type, r.path)
			}
		}
		walked = c.helper("wire")
		for i, e := range es {
			c.addRule(rule{head: frag{walked + "(?x, ?r, ?y)"}, body: []frag{append(edgeAtom(e, argss[i], "?x", "?y"), " as ?r")}})
			if r.dir == 0 {
				c.addRule(rule{head: frag{walked + "(?x, ?r, ?y)"}, body: []frag{append(edgeAtom(e, argss[i], "?y", "?x"), " as ?r")}})
			}
		}
		src, dst = keyArg{from}, keyArg{to}
		if r.dir == -1 {
			src, dst = dst, src
		}
	case helper != "":
	case len(argss[0]) == 2:
		walked = e.Relation
		if (e.From == 1) != (r.dir == -1) { // src and dst are the pattern's, the relation's columns may run the other way
			src, dst = dst, src
		}
	default:
		walked = c.helper("step")
		c.addRule(rule{head: frag{walked + "(?x, ?y)"}, body: []frag{step("?x", "?y")}})
	}
	lo, upTo := r.min, max(r.max, 0) // graph.closure and graph.paths read 0 as no bound
	if !r.varLen {
		lo, upTo = 1, 1
	}
	if r.path != "" {
		mode := "trail" // Cypher's: no relationship twice
		if r.selector == "shortest" {
			mode = "shortest"
		}
		return frag{fmt.Sprintf("graph.paths(%q, ", walked), src, ", ", dst, fmt.Sprintf(", %q, %d, %d, ?%s)", mode, lo, upTo, c.values[r.path])}, nil
	}
	return frag{fmt.Sprintf("graph.closure(%q, ", walked), src, ", ", dst, fmt.Sprintf(", %d, %d, _)", lo, upTo)}, nil
}

// value is e as an operand of a comparison: a constant, a parameter, a variable or arithmetic over
// at least one variable or parameter, since jaala computes only from what a body binds.
func (c *compiler) value(e expr) (frag, *Error) {
	switch e.(type) {
	case binary, minus:
		if !isConst(e) && !c.readsVar(e) {
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
		if c.paramVals != nil {
			return c.constant(e.name, e.pos)
		}
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
		if jv := c.values[e.name]; jv != "" {
			return frag{"?" + jv}, nil
		}
		n, err := c.nodeRef(e)
		if err != nil {
			return nil, err
		}
		markValued(n)
		return frag{keyVal{n}}, nil
	case propRef:
		return c.prop(e)
	case subquery:
		if !e.count {
			return nil, errAt(e.pos, "EXISTS { } is a test, not a value: write it in WHERE")
		}
		return c.countSubquery(e)
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
			l, err := c.value(e.l)
			if err != nil {
				return nil, err
			}
			r, err := c.value(e.r)
			if err != nil {
				return nil, err
			}
			if e.op == "+" && (c.isText(e.l) || c.isText(e.r)) {
				return frag{"str.concat(", l, ", ", r, ")"}, nil
			}
			return frag{"(", l, " " + e.op + " ", r, ")"}, nil
		case "%", "^":
			return nil, gapFunctions.at(e.pos, "%s", e.op)
		}
		return nil, errAt(e.pos, "%s is a test, not a value", e.op)
	case call:
		if fn, ok := textFuncs[strings.ToLower(e.name)]; ok {
			if len(e.args) != fn.args {
				return nil, errAt(e.pos, "%s() takes %d arguments", e.name, fn.args)
			}
			f := frag{fn.name + "("}
			for i, a := range e.args {
				v, err := c.value(a)
				if err != nil {
					return nil, err
				}
				if i > 0 {
					f = append(f, ", ")
				}
				f = append(f, v)
			}
			return append(f, ")"), nil
		}
		switch strings.ToLower(e.name) {
		case "split":
			return nil, gapLists.at(e.pos, "split() as a list: take one part with split(s, sep)[i]")
		case "id", "elementid":
			if len(e.args) == 1 {
				if v, ok := e.args[0].(varRef); ok {
					return c.value(v)
				}
			}
		case "type", "startnode", "endnode":
			return nil, gapEdgeVars.at(e.pos, "%s()", e.name)
		case "length", "nodes", "relationships":
			if p, ok := c.pathArg(e); ok {
				return frag{pathFuncs[strings.ToLower(e.name)] + "(?" + p + ")"}, nil
			}
			return nil, errAt(e.pos, "%s() reads a path variable, bound by p = (...)", e.name)
		case "size", "head", "last", listIndex:
			// a list's length and values (#283), read by stdlib's list.*
			if want := 1; e.name != listIndex && len(e.args) != want {
				return nil, errAt(e.pos, "%s() takes one argument", e.name)
			}
			l, err := c.value(e.args[0])
			if err != nil {
				return nil, err
			}
			switch strings.ToLower(e.name) {
			case "size":
				return frag{"list.length(", l, ")"}, nil
			case "head":
				return frag{"list.at(", l, ", 0)"}, nil
			case "last":
				return frag{"list.at(", l, ", -1)"}, nil
			}
			i, err := c.value(e.args[1])
			if err != nil {
				return nil, err
			}
			return frag{"list.at(", l, ", ", i, ")"}, nil
		case "labels", "keys", "properties", "range", "tail":
			return nil, gapLists.at(e.pos, "%s()", e.name)
		}
		if aggregates[strings.ToLower(e.name)] {
			return nil, errAt(e.pos, "%s() aggregates, so it can only be returned", e.name)
		}
		return nil, gapFunctions.at(e.pos, "%s()", e.name)
	case listLit:
		return c.listConst(e)
	case unsupported:
		return nil, e.err
	}
	return nil, errAt(e.at(), "a test can't be a value here")
}

// pathFuncs are Cypher's functions of a path, which stdlib's path.* compute from the path's value
// (#280): relationships() lists its edges' facts, which a path variable relationships() reads walks.
var pathFuncs = map[string]string{"length": "path.length", "nodes": "path.nodes", "relationships": "path.edges"}

// pathArg is the jaala variable of the one value variable e is called on, a path bound by p = (...) or
// carried by a WITH.
func (c *compiler) pathArg(e call) (string, bool) {
	if len(e.args) != 1 {
		return "", false
	}
	v, ok := e.args[0].(varRef)
	if !ok || c.values[v.name] == "" {
		return "", false
	}
	return c.values[v.name], true
}

// textFuncs are Cypher's string functions that stdlib's str.* compute (#360), each a relation with
// its result last, so an expression calls it.
var textFuncs = map[string]struct {
	name string
	args int
}{
	"tolower": {"str.lower", 1}, "lower": {"str.lower", 1},
	"toupper": {"str.upper", 1}, "upper": {"str.upper", 1},
	"replace":  {"str.replace", 3},
	splitIndex: {"str.split", 3},
}

// isText reports whether e is text a + joins rather than adds: a string, a string function, a join,
// or a property its relation's schema (or a Derived predicate's Types) types as text.
func (c *compiler) isText(e expr) bool {
	switch e := e.(type) {
	case strLit:
		return true
	case binary:
		return e.op == "+" && (c.isText(e.l) || c.isText(e.r))
	case call:
		_, ok := textFuncs[strings.ToLower(e.name)]
		return ok
	case propRef:
		n, ok := c.nodes[e.v]
		if !ok {
			return false
		}
		for _, l := range n.labels {
			nd, _ := c.g.node(l)
			if i, ok := nd.Props[e.key]; ok {
				if s, ok := c.v.Schema(nd.Relation); ok && i < len(s.Types) {
					return s.Types[i].Type == ns.TypeString
				}
				return false
			}
		}
		if d, ok := c.g.Derived[e.key]; ok {
			types := argTypes(c.v, d.Relation)
			return d.Value < len(types) && types[d.Value].Type == ns.TypeString
		}
	}
	return false
}

// term is e as a predicate's argument, which takes a variable or a constant: arithmetic is computed
// into a fresh variable first.
func (c *compiler) term(e expr) (frag, *Error) {
	f, err := c.value(e)
	if err != nil {
		return nil, err
	}
	if isVar(f) || isConst(e) {
		return f, nil
	}
	switch e.(type) {
	case binary, minus, call:
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
			return append(frag{l, " " + op + " ", r}, c.guards(op, e.l, e.r, l, r)...), nil
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
			return append(frag{not + pred + "(", l, ", ", r, ")"}, c.guards(e.op, e.l, e.r, l, r)...), nil
		case "IN":
			x, err := c.term(e.l)
			if err != nil {
				return nil, err
			}
			var g frag
			if neg && c.nullable(e.l) && isVar(x) {
				g = frag{", not absent(", x, ")"} // null IN a list is unknown, which NOT keeps unknown
			}
			if f, ok := e.r.(call); ok && strings.EqualFold(f.name, "nodes") {
				// x IN nodes(p): the path passes through x
				p, ok := c.pathArg(f)
				if !ok {
					return nil, errAt(e.pos, "nodes() reads a path variable, bound by p = (...)")
				}
				return append(frag{not + "path.node(?" + p + ", _, ", x, ")"}, g...), nil
			}
			// x IN l, a list a value, a property or a parameter holds (#283); an expression is computed
			// into a variable first, since an atom's argument is a term (FuzzCompile: 0 IN -(a))
			l, err := c.term(e.r)
			if err != nil {
				return nil, err
			}
			return append(frag{not + "list.member(", l, ", _, ", x, ")"}, g...), nil
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
			return append(frag{not + "str.match(", l, ", ", re, ")"}, c.guards("=~", e.l, e.r, l, nil)...), nil
		}
	case isNull:
		null := !e.not
		if neg {
			null = !null
		}
		if v, ok := e.e.(varRef); ok {
			// a node or a value an OPTIONAL MATCH left absent (#282)
			f, err := c.value(v)
			if err != nil {
				return nil, err
			}
			if null {
				return frag{"absent(", f, ")"}, nil
			}
			return frag{"not absent(", f, ")"}, nil
		}
		r, ok := e.e.(propRef)
		if !ok {
			return nil, errAt(e.pos, "IS NULL tests a property or a variable")
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
				return append(frag{"not "}, c.kvAtom(n, r.key, "_")...), nil
			}
			return c.kvAtom(n, r.key, "_"), nil
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
				return nil, errAt(e.pos, "no label %s in this graph%s", l, oneOf("labels", l, c.g.labelNames()))
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
		return c.patternTest([]pattern{e.p}, nil, false, neg)
	case subquery:
		if e.count {
			return nil, errAt(e.pos, "COUNT { } is a number, not a test: compare it, COUNT { ... } > 0")
		}
		return c.patternTest(e.patterns, e.where, true, neg)
	case call:
		if strings.EqualFold(e.name, "exists") && len(e.args) == 1 {
			if r, ok := e.args[0].(propRef); ok {
				return c.condition(isNull{pos: e.pos, e: r, not: true}, neg)
			}
			if p, ok := e.args[0].(patExpr); ok {
				return c.patternTest([]pattern{p.p}, nil, false, neg)
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

// patternTest is a pattern in WHERE, or EXISTS { }: a helper relation over the nodes it shares with
// the MATCH, a rule for each alternative its WHERE has, called (or negated) on them.
func (c *compiler) patternTest(pats []pattern, where expr, allowNew, neg bool) (frag, *Error) {
	bodies, anchors, _, err := c.subBody(pats, where, allowNew)
	if err != nil {
		return nil, err
	}
	name := c.helper("exists")
	head := frag{name + "("}
	call := frag{name + "("}
	for i, a := range anchors {
		for n := a; n != nil; n = n.of {
			n.anchor = true
		}
		if i > 0 {
			head, call = append(head, ", "), append(call, ", ")
		}
		head, call = append(head, keyArg{a}), append(call, keyArg{a.of})
	}
	head = append(head, ")")
	for _, body := range bodies {
		c.addRule(rule{head: head, body: body})
	}
	call = append(call, ")")
	if neg {
		return append(frag{"not "}, call...), nil
	}
	return call, nil
}

// countSubquery is COUNT { }: a body aggregate counting the pattern's matches from the nodes it shares,
// so a node it never matches from counts 0.
func (c *compiler) countSubquery(sq subquery) (frag, *Error) {
	bodies, anchors, inner, err := c.subBody(sq.patterns, sq.where, true)
	if err != nil {
		return nil, err
	}
	if len(bodies) > 1 {
		return nil, errAt(sq.pos, "COUNT { } over a WHERE with OR: count each alternative and add them")
	}
	var counted any
	if len(inner) > 0 {
		counted = keyVal{inner[0]}
	} else {
		markValued(anchors[0])
		counted = keyVal{anchors[0]}
	}
	jv := c.fresh("_count")
	*c.scope = append(*c.scope, frag{"?" + jv + " = count(", counted, ") : { ", braces{lits: bodies[0]}, " }"})
	return frag{"?" + jv}, nil
}

// subBody compiles a subquery's patterns and WHERE, a body per alternative, with the nodes it shares
// with the stage (anchors, each a stand-in for the stage's node, so reading one's column reads it
// here) and those it introduces (inner, only with allowNew).
func (c *compiler) subBody(pats []pattern, where expr, allowNew bool) (bodies [][]frag, anchors, inner []*nodeVar, err *Error) {
	saved, savedShared, savedScope, savedOrder := c.nodes, c.shared, c.scope, c.order
	c.nodes = map[string]*nodeVar{}
	defer func() { c.nodes, c.shared, c.scope, c.order = saved, savedShared, savedScope, savedOrder }()
	var body []frag
	c.shared, c.scope = nil, &body
	stand := map[*nodeVar]*nodeVar{}
	var conds []expr
	for _, p := range pats {
		var ns []*nodeVar
		for _, np := range p.nodes {
			if outer, ok := saved[np.v]; ok && np.v != "" {
				if len(np.props) > 0 {
					return nil, nil, nil, errAt(np.pos, "write %s's properties in the MATCH, or as a WHERE inside the subquery", np.v)
				}
				a := stand[outer]
				if a == nil {
					a = &nodeVar{name: outer.name, jv: outer.jv, labels: append([]string(nil), outer.labels...),
						slots: map[string]*slot{}, endpoint: true, carried: true, of: outer}
					stand[outer] = a
					c.nodes[np.v] = a
					anchors = append(anchors, a)
					for _, l := range a.labels {
						body = append(body, frag{nodeAtm{n: a, label: l, lazy: true}})
					}
				}
				for _, l := range np.labels {
					if _, ok := c.g.node(l); !ok {
						return nil, nil, nil, errAt(np.pos, "no label %s in this graph%s", l, oneOf("labels", l, c.g.labelNames()))
					}
					body = append(body, frag{nodeAtm{n: a, label: l}})
				}
				ns = append(ns, a)
				continue
			}
			if np.v != "" && !allowNew && c.nodes[np.v] == nil {
				return nil, nil, nil, errAt(np.pos, "a pattern in WHERE can't introduce %s: a MATCH has to bind it, or write EXISTS { ... }", np.v)
			}
			known := c.nodes[np.v] != nil && np.v != ""
			n, err := c.declareNode(np, &conds)
			if err != nil {
				return nil, nil, nil, err
			}
			if !known && !containsNode(inner, n) {
				inner = append(inner, n)
				for _, l := range n.labels {
					body = append(body, frag{nodeAtm{n: n, label: l}})
				}
			} else {
				for _, l := range np.labels {
					body = append(body, frag{nodeAtm{n: n, label: l}})
				}
			}
			ns = append(ns, n)
		}
		for i, r := range p.rels {
			if r.v != "" {
				return nil, nil, nil, gapEdgeVars.at(r.pos, "a relationship variable in a subquery")
			}
			ns[i].endpoint, ns[i+1].endpoint = true, true
			f, err := c.relLiteral(r, ns[i], ns[i+1])
			if err != nil {
				return nil, nil, nil, err
			}
			body = append(body, f)
		}
	}
	if len(anchors) == 0 {
		return nil, nil, nil, errAt(pats[0].pos, "a pattern in WHERE has to share a variable with the MATCH")
	}
	// A node of its own that nothing else matches ranges over every node, as in a MATCH.
	for _, n := range inner {
		if len(n.labels) == 0 && !n.endpoint {
			f, err := c.anyNode(n)
			if err != nil {
				return nil, nil, nil, err
			}
			body = append(body, f)
		}
	}
	// A subquery of shared nodes alone matches them by their labels.
	if len(inner) == 0 && !hasRels(pats) {
		for _, a := range anchors {
			for _, l := range a.labels {
				body = append(body, frag{nodeAtm{n: a, label: l}})
			}
		}
		if len(body) == 0 {
			return nil, nil, nil, errAt(pats[0].pos, "this subquery matches nothing of its own: give it a relationship or a label")
		}
	}
	if where != nil {
		conds = append(conds, conjuncts(where)...)
	}
	ds, err := c.dnf(conds)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, d := range ds {
		var lits []frag
		c.scope = &lits
		for _, cd := range d {
			f, err := c.condition(cd.e, cd.neg)
			if err != nil {
				return nil, nil, nil, err
			}
			lits = append(lits, f)
		}
		bodies = append(bodies, append(append(append([]frag{}, body...), c.shared...), lits...))
	}
	return bodies, anchors, inner, nil
}

func hasRels(pats []pattern) bool {
	for _, p := range pats {
		if len(p.rels) > 0 {
			return true
		}
	}
	return false
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
		if err == errTooManyDisjuncts {
			return nil, gapDisjuncts.at(e.at(), "this WHERE expands to more than %d alternatives", maxDisjuncts)
		}
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
			return nil, gapDisjuncts.at(e.at(), "this WHERE expands to more than %d alternatives", maxDisjuncts)
		}
		out = next
	}
	return out, nil
}

// errTooManyDisjuncts stops dnfOf as soon as a part of the expression passes maxDisjuncts, which
// dnf reports at the WHERE: built whole first, a chain of n XORs took 2^n alternatives to refuse
// (#377).
var errTooManyDisjuncts = &Error{Msg: "too many alternatives"}

func (c *compiler) dnfOf(e expr, neg bool) ([][]cond, *Error) {
	or := func(a, b [][]cond) ([][]cond, *Error) {
		if len(a)+len(b) > maxDisjuncts {
			return nil, errTooManyDisjuncts
		}
		return append(append([][]cond{}, a...), b...), nil
	}
	and := func(a, b [][]cond) ([][]cond, *Error) {
		if len(a)*len(b) > maxDisjuncts {
			return nil, errTooManyDisjuncts
		}
		var out [][]cond
		for _, x := range a {
			for _, y := range b {
				out = append(out, append(append([]cond{}, x...), y...))
			}
		}
		return out, nil
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
				return and(l, r)
			}
			return or(l, r)
		case "XOR":
			x := binary{pos: e.pos, op: "OR",
				l: binary{pos: e.pos, op: "AND", l: e.l, r: negate{pos: e.pos, e: e.r}},
				r: binary{pos: e.pos, op: "AND", l: negate{pos: e.pos, e: e.l}, r: e.r}}
			return c.dnfOf(x, neg)
		case "IN":
			list, ok := e.r.(listLit)
			if !ok {
				return [][]cond{{{e: e, neg: neg}}}, nil // a test of its own: path.node, or list.member (#283)
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
	agg  bool
	vars []string
	key  string // the expression, for ORDER BY to find it
}

type returned struct {
	cols  []column
	order []string
	limit *int
	skip  *int
}

func (c *compiler) returning(r returnClause) (returned, *Error) {
	var out returned
	items := r.items
	if r.star {
		items = append(c.starItems(r.pos), r.items...)
		if len(items) == 0 {
			return out, errAt(r.pos, "RETURN * with no named variable")
		}
	}
	for _, it := range items {
		c.cur = startOf(it.e)
		from := len(*c.scope)
		col, err := c.column(it)
		if err != nil {
			return out, err
		}
		for i := from; i < len(*c.scope); i++ {
			(*c.scope)[i] = at(startOf(it.e), (*c.scope)[i])
		}
		col.text = at(startOf(it.e), col.text)
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
		case fn.star && agg == "count" && len(c.order) > 0:
			markValued(c.order[0])
			arg = frag{keyVal{c.order[0]}}
		case fn.star && agg == "count" && len(c.vorder) > 0:
			arg = frag{"?" + c.values[c.vorder[0]]}
		case fn.star && agg == "count":
			return col, errAt(fn.pos, "count(*) with nothing matched")
		case len(fn.args) != 1:
			return col, errAt(fn.pos, "%s() takes one argument", fn.name)
		case agg == "count" && !fn.distinct && c.optionalNode(fn.args[0]) != nil:
			// Cypher's count skips a null: sum what the node's braces bind to 1, absent without a match
			part := c.opts[c.optionalNode(fn.args[0]).opt-1]
			if part.seen == "" {
				part.seen = c.fresh("_seen")
			}
			col.text, col.agg = frag{"sum(?" + part.seen + ")"}, true
			return col, nil
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
		col.text, col.agg = frag{agg + "(" + d, arg, ")"}, true
		return col, nil
	}
	switch it.e.(type) {
	case paramRef:
		return col, errAt(it.e.at(), "RETURN of a parameter: its value is the one the host binds")
	}
	if _, list := it.e.(listLit); isConst(it.e) || list {
		return col, errAt(it.e.at(), "RETURN of a constant: jaala's columns are variables")
	}
	f, err := c.term(it.e)
	if err != nil {
		return col, err
	}
	col.text = f
	return col, nil
}

// optionalNode is the node e names when an OPTIONAL MATCH declared it, nil otherwise.
func (c *compiler) optionalNode(e expr) *nodeVar {
	if v, ok := e.(varRef); ok {
		if n := c.nodes[v.name]; n != nil && n.opt > 0 {
			return n
		}
	}
	return nil
}

// nullable reports whether e may be absent where Cypher's would be null (#282): a node or a value an
// OPTIONAL MATCH binds, a property one of its nodes has, a property a label's column holds (a source
// may leave it unstated), and a value a WITH carried (an optional's, or an aggregate's over nothing).
// A node's key and a property read through Props or a Derived relation are matches, never absent.
func (c *compiler) nullable(e expr) bool {
	switch e := e.(type) {
	case varRef:
		if n := c.nodes[e.name]; n != nil {
			return n.absent || n.opt > 0 && n.opt != c.inPart
		}
		return c.values[e.name] != ""
	case propRef:
		n := c.nodes[e.v]
		if n == nil {
			return false
		}
		if n.absent || n.opt > 0 && n.opt != c.inPart {
			return true
		}
		_, identity, kv, err := c.resolveProp(n, e)
		return err == nil && !identity && !kv
	}
	return false
}

// guards are the not absent(...) a test over nullable operands needs to answer as Cypher's does, where
// a comparison with null is unknown and filters its row: jaala's absent equals absent and differs
// from anything else, so = needs both sides guarded and <> or a text test each nullable one. An
// operand computed by a function gives no row over absent already, so only a variable is guarded.
func (c *compiler) guards(op string, l, r expr, lf, rf frag) frag {
	var g frag
	guard := func(e expr, f frag) {
		if c.nullable(e) && isVar(f) {
			g = append(g, ", not absent(", f, ")")
		}
	}
	switch op {
	case "=":
		if c.nullable(l) && c.nullable(r) {
			guard(l, lf)
			guard(r, rf)
		}
	case "!=", "<>", "STARTS WITH", "ENDS WITH", "CONTAINS", "=~":
		guard(l, lf)
		guard(r, rf)
	}
	return g
}

// listConst is a list written in the query as a value: its constants as a list's text (ns.ListText),
// written as a jaala string, whose quotes stay escaped in its value and list.* reads unescaped (#283).
func (c *compiler) listConst(l listLit) (frag, *Error) {
	vals := make([]ns.Value, 0, len(l.items))
	for _, it := range l.items {
		switch it := it.(type) {
		case strLit:
			if strings.ContainsAny(it.s, `"\`) {
				return nil, errAt(it.pos, "a string in a list holding a double quote or a backslash can't be written in jaala")
			}
			vals = append(vals, ns.S(it.s))
		case numLit:
			f, err := strconv.ParseFloat(it.text, 64)
			if err != nil {
				return nil, errAt(it.pos, "%s isn't a number jaala reads", it.text)
			}
			vals = append(vals, ns.N(f))
		default:
			return nil, gapLists.at(it.at(), "a list of anything but numbers and strings written in the query")
		}
	}
	return frag{quote(strings.ReplaceAll(ns.ListText(vals), `"`, `\"`))}, nil
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

var varRe = regexp.MustCompile(`\?[A-Za-z0-9_]+`)

func varsIn(s string) []string { return varRe.FindAllString(s, -1) }
