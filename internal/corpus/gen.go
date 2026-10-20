package corpus

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"slices"
	"strconv"
	"strings"

	"github.com/panyam/jaala/ns"
)

// The generated corpus: random programs over random graphs, each answered by every evaluator through
// agree, the comparison both makes. Hand-written tests only reach the shapes their authors thought
// of; this reaches the rewrites (unfold, magic, plan, factor, supplementary relations) with ones
// nobody wrote. A disagreement is shrunk to a small program and graph before it is reported.
//
// A program is built in the structures below and printed as text, rather than as a Query, so
// shrinking edits a small tree and the Soufflé translator (#23) can print the same programs.
//
// JAALA_GEN_SEEDS sets how many seeds run (selfcheck.sh runs thousands), and JAALA_GEN_SEED=n
// replays the one seed a failure names.

// genType is a column's type. Node columns hold node names, number columns weights; a program keeps
// them apart, so a constant is never refused for its type (#65) and a comparison never orders a
// number against a word.
type genType int

const (
	genNode genType = iota
	genNum
)

// genTypes walks a body's bound variables in a fixed order: ranging over the map draws in Go's
// random order, so one seed would generate different programs (#226).
var genTypes = []genType{genNode, genNum}

// genRel is a relation a program reads: one of the graph's, or one it derives. Derived relations are
// layered. A body reads its own layer only positively (recursion, mutual within a layer) and lower
// layers freely, so every program stratifies. An aggregating relation sits alone in its layer, since
// stratify makes its body edges strict.
type genRel struct {
	name  string
	cols  []genType
	layer int
	agg   bool
}

var genGraphRels = []genRel{
	{name: "edge", cols: []genType{genNode, genNode}, layer: -1},
	{name: "node", cols: []genType{genNode}, layer: -1},
	{name: "weight", cols: []genType{genNode, genNum}, layer: -1},
}

// genLit is a body literal: an atom, a negated atom, an aggregate over a body of its own, or (rel
// empty) the comparison args[0] op args[1]. Args are printed terms: "?x", "_", `"v1"`, "3".
type genLit struct {
	neg  bool
	rel  string
	args []string
	op   string
	agg  *genAgg
	fact string   // the variable `as` binds the atom's fact to (#279), "" for none
	opt  []genLit // optional { opt } (#282), the literal's only content
}

// genAgg is `result = fn : { body }` (#71).
type genAgg struct {
	result, fn string
	body       []genLit
}

func (l genLit) String() string {
	if l.opt != nil {
		return "optional { " + joinLits(l.opt) + " }"
	}
	if l.agg != nil {
		return l.agg.result + " = " + l.agg.fn + " : { " + joinLits(l.agg.body) + " }"
	}
	if l.rel == "" {
		return l.args[0] + " " + l.op + " " + l.args[1]
	}
	s := l.rel + "(" + strings.Join(l.args, ", ") + ")"
	if l.neg {
		s = "not " + s
	}
	if l.fact != "" {
		s += " as " + l.fact
	}
	return s
}

type genRule struct {
	head string
	args []string // terms or aggregates, "count(distinct ?b)"
	body []genLit
}

func (r genRule) String() string {
	return r.head + "(" + strings.Join(r.args, ", ") + ") :- " + joinLits(r.body)
}

type genGoal struct {
	body   []genLit
	sel    []string
	having string
	order  string
	limit  int
	bind   map[string][]string // variable (no "?") -> node names the host binds it to
}

// A Program is a generated program kept as data, printed by String, so shrinking edits a small
// tree and the Soufflé translator can print the same programs.
type Program struct {
	rules  []genRule
	goal   genGoal
	shapes []string
}

func (p Program) String() string {
	var b strings.Builder
	for _, r := range p.rules {
		b.WriteString(r.String() + "; ")
	}
	g := p.goal
	b.WriteString(joinLits(g.body) + " => " + strings.Join(g.sel, ", "))
	if g.having != "" {
		b.WriteString(" having " + g.having)
	}
	if g.order != "" {
		b.WriteString(" order by " + g.order)
	}
	if g.limit > 0 {
		b.WriteString(" limit " + strconv.Itoa(g.limit))
	}
	return b.String()
}

func joinLits(lits []genLit) string {
	out := make([]string, len(lits))
	for i, l := range lits {
		out[i] = l.String()
	}
	return strings.Join(out, ", ")
}

// Facts is a generated graph kept as data, so shrinking can drop an edge or a node: nodes v0 to
// vn-1, their weights and units, and the edges between them.
type Facts struct {
	n       int
	edges   [][2]int
	weights []int
	units   []string // each weight's unit, "" for none; nil when no weight has one (#192)
}

// Graph draws a graph from rnd, before New draws its program from the same rnd.
func Graph(rnd *rand.Rand) Facts {
	f := Facts{n: 3 + rnd.Intn(6)}
	p := 0.1 + rnd.Float64()*0.25
	for i := 0; i < f.n; i++ {
		f.weights = append(f.weights, rnd.Intn(10))
		for j := 0; j < f.n; j++ {
			if rnd.Float64() < p {
				f.edges = append(f.edges, [2]int{i, j})
			}
		}
	}
	f.units = genUnits(f)
	return f
}

// genUnits gives a graph's weights units now and then (#192): most graphs none, some every weight
// in volts, and some a mix of volts, amps and none. It draws from a source seeded by the graph
// itself, so a seed's program is what it was before units.
func genUnits(f Facts) []string {
	h := fnv.New64a()
	fmt.Fprint(h, f.n, f.edges, f.weights)
	rnd := rand.New(rand.NewSource(int64(h.Sum64()))) // #nosec G115 G404 -- as stream's: a seeded source of test data
	var units []string
	switch p := rnd.Float64(); {
	case p < 0.7:
		return nil
	case p < 0.85:
		units = slices.Repeat([]string{"V"}, f.n)
	default:
		units = make([]string, f.n)
		for i := range units {
			units[i] = []string{"V", "A", ""}[rnd.Intn(3)]
		}
	}
	// Half of these spell their volts other ways now and then, which the Base folds back to V as it
	// reads them (#205). Drawn after the units, so a graph's units are what they were.
	if rnd.Float64() < 0.5 {
		for i, u := range units {
			if u == "V" {
				units[i] = voltSpellings[rnd.Intn(len(voltSpellings))]
			}
		}
	}
	return units
}

// voltSpellings are the ways a weight in volts is served (#205): as it is, prefixed (w V as w*1000
// mV or w/1000 kV) and as a product the vocabulary names V. Each folds to w V exactly.
var voltSpellings = []string{"V", "mV", "kV", "A*ohm"}

// Folded is the unit u reads as once the Base folds it, V for each of voltSpellings.
func Folded(u string) string {
	if slices.Contains(voltSpellings, u) {
		return "V"
	}
	return u
}

// MixedUnits reports whether the weights carry more than one unit, or a unit beside none, which
// Soufflé can't express: its numbers have no units. A unit counts as it reads once folded, so mV and
// V are one.
func (f Facts) MixedUnits() bool {
	for _, u := range f.units {
		if Folded(u) != Folded(f.units[0]) {
			return true
		}
	}
	return false
}

// Prefixed reports whether a weight is served in a unit the Base folds (#205).
func (f Facts) Prefixed() bool {
	for _, u := range f.units {
		if Folded(u) != u {
			return true
		}
	}
	return false
}

// Source serves the graph as edge, node and weight, as the corpus evaluates it.
func (f Facts) Source() *ns.MemSource {
	src := ns.NewMemSource().Declare("edge", "from", "to").Declare("node", "name").Declare("weight", "node", "w")
	name := func(i int) ns.Value { return ns.S(fmt.Sprintf("v%d", i)) }
	for i := 0; i < f.n; i++ {
		src.Add("node", ns.Tuple{Vals: []ns.Value{name(i)}, Cites: []string{fmt.Sprintf("node:%d", i)}})
		unit := ""
		if f.units != nil {
			unit = f.units[i]
		}
		w := float64(f.weights[i])
		switch unit {
		case "mV":
			w *= 1000
		case "kV":
			w /= 1000
		}
		src.Add("weight", ns.Tuple{Vals: []ns.Value{name(i), ns.NU(w, unit)}})
	}
	for k, e := range f.edges {
		src.Add("edge", ns.Tuple{Vals: []ns.Value{name(e[0]), name(e[1])}, Cites: []string{fmt.Sprintf("edge:%d-%d", e[0], e[1])}, ID: edgeID(k)})
	}
	return src
}

// edgeID is the k-th edge's id: e<k>, but every third has none, so the engine names it (#279).
func edgeID(k int) string {
	if k%3 == 0 {
		return ""
	}
	return fmt.Sprintf("e%d", k)
}

func (f Facts) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "nodes v0..v%d, weights %v, edges", f.n-1, f.weights)
	if f.units != nil {
		b.Reset()
		fmt.Fprintf(&b, "nodes v0..v%d, weights %v in units %q, edges", f.n-1, f.weights, f.units)
	}
	for _, e := range f.edges {
		fmt.Fprintf(&b, " v%d->v%d", e[0], e[1])
	}
	return b.String()
}

// A Generator draws one program over its graph.
type Generator struct {
	rnd    *rand.Rand
	nodes  int
	rels   []genRel
	vars   int
	shapes map[string]bool
	// arith draws the expressions a numeric comparison writes (#209), from a stream of its own so a
	// seed's program is what it was before them, wherever none is written. Nil for a graph whose
	// weights carry units: V times V is 25 V^2, which Soufflé, comparing by text, reads as 25.
	arith *rand.Rand
	// recursive is set while a recursive rule's body is drawn, which writes no expression: a computing
	// rule that reaches itself is refused (#207).
	recursive bool
	// facts draws where an edge binds its fact and where a program reads one (#279), from a stream of
	// its own, as arith does, so a seed binding none is the program it was; nfacts numbers them.
	facts  *rand.Rand
	nfacts int
	// avgs turns a drawn sum into an avg now and then (#284), from a stream of its own, so a seed
	// writing no sum is the program it was.
	avgs *rand.Rand
	// closures turns a literal of two nodes into graph.closure over its relation now and then (#281),
	// from a stream of its own, as facts does; nclosures numbers the hops it binds.
	closures  *rand.Rand
	nclosures int
	// consts binds a fresh variable by an equality with a constant now and then (#457), from a stream
	// of its own, so a seed binding none is the program it was; nconsts numbers them.
	consts  *rand.Rand
	nconsts int
	// walks turns a literal of two nodes that closures left alone into graph.paths now and then (#280),
	// from a stream of its own, so a seed's closures are the ones it drew before; npaths numbers the
	// paths it binds.
	walks  *rand.Rand
	npaths int
	// optionals wraps an atom over a graph relation in optional { } now and then (#282), from a stream
	// of its own.
	optionals *rand.Rand
	// lists makes the goal read back a list a rule collected now and then (#283), from a stream of its
	// own; listRules are those rules, which program adds.
	lists     *rand.Rand
	listRules []genRule
	// steps makes a closure or paths call walk a one-atom rule over its relation now and then rather
	// than the relation (#438), the step walkSteps walks through, from a stream of its own; stepRules
	// are those rules, which Program adds.
	steps     *rand.Rand
	stepRules []genRule
}

// New draws a program for facts from rnd. Arithmetic depends on the graph's units as drawn,
// before the Soufflé step drops mixed ones, so the corpus and the Soufflé step generate one program.
func New(rnd *rand.Rand, facts Facts) *Generator {
	g := &Generator{rnd: rnd, nodes: facts.n}
	g.facts = stream("facts", facts)
	g.avgs = stream("avgs", facts)
	g.closures = stream("closures", facts)
	g.walks = stream("walks", facts)
	g.optionals = stream("optionals", facts)
	g.lists = stream("lists", facts)
	g.steps = stream("steps", facts)
	g.consts = stream("consts", facts)
	if facts.units == nil {
		g.arith = stream("arith", facts)
	}
	return g
}

// stream is a source of its own for one kind of draw, seeded by the graph, so adding a kind leaves
// every other seed's program as it was.
func stream(name string, facts Facts) *rand.Rand {
	h := fnv.New64a()
	fmt.Fprint(h, name, facts.n, facts.edges, facts.weights)
	return rand.New(rand.NewSource(int64(h.Sum64()))) // #nosec G115 G404 -- a seed replays its program: the hash's bits are the seed, and the draws are test data, not secrets
}

func (g *Generator) chance(p float64) bool { return g.rnd.Float64() < p }
func (g *Generator) pick(n int) int        { return g.rnd.Intn(n) }

// constant names a node one past the graph now and then, so a constant can match nothing.
func (g *Generator) constant(t genType) string {
	if t == genNum {
		return strconv.Itoa(g.pick(10))
	}
	return fmt.Sprintf(`"v%d"`, g.pick(g.nodes+1))
}

func (g *Generator) fresh() string {
	g.vars++
	return fmt.Sprintf("?x%d", g.vars)
}

// genBody is a body being built and the variables its positive literals have bound so far, by type.
// Literals are added in an order Naive can run as written: a comparison or a negation only reads
// variables already bound.
type genBody struct {
	lits     []genLit
	facts    []string // the facts its edges bind (#279)
	bound    map[genType][]string
	results  map[string]bool // what a body aggregate binds
	computed []string        // what ?y = expr binds (#209), which a head or the goal reads now and then
	consts   map[string]bool // what ?c = constant binds (#457), which no aggregate reduces
}

func newBody() *genBody { return &genBody{bound: map[genType][]string{}} }

func (b *genBody) all() []string { return append(slices.Clone(b.bound[genNode]), b.bound[genNum]...) }

// bind puts constant c in place of literal i's argument j, so a demand rewrite sees a bound call. A
// variable that appeared only there is no longer bound.
func (b *genBody) bind(i, j int, c string) {
	old := b.lits[i].args[j]
	b.lits[i].args[j] = c
	for _, l := range b.lits {
		if !l.neg && slices.Contains(l.args, old) {
			return
		}
	}
	for t, vars := range b.bound {
		b.bound[t] = slices.DeleteFunc(vars, func(v string) bool { return v == old })
	}
}

func (g *Generator) atom(b *genBody, r genRel, neg bool) {
	args := make([]string, len(r.cols))
	type binding struct {
		t genType
		v string
	}
	var fresh []binding
	for i, t := range r.cols {
		vars := b.bound[t]
		switch {
		case g.chance(0.1):
			args[i] = g.constant(t)
		case g.chance(0.08):
			args[i] = "_"
		case len(vars) > 0 && (neg || g.chance(0.6)):
			args[i] = vars[g.pick(len(vars))]
		case neg:
			args[i] = "_"
		default:
			args[i] = g.fresh()
			fresh = append(fresh, binding{t, args[i]})
		}
	}
	lit := genLit{neg: neg, rel: r.name, args: args}
	if !neg && r.name == "edge" && g.facts != nil && g.facts.Float64() < 0.2 {
		g.nfacts++
		lit.fact = fmt.Sprintf("?f%d", g.nfacts)
		b.facts = append(b.facts, lit.fact)
		g.shapes["bound fact"] = true
	}
	b.lits = append(b.lits, lit)
	for _, f := range fresh {
		b.bound[f.t] = append(b.bound[f.t], f.v)
	}
	if neg {
		g.shapes["negation"] = true
	}
}

// binding makes sure a variable of type t is bound, adding node(?x) or weight(?n, ?w) when none is.
func (g *Generator) binding(b *genBody, t genType) string {
	if vars := b.bound[t]; len(vars) > 0 {
		v := vars[g.pick(len(vars))]
		if t == genNum {
			v = g.computed(b, v)
		}
		return v
	}
	v := g.fresh()
	if t == genNode {
		b.lits = append(b.lits, genLit{rel: "node", args: []string{v}})
	} else {
		n := "_"
		if nodes := b.bound[genNode]; len(nodes) > 0 {
			n = nodes[g.pick(len(nodes))]
		}
		b.lits = append(b.lits, genLit{rel: "weight", args: []string{n, v}})
	}
	b.bound[t] = append(b.bound[t], v)
	return v
}

func (g *Generator) compare(b *genBody) {
	t := genNode
	if len(b.bound[genNum]) > 0 && g.chance(0.5) {
		t = genNum
	}
	vars := b.bound[t]
	if len(vars) == 0 {
		return
	}
	l, r := vars[g.pick(len(vars))], g.constant(t)
	if len(vars) > 1 && g.chance(0.5) {
		r = vars[g.pick(len(vars))]
	}
	ops := []string{"=", "!=", "!=", "<"}
	if t == genNum {
		ops = []string{"=", "!=", "<", "<=", ">", ">="}
	}
	op := ops[g.pick(len(ops))]
	if t == genNum && g.arith != nil && !g.recursive && g.arith.Float64() < 0.4 {
		l = g.expression(vars, l)
		g.shapes["arithmetic"] = true
		if g.arith.Float64() < 0.5 {
			// ?y = expr computes into a fresh variable, which the rest of the body can read.
			g.computeInto(b, l)
			return
		}
		g.place(b, genLit{args: []string{l, r}, op: op})
		return
	}
	b.lits = append(b.lits, genLit{args: []string{l, r}, op: op})
}

// computeInto adds ?y = e for a fresh ?y, which the rest of the body can read.
func (g *Generator) computeInto(b *genBody, e string) {
	y := g.fresh()
	g.place(b, genLit{args: []string{y, e}, op: "="})
	b.bound[genNum] = append(b.bound[genNum], y)
	b.computed = append(b.computed, y)
}

// place adds a computing literal at the end, or now and then earlier, before what binds its
// operands: every evaluator holds it until they're bound (deferComparisons, #5), and the planner
// orders it by its modes.
func (g *Generator) place(b *genBody, l genLit) {
	i := len(b.lits)
	if g.arith.Float64() < 0.3 {
		i = g.arith.Intn(len(b.lits) + 1)
	}
	b.lits = slices.Insert(b.lits, i, l)
}

// bindConstant binds a fresh variable by ?c = constant now and then (#457), anywhere in the body, which
// the head or the goal may then read: a node's name, or on a unitless graph a number.
func (g *Generator) bindConstant(b *genBody) {
	if g.consts == nil || g.consts.Float64() >= 0.12 {
		return
	}
	g.nconsts++
	v := fmt.Sprintf("?c%d", g.nconsts)
	t, c := genNode, strconv.Quote(fmt.Sprintf("v%d", g.consts.Intn(g.nodes+1)))
	if g.arith != nil && g.consts.Float64() < 0.4 {
		t, c = genNum, strconv.Itoa(g.consts.Intn(10))
	}
	l := genLit{args: []string{v, c}, op: "="}
	if g.consts.Float64() < 0.5 {
		l.args[0], l.args[1] = c, v
	}
	b.lits = slices.Insert(b.lits, g.consts.Intn(len(b.lits)+1), l)
	b.bound[t] = append(b.bound[t], v)
	if b.consts == nil {
		b.consts = map[string]bool{}
	}
	b.consts[v] = true
	g.shapes["constant binding"] = true
}

// compute adds a computed number to a body that binds one, now and then, so a head or the goal can
// show what arithmetic answers rather than only what it filters.
func (g *Generator) compute(b *genBody) {
	if vars := b.bound[genNum]; g.arith != nil && !g.recursive && len(vars) > 0 && g.arith.Float64() < 0.15 {
		g.computeInto(b, g.expression(vars, vars[g.arith.Intn(len(vars))]))
		g.shapes["arithmetic"] = true
	}
}

// computed is a computed variable of b, now and then, to read in place of v.
func (g *Generator) computed(b *genBody, v string) string {
	if len(b.computed) > 0 && g.arith != nil && g.arith.Float64() < 0.6 {
		return b.computed[g.arith.Intn(len(b.computed))]
	}
	return v
}

// expression writes arithmetic over v and the number variables vars (#209): a sum, a difference, a
// product, a negation or a function call, nested now and then. No division: Soufflé's numbers are
// integers.
func (g *Generator) expression(vars []string, v string) string {
	operand := func() string {
		if g.arith.Float64() < 0.5 {
			return vars[g.arith.Intn(len(vars))]
		}
		return strconv.Itoa(g.arith.Intn(5))
	}
	var e string
	switch g.arith.Intn(6) {
	case 0:
		e = v + " + " + operand()
	case 1:
		e = v + " - " + operand()
	case 2:
		e = v + " * " + operand()
	case 3:
		e = "-" + v
	case 4:
		e = "num.add(" + v + ", " + operand() + ")"
	default:
		e = "(" + v + " + " + operand() + ") * " + operand()
	}
	return e
}

// readable is what a body in layer may read: the graph and lower layers always, its own layer only
// positively and only when recursive.
func (g *Generator) readable(layer int, positive, recursive bool) []genRel {
	out := slices.Clone(genGraphRels)
	for _, r := range g.rels {
		if r.layer < layer || (positive && recursive && r.layer == layer && !r.agg) {
			out = append(out, r)
		}
	}
	return out
}

func (g *Generator) body(layer int, recursive bool) *genBody {
	b := newBody()
	n := 1 + g.pick(3)
	same := -1
	if recursive {
		same = g.pick(n)
	}
	for i := 0; i < n; i++ {
		from := g.readable(layer, true, false)
		if i == same {
			from = nil
			for _, r := range g.rels {
				if r.layer == layer {
					from = append(from, r)
				}
			}
		}
		r := from[g.pick(len(from))]
		g.atom(b, r, false)
		if i != same {
			g.closure(b, r)
		}
	}
	if g.chance(0.08) {
		g.bodyAggregate(b, layer)
	}
	if len(b.all()) > 0 && g.chance(0.25) {
		from := g.readable(layer, false, false)
		g.atom(b, from[g.pick(len(from))], true)
	}
	if g.chance(0.3) {
		g.compare(b)
	}
	g.compareFacts(b)
	g.compute(b)
	g.bindConstant(b)
	g.optional(b)
	return b
}

// optional now and then makes an atom over a graph relation, one not the body's first positive
// literal, optional (#282): its row holds whether the atom matches, what only it binds absent when it
// doesn't. A derived relation stays outside, since an optional over one the clause reaches itself
// through doesn't stratify.
func (g *Generator) optional(b *genBody) {
	if g.optionals == nil || g.optionals.Float64() >= 0.15 {
		return
	}
	var at []int
	first := true
	for i, l := range b.lits {
		if l.rel == "" || l.neg || l.agg != nil || l.opt != nil {
			continue
		}
		if first {
			first = false
			continue
		}
		if l.fact == "" && slices.ContainsFunc(genGraphRels, func(r genRel) bool { return r.name == l.rel }) {
			at = append(at, i)
		}
	}
	if len(at) == 0 {
		return
	}
	i := at[g.optionals.Intn(len(at))]
	b.lits[i] = genLit{opt: []genLit{b.lits[i]}}
	g.shapes["optional"] = true
}

// closure now and then makes the literal of two nodes just drawn a walk of its relation (#281), min 1
// to 3 steps and max none or a few more, binding the hops it took to a number half the time. A body
// never walks its own relation, which stratification refuses.
func (g *Generator) closure(b *genBody, r genRel) {
	l := &b.lits[len(b.lits)-1]
	if g.closures == nil || len(r.cols) != 2 || r.cols[0] != genNode || r.cols[1] != genNode || l.fact != "" {
		return
	}
	if g.closures.Float64() >= 0.12 {
		g.paths(b, r)
		return
	}
	lo, hi := 1+g.closures.Intn(3), 0
	if g.closures.Float64() < 0.6 {
		hi = lo + g.closures.Intn(3)
	}
	hops := "_"
	if g.closures.Float64() < 0.5 {
		g.nclosures++
		hops = fmt.Sprintf("?h%d", g.nclosures)
		b.bound[genNum] = append(b.bound[genNum], hops)
	}
	l.rel, l.args = ClosureName, []string{strconv.Quote(g.step(r)), l.args[0], l.args[1], strconv.Itoa(lo), strconv.Itoa(hi), hops}
	g.shapes["closure"] = true
}

// step is the relation a walk over r steps through: r, or now and then a rule renaming it, which the
// planned evaluator walks through rather than deriving (walkSteps), so a walk from no bound end reads
// its starts through the rule too (#438).
func (g *Generator) step(r genRel) string {
	if g.steps == nil || g.steps.Float64() >= 0.4 {
		return r.name
	}
	name := fmt.Sprintf("st%d", len(g.stepRules)+1)
	g.stepRules = append(g.stepRules, genRule{head: name, args: []string{"?sa", "?sb"},
		body: []genLit{{rel: r.name, args: []string{"?sa", "?sb"}}}})
	g.shapes["derived step"] = true
	return name
}

// paths now and then makes the literal of two nodes just drawn a paths call over its relation (#280):
// any mode, min 0 to 2 steps and max none or a few more (a walk always has one), binding the path more
// often than not, and then half the time reading its length.
func (g *Generator) paths(b *genBody, r genRel) {
	if g.walks == nil || g.walks.Float64() >= 0.1 {
		return
	}
	l := &b.lits[len(b.lits)-1]
	mode := PathModes[g.walks.Intn(len(PathModes))]
	lo, hi := g.walks.Intn(3), 0
	if mode == "walk" || g.walks.Float64() < 0.7 {
		hi = max(lo, 1) + g.walks.Intn(3)
	}
	path := "_"
	var length *genLit
	if g.walks.Float64() < 0.6 {
		g.npaths++
		path = fmt.Sprintf("?p%d", g.npaths)
		b.bound[genNode] = append(b.bound[genNode], path)
		if g.walks.Float64() < 0.5 {
			n := fmt.Sprintf("?pl%d", g.npaths)
			length = &genLit{rel: "path.length", args: []string{path, n}}
			b.bound[genNum] = append(b.bound[genNum], n)
		}
	}
	l.rel, l.args = PathsName, []string{strconv.Quote(g.step(r)), l.args[0], l.args[1], strconv.Quote(mode), strconv.Itoa(lo), strconv.Itoa(hi), path}
	if length != nil {
		b.lits = append(b.lits, *length)
	}
	g.shapes["paths"] = true
}

// listRead now and then collects each node's edges into a list with a rule, and has the goal require
// a node it bound to be in some node's list, reading its length half the time (#283): a list built by
// one relation and read back through list.* in another.
func (g *Generator) listRead(b *genBody) {
	nodes := b.bound[genNode]
	if g.lists == nil || len(nodes) == 0 || g.lists.Float64() >= 0.12 {
		return
	}
	k := len(g.listRules) + 1
	rel, l := fmt.Sprintf("ls%d", k), fmt.Sprintf("?l%d", k)
	g.listRules = append(g.listRules, genRule{head: rel, args: []string{"?la", "list(?lb)"},
		body: []genLit{{rel: "edge", args: []string{"?la", "?lb"}}}})
	b.lits = append(b.lits, genLit{rel: rel, args: []string{"_", l}},
		genLit{rel: "list.member", args: []string{l, "_", nodes[g.lists.Intn(len(nodes))]}})
	if g.lists.Float64() < 0.5 {
		n := fmt.Sprintf("?ln%d", k)
		b.lits = append(b.lits, genLit{rel: "list.length", args: []string{l, n}})
		b.bound[genNum] = append(b.bound[genNum], n)
	}
	g.shapes["list read"] = true
}

// compareFacts now and then requires two of the facts b binds to differ, or to be one, as a Cypher
// match keeps its relationships apart (#279).
func (g *Generator) compareFacts(b *genBody) {
	if len(b.facts) < 2 || g.facts.Float64() >= 0.6 {
		return
	}
	i := g.facts.Intn(len(b.facts))
	j := (i + 1 + g.facts.Intn(len(b.facts)-1)) % len(b.facts)
	op := "!="
	if g.facts.Float64() < 0.3 {
		op = "="
	}
	b.lits = append(b.lits, genLit{args: []string{b.facts[i], b.facts[j]}, op: op})
}

// bodyAggregate adds `?v = fn : { ... }` (#71) over relations below layer, sharing up to two of the
// variables b has bound, and binds ?v as a number. Each shared variable is bound by a relation inside
// the braces, and none is another aggregate's value, since the braces' domain is what the rest of the
// body binds without one.
func (g *Generator) bodyAggregate(b *genBody, layer int) {
	inner := newBody()
	var shared []string
	for _, t := range genTypes {
		for _, v := range b.bound[t] {
			if !b.results[v] && len(shared) < 2 && g.chance(0.5) {
				inner.bound[t] = append(inner.bound[t], v)
				shared = append(shared, v)
			}
		}
	}
	from := g.readable(layer, false, false)
	for i := 1 + g.pick(2); i > 0; i-- {
		g.atom(inner, from[g.pick(len(from))], false)
	}
	fn := g.aggregate(inner, g.chance(0.1))
	for _, t := range genTypes {
		for _, v := range inner.bound[t] {
			if !slices.Contains(shared, v) || slices.ContainsFunc(inner.lits, func(l genLit) bool { return slices.Contains(l.args, v) }) {
				continue
			}
			if t == genNode {
				inner.lits = append(inner.lits, genLit{rel: "node", args: []string{v}})
			} else {
				inner.lits = append(inner.lits, genLit{rel: "weight", args: []string{"_", v}})
			}
		}
	}
	if g.chance(0.3) {
		g.compare(inner)
	}
	v := g.fresh()
	b.lits = append(b.lits, genLit{agg: &genAgg{result: v, fn: fn, body: inner.lits}})
	b.bound[genNum] = append(b.bound[genNum], v)
	if b.results == nil {
		b.results = map[string]bool{}
	}
	b.results[v] = true
	g.shapes["body aggregate"] = true
}

func (g *Generator) rule(r genRel, recursive bool) genRule {
	g.recursive = recursive
	b := g.body(r.layer, recursive)
	g.recursive = false
	args := make([]string, len(r.cols))
	for i, t := range r.cols {
		if g.chance(0.05) {
			args[i] = g.constant(t)
		} else {
			args[i] = g.binding(b, t)
		}
	}
	return genRule{head: r.name, args: args, body: b.lits}
}

// aggRule is an aggregating relation's one rule: node group columns, then number columns reduced.
func (g *Generator) aggRule(r genRel) genRule {
	b := g.body(r.layer, false)
	args := make([]string, len(r.cols))
	for i, t := range r.cols {
		if t == genNode {
			args[i] = g.binding(b, genNode)
			continue
		}
		args[i] = g.aggregate(b, false)
	}
	g.shapes["head aggregate"] = true
	return genRule{head: r.name, args: args, body: b.lits}
}

func (g *Generator) aggregate(b *genBody, list bool) string {
	fns := []string{"count", "count distinct", "sum", "min", "max"}
	if list {
		fns = append(fns, "list", "list distinct")
	}
	fn := fns[g.pick(len(fns))]
	if fn == "sum" && g.avgs.Float64() < 0.4 {
		fn = "avg"
		g.shapes["avg"] = true
	}
	var v string
	switch fn {
	case "sum", "avg", "min", "max":
		v = g.binding(b, genNum)
	default:
		all := b.all()
		if len(all) == 0 {
			v = g.binding(b, genNode)
		} else {
			v = all[g.pick(len(all))]
		}
	}
	if b.consts[v] {
		// a constant-bound variable has no relation for an aggregate to reduce; bind one that does
		t := genNode
		switch fn {
		case "sum", "avg", "min", "max":
			t = genNum
		}
		v = g.fresh()
		if t == genNode {
			b.lits = append(b.lits, genLit{rel: "node", args: []string{v}})
		} else {
			b.lits = append(b.lits, genLit{rel: "weight", args: []string{"_", v}})
		}
		b.bound[t] = append(b.bound[t], v)
	}
	if f, ok := strings.CutSuffix(fn, " distinct"); ok {
		return f + "(distinct " + v + ")"
	}
	return fn + "(" + v + ")"
}

// Program draws the program: its rules, layer by layer, then the goal.
func (g *Generator) Program() Program {
	g.shapes = map[string]bool{}
	g.stepRules = nil
	layer, prevAgg, n := 0, false, 1+g.pick(4)
	for i := 0; i < n; i++ {
		agg := g.chance(0.2)
		if i > 0 && (agg || prevAgg || !g.chance(0.3)) {
			layer++
		}
		r := genRel{name: fmt.Sprintf("r%d", i), layer: layer, agg: agg}
		if agg {
			for j := g.pick(3); j > 0; j-- {
				r.cols = append(r.cols, genNode)
			}
			for j := 1 + g.pick(2); j > 0; j-- {
				r.cols = append(r.cols, genNum)
			}
		} else {
			for j := 1 + g.pick(3); j > 0; j-- {
				t := genNode
				if g.chance(0.2) {
					t = genNum
				}
				r.cols = append(r.cols, t)
			}
		}
		g.rels = append(g.rels, r)
		prevAgg = agg
	}
	var p Program
	for _, r := range g.rels {
		if r.agg {
			p.rules = append(p.rules, g.aggRule(r))
			continue
		}
		for j := 1 + g.pick(3); j > 0; j-- {
			recursive := (j > 1 || g.chance(0.05)) && g.chance(0.6)
			if recursive {
				g.shapes["recursion"] = true
			}
			p.rules = append(p.rules, g.rule(r, recursive))
		}
	}
	g.listRules = nil
	p.goal = g.goal()
	p.rules = append(p.rules, g.listRules...)
	p.rules = append(p.rules, g.stepRules...)
	for s := range g.shapes {
		p.shapes = append(p.shapes, s)
	}
	slices.Sort(p.shapes)
	return p
}

func (g *Generator) goal() genGoal {
	b := newBody()
	for i := 1 + g.pick(2); i > 0; i-- {
		r := g.rels[g.pick(len(g.rels))]
		if g.chance(0.2) {
			r = genGraphRels[g.pick(len(genGraphRels))]
		}
		g.atom(b, r, false)
		if g.chance(0.3) {
			b.bind(len(b.lits)-1, 0, g.constant(r.cols[0]))
			g.shapes["bound goal"] = true
		}
	}
	if g.chance(0.1) {
		g.bodyAggregate(b, len(g.rels)+1)
	}
	all := b.all()
	if len(all) > 0 && g.chance(0.2) {
		from := g.readable(len(g.rels)+1, false, false)
		g.atom(b, from[g.pick(len(from))], true)
	}
	if g.chance(0.2) {
		g.compare(b)
	}
	g.compareFacts(b)
	g.compute(b)
	g.bindConstant(b)
	g.listRead(b)
	goal := genGoal{}
	if g.chance(0.35) {
		g.shapes["goal aggregate"] = true
		if nodes := b.bound[genNode]; len(nodes) > 0 && g.chance(0.6) {
			goal.sel = append(goal.sel, nodes[g.pick(len(nodes))])
		}
		for i := 1 + g.pick(2); i > 0; i-- {
			goal.sel = append(goal.sel, g.aggregate(b, true))
		}
		if g.chance(0.2) {
			goal.having = goal.sel[len(goal.sel)-1] + " >= " + strconv.Itoa(1+g.pick(2))
		}
	} else {
		all := b.all()
		if len(all) == 0 {
			all = []string{g.binding(b, genNode)}
		}
		for _, v := range all {
			if len(goal.sel) == 0 || (g.chance(0.5) && !slices.Contains(goal.sel, v)) {
				goal.sel = append(goal.sel, v)
			}
		}
		if y := g.computed(b, ""); y != "" && !slices.Contains(goal.sel, y) {
			goal.sel = append(goal.sel, y)
		}
		if len(b.facts) > 0 && g.facts.Float64() < 0.7 {
			goal.sel = append(goal.sel, b.facts[g.facts.Intn(len(b.facts))])
		}
	}
	goal.body = b.lits
	if g.chance(0.15) {
		goal.order = goal.sel[g.pick(len(goal.sel))] + " desc"
	}
	if g.chance(0.1) {
		goal.limit = 1 + g.pick(3)
	}
	if nodes := b.bound[genNode]; len(nodes) > 0 && g.chance(0.1) {
		// Mostly one value, which is a constant in the goal; otherwise a set of none, two or three (#132).
		n := 1
		if g.chance(0.4) {
			n = []int{0, 2, 3}[g.pick(3)]
			g.shapes["set-bound goal"] = true
		}
		vals := []string{}
		for range n {
			vals = append(vals, fmt.Sprintf("v%d", g.pick(g.nodes)))
		}
		goal.bind = map[string][]string{strings.TrimPrefix(nodes[g.pick(len(nodes))], "?"): vals}
		g.shapes["bound goal"] = true
	}
	return goal
}
