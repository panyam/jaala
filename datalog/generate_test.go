package datalog

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"iter"
	"math/rand"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

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

type genProgram struct {
	rules  []genRule
	goal   genGoal
	shapes []string
}

func (p genProgram) String() string {
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

// genFacts is randomGraph's shape kept as data, so shrinking can drop an edge or a node.
type genFacts struct {
	n       int
	edges   [][2]int
	weights []int
	units   []string // each weight's unit, "" for none; nil when no weight has one (#192)
}

func genGraph(rnd *rand.Rand) genFacts {
	f := genFacts{n: 3 + rnd.Intn(6)}
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
func genUnits(f genFacts) []string {
	h := fnv.New64a()
	fmt.Fprint(h, f.n, f.edges, f.weights)
	rnd := rand.New(rand.NewSource(int64(h.Sum64())))
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

// folded is the unit u reads as once the Base folds it, V for each of voltSpellings.
func folded(u string) string {
	if slices.Contains(voltSpellings, u) {
		return "V"
	}
	return u
}

// mixedUnits reports whether the weights carry more than one unit, or a unit beside none, which
// Soufflé can't express: its numbers have no units. A unit counts as it reads once folded, so mV and
// V are one.
func (f genFacts) mixedUnits() bool {
	for _, u := range f.units {
		if folded(u) != folded(f.units[0]) {
			return true
		}
	}
	return false
}

// prefixed reports whether a weight is served in a unit the Base folds (#205).
func (f genFacts) prefixed() bool {
	for _, u := range f.units {
		if folded(u) != u {
			return true
		}
	}
	return false
}

func (f genFacts) source() *ns.MemSource {
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

func (f genFacts) String() string {
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

type generator struct {
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
}

// newGenerator draws a program for facts from rnd. Arithmetic depends on the graph's units as drawn,
// before corpusCase drops mixed ones, so the corpus and the Soufflé step generate one program.
func newGenerator(rnd *rand.Rand, facts genFacts) *generator {
	g := &generator{rnd: rnd, nodes: facts.n}
	h := fnv.New64a()
	fmt.Fprint(h, "facts", facts.n, facts.edges, facts.weights)
	g.facts = rand.New(rand.NewSource(int64(h.Sum64())))
	h = fnv.New64a()
	fmt.Fprint(h, "avgs", facts.n, facts.edges, facts.weights)
	g.avgs = rand.New(rand.NewSource(int64(h.Sum64())))
	h = fnv.New64a()
	fmt.Fprint(h, "closures", facts.n, facts.edges, facts.weights)
	g.closures = rand.New(rand.NewSource(int64(h.Sum64())))
	h = fnv.New64a()
	fmt.Fprint(h, "walks", facts.n, facts.edges, facts.weights)
	g.walks = rand.New(rand.NewSource(int64(h.Sum64())))
	h = fnv.New64a()
	fmt.Fprint(h, "optionals", facts.n, facts.edges, facts.weights)
	g.optionals = rand.New(rand.NewSource(int64(h.Sum64())))
	h = fnv.New64a()
	fmt.Fprint(h, "lists", facts.n, facts.edges, facts.weights)
	g.lists = rand.New(rand.NewSource(int64(h.Sum64())))
	if facts.units == nil {
		h := fnv.New64a()
		fmt.Fprint(h, "arith", facts.n, facts.edges, facts.weights)
		g.arith = rand.New(rand.NewSource(int64(h.Sum64())))
	}
	return g
}

func (g *generator) chance(p float64) bool { return g.rnd.Float64() < p }
func (g *generator) pick(n int) int        { return g.rnd.Intn(n) }

// constant names a node one past the graph now and then, so a constant can match nothing.
func (g *generator) constant(t genType) string {
	if t == genNum {
		return strconv.Itoa(g.pick(10))
	}
	return fmt.Sprintf(`"v%d"`, g.pick(g.nodes+1))
}

func (g *generator) fresh() string {
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

func (g *generator) atom(b *genBody, r genRel, neg bool) {
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
func (g *generator) binding(b *genBody, t genType) string {
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

func (g *generator) compare(b *genBody) {
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
func (g *generator) computeInto(b *genBody, e string) {
	y := g.fresh()
	g.place(b, genLit{args: []string{y, e}, op: "="})
	b.bound[genNum] = append(b.bound[genNum], y)
	b.computed = append(b.computed, y)
}

// place adds a computing literal at the end, or now and then earlier, before what binds its
// operands: every evaluator holds it until they're bound (deferComparisons, #5), and the planner
// orders it by its modes.
func (g *generator) place(b *genBody, l genLit) {
	i := len(b.lits)
	if g.arith.Float64() < 0.3 {
		i = g.arith.Intn(len(b.lits) + 1)
	}
	b.lits = slices.Insert(b.lits, i, l)
}

// compute adds a computed number to a body that binds one, now and then, so a head or the goal can
// show what arithmetic answers rather than only what it filters.
func (g *generator) compute(b *genBody) {
	if vars := b.bound[genNum]; g.arith != nil && !g.recursive && len(vars) > 0 && g.arith.Float64() < 0.15 {
		g.computeInto(b, g.expression(vars, vars[g.arith.Intn(len(vars))]))
		g.shapes["arithmetic"] = true
	}
}

// computed is a computed variable of b, now and then, to read in place of v.
func (g *generator) computed(b *genBody, v string) string {
	if len(b.computed) > 0 && g.arith != nil && g.arith.Float64() < 0.6 {
		return b.computed[g.arith.Intn(len(b.computed))]
	}
	return v
}

// expression writes arithmetic over v and the number variables vars (#209): a sum, a difference, a
// product, a negation or a function call, nested now and then. No division: Soufflé's numbers are
// integers.
func (g *generator) expression(vars []string, v string) string {
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
func (g *generator) readable(layer int, positive, recursive bool) []genRel {
	out := slices.Clone(genGraphRels)
	for _, r := range g.rels {
		if r.layer < layer || (positive && recursive && r.layer == layer && !r.agg) {
			out = append(out, r)
		}
	}
	return out
}

func (g *generator) body(layer int, recursive bool) *genBody {
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
	g.optional(b)
	return b
}

// optional now and then makes an atom over a graph relation, one not the body's first positive
// literal, optional (#282): its row holds whether the atom matches, what only it binds absent when it
// doesn't. A derived relation stays outside, since an optional over one the clause reaches itself
// through doesn't stratify.
func (g *generator) optional(b *genBody) {
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
func (g *generator) closure(b *genBody, r genRel) {
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
	l.rel, l.args = closureName, []string{strconv.Quote(r.name), l.args[0], l.args[1], strconv.Itoa(lo), strconv.Itoa(hi), hops}
	g.shapes["closure"] = true
}

// paths now and then makes the literal of two nodes just drawn a paths call over its relation (#280):
// any mode, min 0 to 2 steps and max none or a few more (a walk always has one), binding the path more
// often than not, and then half the time reading its length.
func (g *generator) paths(b *genBody, r genRel) {
	if g.walks == nil || g.walks.Float64() >= 0.1 {
		return
	}
	l := &b.lits[len(b.lits)-1]
	mode := pathModes[g.walks.Intn(len(pathModes))]
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
	l.rel, l.args = pathsName, []string{strconv.Quote(r.name), l.args[0], l.args[1], strconv.Quote(mode), strconv.Itoa(lo), strconv.Itoa(hi), path}
	if length != nil {
		b.lits = append(b.lits, *length)
	}
	g.shapes["paths"] = true
}

// listRead now and then collects each node's edges into a list with a rule, and has the goal require
// a node it bound to be in some node's list, reading its length half the time (#283): a list built by
// one relation and read back through list.* in another.
func (g *generator) listRead(b *genBody) {
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
func (g *generator) compareFacts(b *genBody) {
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
func (g *generator) bodyAggregate(b *genBody, layer int) {
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

func (g *generator) rule(r genRel, recursive bool) genRule {
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
func (g *generator) aggRule(r genRel) genRule {
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

func (g *generator) aggregate(b *genBody, list bool) string {
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
	if f, ok := strings.CutSuffix(fn, " distinct"); ok {
		return f + "(distinct " + v + ")"
	}
	return fn + "(" + v + ")"
}

func (g *generator) program() genProgram {
	g.shapes = map[string]bool{}
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
	var p genProgram
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
	for s := range g.shapes {
		p.shapes = append(p.shapes, s)
	}
	slices.Sort(p.shapes)
	return p
}

func (g *generator) goal() genGoal {
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

// genCase is one program over one graph, evaluated plain or under Witnesses, which turns inlining
// and factoring off, so the two runs take different rewrites.
type genCase struct {
	prog      genProgram
	facts     genFacts
	witnessed bool
}

type genOutcome int

const (
	genAgreed  genOutcome = iota
	genSkipped            // Naive ran past genBudget
	genInvalid            // the generator wrote something the engine refuses
	genDisagreed
)

// genBudget bounds Naive's work, so an unlucky program over a dense graph is skipped rather than
// slowing the corpus down. The evaluators' work differs, so the comparison itself runs unbudgeted.
const genBudget = 200_000

func (c genCase) String() string {
	return fmt.Sprintf(" shrunk program: %s\n facts: %s\n bind: %v, witnessed: %v", c.prog, c.facts, c.prog.goal.bind, c.witnessed)
}

func (c genCase) opts() []Option {
	var opts []Option
	if len(c.prog.goal.bind) > 0 {
		bind := map[Var][]ns.Value{}
		for v, names := range c.prog.goal.bind {
			bind[Var(v)] = []ns.Value{}
			for _, n := range names {
				bind[Var(v)] = append(bind[Var(v)], ns.S(n))
			}
		}
		opts = append(opts, Bind(bind))
	}
	if c.witnessed {
		opts = append(opts, Witnesses())
	}
	return opts
}

func (c genCase) check() (genOutcome, string) {
	text := c.prog.String()
	q, err := Parse(text)
	if err != nil {
		return genInvalid, err.Error()
	}
	b := baseFor(folding(c.facts.source())) // prefixed weights read folded (#205, #303)
	if err := Validate(q, b.reg); err != nil {
		return genInvalid, err.Error()
	}
	var over *BudgetExceeded
	if _, err := (Naive{}).Eval(bg, q, b, append(c.opts(), Budget(genBudget))...); errors.As(err, &over) {
		return genSkipped, ""
	}
	want, diff, werr := agree(q, b, c.opts()...)
	if diff != "" {
		return genDisagreed, diff
	}
	countUnitAnswers(want)
	if c.facts.prefixed() {
		if diff := checkFolded(want); diff != "" {
			return genDisagreed, diff
		}
	}
	if diff := agreeLookingUp(q, b, want, werr, c.opts()); diff != "" {
		return genDisagreed, diff
	}
	if diff := agreeFederated(q, b, want, werr, c.opts()); diff != "" {
		return genDisagreed, diff
	}
	if diff := agreePushed(q, b, want, werr, c.opts()); diff != "" {
		return genDisagreed, diff
	}
	if diff := agreePartitioned(q, b, want, werr, c.opts()); diff != "" {
		return genDisagreed, diff
	}
	if diff := agreeStreamed(q, b, want, werr, c.opts()); diff != "" {
		return genDisagreed, diff
	}
	if diff := agreeOverlaid(q, b, want, werr, c.opts()); diff != "" {
		return genDisagreed, diff
	}
	if diff := agreeAt(q, b, c.opts(), len(text)); diff != "" {
		return genDisagreed, diff
	}
	if diff := agreeAcross(q, b, c.opts(), len(text)); diff != "" {
		return genDisagreed, diff
	}
	return genAgreed, ""
}

// agreeStreamed runs q over b's facts through a Source that streams every relation (#264), where the
// planned SemiNaive streams a relation only its goal reads: every evaluator must answer as Naive did
// reading them whole. A program whose goal streamed, on a fresh Base, counts for the control.
func agreeStreamed(q Query, b *Base, want []Row, werr error, opts []Option) string {
	if diff := agreeOver(q, b, MustBase(b.reg, streamingOver(b.src)), "streamed", want, werr, opts); diff != "" {
		return diff
	}
	src := streamingOver(b.src)
	fresh := MustBase(b.reg, src)
	if _, err := (SemiNaive{}).Eval(bg, q, fresh, opts...); err == nil {
		for _, rel := range src.Relations() {
			if src.count(rel) > 0 && !fresh.edb.holds(rel) {
				streamedPrograms++
				break
			}
		}
	}
	return ""
}

// alonePushedPrograms counts those whose planned evaluation pushed one literal alone (#263).
var alonePushedPrograms int

// partitionedBatchPrograms counts those whose partitioned push carried Bindings (#268).
var partitionedBatchPrograms int

// streamedPrograms counts those whose goal streamed a relation into its answer (#264).
var streamedPrograms int

// lookedUpPrograms counts the corpus programs whose evaluation made a lookup, for the corpus's control.
var lookedUpPrograms int

// federatedPrograms counts those whose evaluation made a lookup through a Federation's members.
var federatedPrograms int

// pushedPrograms counts those whose planned evaluation pushed a conjunction to the Source (#237).
var pushedPrograms int

// batchedPrograms counts those whose planned evaluation asked a batching Source for a batch (#153).
var batchedPrograms int

// fetchedWalkPrograms counts those whose planned evaluation over a batching Source fetched a closure's
// or a paths call's steps ahead of its walk (#396), and federatedWalkPrograms those through a
// Federation's batching member.
var fetchedWalkPrograms, federatedWalkPrograms int

// callsWalk reports whether q calls graph.closure or graph.paths.
func callsWalk(q Query) bool {
	called := func(b Body) bool {
		return slices.ContainsFunc(b.Literals, func(l Literal) bool { return l.Pos != nil && walkCalled(l.Pos.Relation) != nil })
	}
	return called(q.Goal) || slices.ContainsFunc(q.Rules, func(r Rule) bool { return called(r.Body) })
}

// walkFetched reports whether the planned SemiNaive over src fetched an engine walk's steps ahead of
// it: a batch or a walk read under a closure's or a paths call's literal, which only prefetchClosures
// makes there.
func walkFetched(q Query, reg *ns.Vocabulary, src ns.Source) bool {
	var r Report
	if _, err := (SemiNaive{}).Eval(bg, q, MustBase(reg, src), Explain(&r)); err != nil {
		return false
	}
	for _, body := range append([]*BodyReport{r.Goal}, r.Rules...) {
		if body == nil {
			continue
		}
		for _, l := range body.Literals {
			if !strings.HasPrefix(l.Literal, "graph.") {
				continue
			}
			if slices.ContainsFunc(l.Access, func(a string) bool { return strings.HasPrefix(a, "batched") || strings.HasPrefix(a, "walked") }) {
				return true
			}
		}
	}
	return false
}

// unitAnswers counts the corpus answers holding a number with a unit, and mixedAnswers those with an
// aggregate absent over mixed units, for the control that units reach the goals (#192).
var unitAnswers, mixedAnswers int

func countUnitAnswers(rows []Row) {
	unit, mixed := false, false
	for _, r := range rows {
		for _, v := range r.Bind {
			unit = unit || v.BaseUnit != ""
		}
		for _, why := range r.Why {
			mixed = mixed || strings.HasPrefix(why, "mixed units")
		}
	}
	if unit {
		unitAnswers++
	}
	if mixed {
		mixedAnswers++
	}
}

// foldedAnswers counts the answers of programs over weights served in mV, kV or A*ohm that hold a
// number in V, for the control that folded values reach the goals (#205).
var foldedAnswers int

// checkFolded fails an answer, over weights served in other spellings of volts, that holds one of
// those spellings rather than V: every evaluator reads values through one fold, so only this (and the
// Soufflé step, which sees the folded numbers) would notice it missing.
func checkFolded(rows []Row) string {
	inV := false
	for _, r := range rows {
		for v, val := range r.Bind {
			if folded(val.BaseUnit) != val.BaseUnit {
				return fmt.Sprintf("answer column %s holds %s %s, a spelling the Base folds to %s", v, val.S, val.BaseUnit, folded(val.BaseUnit))
			}
			inV = inV || val.BaseUnit == "V"
		}
	}
	if inV {
		foldedAnswers++
	}
	return ""
}

// agreeLookingUp runs q over b's facts through a Source answering lookups (#126), and the planned
// SemiNaive's in batches (#153): every evaluator must
// give the rows and error Naive gave reading them whole, and Naive the same citations and witnesses
// under CanonicalCites.
func agreeLookingUp(q Query, b *Base, want []Row, werr error, opts []Option) string {
	src := batchingAt(b.src)
	diff := agreeOver(q, b, MustBase(b.reg, src), "looking up", want, werr, opts)
	if len(src.lookups) > 0 {
		lookedUpPrograms++
	}
	if len(src.batches) > 0 {
		batchedPrograms++
	}
	if diff == "" && callsWalk(q) && walkFetched(q, b.reg, batchingAt(b.src)) {
		fetchedWalkPrograms++
	}
	return diff
}

// agreeFederated is agreeLookingUp over b's facts dealt across three stores that each answer lookups
// (#211), so a call's facts come back from several members' lookups at once, and that index only
// their first argument (#154), so the planner reorders bodies around their scans.
func agreeFederated(q Query, b *Base, want []Row, werr error, opts []Option) string {
	members := splitInto(b.src, 3, func(int) bool { return true })
	stores := make([]*looking, len(members))
	for i := range members {
		stores[i] = members[i].Source.(*looking)
		members[i].Source = firstIndexed{stores[i]}
	}
	members[0].Source = firstIndexedBatching{&batching{looking: stores[0]}}
	members[2].Source = streamingFirstIndexed{firstIndexed{stores[2]}}
	f, err := ns.NewFederation(members...)
	if err != nil {
		return err.Error()
	}
	diff := agreeOver(q, b, MustBase(b.reg, f), "federated", want, werr, opts)
	if diff == "" && callsWalk(q) && walkFetched(q, b.reg, f) {
		federatedWalkPrograms++
	}
	for _, st := range stores {
		if len(st.lookups) > 0 {
			federatedPrograms++
			break
		}
	}
	return diff
}

// agreePushed is agreeLookingUp over a store that also evaluates conjunctions (#237), and compares the
// planned SemiNaive over it, the only evaluator that pushes, with Naive reading whole under
// CanonicalCites too, so a pushed row's citations and witnesses are checked as well as its bindings.
// Every other program's store takes no comparisons, so a group is pushed both with and without them.
func agreePushed(q Query, b *Base, want []Row, werr error, opts []Option) string {
	src := pushingAt(b.src)
	pushedSeed++
	src.noCompare = pushedSeed%2 == 0
	lb := MustBase(b.reg, src)
	if diff := agreeOver(q, b, lb, "pushed", want, werr, opts); diff != "" {
		return diff
	}
	if slices.ContainsFunc(src.pushed, func(c ns.Conjunction) bool { return len(c.Atoms) > 1 }) {
		pushedPrograms++
	}
	if slices.ContainsFunc(src.pushed, func(c ns.Conjunction) bool { return len(c.Atoms) == 1 && c.Page == nil }) {
		alonePushedPrograms++
	}
	if slices.ContainsFunc(src.pushed, func(c ns.Conjunction) bool { return len(c.Bindings) > 0 }) {
		batchPushedPrograms++
	}
	if src.pages > 0 {
		pagedPrograms++
	}
	if src.summaries > 0 {
		summarizedPrograms++
	}
	if diff := agreeWalked(b, func() (ns.Source, func() int) {
		p := pushingAt(b.src)
		return p, func() int {
			if p.stepWalks > 0 {
				stepWalkedPrograms++
			}
			return p.walks() + p.stepWalks
		}
	}); diff != "" {
		return diff
	}
	if diff := agreeGoals(b, func() (ns.Source, func() (int, int, int)) {
		p := pushingAt(b.src)
		return p, func() (int, int, int) { return p.pages, p.summaries, p.alone() }
	}); diff != "" {
		return diff
	}
	if werr != nil || anyRows(q) {
		return ""
	}
	canon := append(opts[:len(opts):len(opts)], CanonicalCites(), Witnesses())
	w, _ := Naive{}.Eval(bg, q, b, canon...)
	g, err := SemiNaive{}.Eval(bg, q, MustBase(b.reg, pushingAt(b.src)), canon...)
	if err != nil || !reflect.DeepEqual(w, g) {
		return fmt.Sprintf("planned SemiNaive pushed cites differently under CanonicalCites on %v\n whole:  %v\n pushed: %v %v", q, w, g, err)
	}
	return ""
}

// batchPushedPrograms counts those whose planned evaluation pushed a group for a batch of bindings
// (#246).
var batchPushedPrograms int

// pagedPrograms and summarizedPrograms count the goals agreeGoals pushed that a store paged or
// summarized (#241, #240).
var pagedPrograms, summarizedPrograms int

// goalQuestions aggregate, page and filter one literal over a graph's own relations, the shapes a
// store pages, summarizes or pushes alone (#263) and the generated programs rarely take: their goals
// read derived relations or negate, and their literals join or carry constants.
// The weights carry units in some graphs (#192), mixed in some, so the unit rules are exercised.
var goalQuestions = []string{
	`edge(?a, ?b) => ?a, count(?b)`,
	`weight(?n, ?w) => sum(?w), min(?w), max(?w)`,
	`weight(?n, ?w) => count(distinct ?w), list(?w), list(distinct ?w), sum(distinct ?w)`,
	`edge(?a, ?b), weight(?b, ?w), ?w > 2 => ?a, sum(?w), count(?b)`,
	`edge(?a, ?b) => ?a, ?b order by ?b desc, ?a limit 3 offset 1`,
	`weight(?n, ?w) => ?n, ?w order by ?w limit 2`,
	`edge(?a, ?b), node(?b) => ?a, ?b order by ?a limit 2`,
	`weight(?n, ?w) => ?n, min(?w), max(?w)`,
	`edge(?a, ?b) => ?a, ?b order by ?a desc, ?b after "v3", "v1" limit 2`,
	`edge(?a, ?b), weight(?b, ?w) => ?a, max(?w)`,
	`weight(?n, ?w) => min(?w), max(distinct ?w)`,
	`edge(?a, ?a) => ?a`,
	`weight(?n, ?w), ?w > 2 => ?n, ?w`,
}

// agreeGoals runs goalQuestions over a Source that pages and summarizes, on a fresh Base each (Naive
// reading a relation whole would leave nothing to push), and compares each with Naive reading whole:
// its rows, then under CanonicalCites its citations and witnesses.
func agreeGoals(b *Base, pushing func() (ns.Source, func() (int, int, int))) string {
	for _, text := range goalQuestions {
		q, err := Parse(text)
		if err != nil {
			return err.Error()
		}
		want, werr := Naive{}.Eval(bg, q, b)
		src, counts := pushing()
		got, gerr := SemiNaive{}.Eval(bg, q, MustBase(b.reg, src))
		if errText(gerr) != errText(werr) || !reflect.DeepEqual(binds(got), binds(want)) {
			return fmt.Sprintf("planned SemiNaive over a store paging and summarizing answers differently on %v\n whole:  %v %v\n pushed: %v %v", q, binds(want), werr, binds(got), gerr)
		}
		pages, sums, alone := counts()
		if pages > 0 {
			pagedPrograms++
		}
		if alone > 0 {
			alonePushedPrograms++
		}
		if sums > 0 {
			summarizedPrograms++
		}
		if werr != nil {
			continue
		}
		canon := []Option{CanonicalCites(), Witnesses()}
		w, _ := Naive{}.Eval(bg, q, b, canon...)
		fresh, _ := pushing()
		g, err := SemiNaive{}.Eval(bg, q, MustBase(b.reg, fresh), canon...)
		if err != nil || !reflect.DeepEqual(w, g) {
			return fmt.Sprintf("planned SemiNaive paging and summarizing cites differently under CanonicalCites on %v\n naive:  %v\n pushed: %v %v", q, w, g, err)
		}
	}
	return ""
}

// stepWalkedPrograms counts the walks agreeWalked made of a step of two literals (#250).
var stepWalkedPrograms int

// walkedPrograms counts the graphs agreeWalked walked (#239), and walkedClosurePrograms the bounded
// closures and paths calls among them (#396).
var walkedPrograms, walkedClosurePrograms int

// agreeWalked runs a reach from the graph's first edge's source over a Source that walks (#239), the
// shape a walk takes and the generated programs rarely do, and compares it with Naive reading whole,
// its bindings and, under CanonicalCites, its witnesses. walking makes a fresh Source and a count of
// the walks its stores made.
func agreeWalked(b *Base, walking func() (ns.Source, func() int)) string {
	edges := b.src.Tuples("edge")
	if len(edges) == 0 || edges[0].Vals[0].Num != nil {
		return ""
	}
	from := strconv.Quote(edges[0].Vals[0].S)
	for _, text := range []string{
		`reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c); reach(` + from + `, ?x) => ?x`,
		// two hops a step, a walk of a conjunction (#250)
		`hop2(?a, ?c) :- edge(?a, ?m), edge(?m, ?c); hop2(?a, ?c) :- hop2(?a, ?b), edge(?b, ?m), edge(?m, ?c); hop2(` + from + `, ?x) => ?x`,
		// a closure and a paths call with a max, walked to it (#396)
		`graph.closure("edge", ` + from + `, ?x, 1, 2, ?h) => ?x, ?h`,
		`graph.paths("edge", ` + from + `, ?x, "trail", 1, 2, ?p) => ?p`,
	} {
		if diff := agreeWalkedOn(b, text, walking); diff != "" {
			return diff
		}
	}
	return ""
}

// agreeWalkedOn is agreeWalked for one program.
func agreeWalkedOn(b *Base, text string, walking func() (ns.Source, func() int)) string {
	q, err := Parse(text)
	if err != nil {
		return err.Error()
	}
	src, _ := walking()
	want, werr := Naive{}.Eval(bg, q, b)
	if diff := agreeOver(q, b, MustBase(b.reg, src), "walked", want, werr, nil); diff != "" {
		return diff
	}
	canon := []Option{CanonicalCites(), Witnesses()}
	w, _ := Naive{}.Eval(bg, q, b, canon...)
	fresh, walks := walking() // agreeOver's Naive filled src's Base with lookups, so nothing was left to walk
	g, err := SemiNaive{}.Eval(bg, q, MustBase(b.reg, fresh), canon...)
	if walks() > 0 {
		walkedPrograms++
		if callsWalk(q) {
			walkedClosurePrograms++
		}
	}
	if err != nil || !reflect.DeepEqual(w, g) {
		return fmt.Sprintf("planned SemiNaive walking cites differently under CanonicalCites on %v\n naive:  %v\n walked: %v %v", q, w, g, err)
	}
	return ""
}

// partitionedPrograms counts those whose planned evaluation pushed a conjunction to more than one
// member of a partitioned federation (#238).
var partitionedPrograms int

// agreePartitioned is agreePushed over b's facts dealt across three pushing stores by their first
// argument, in a partitioned federation whose Holds routes that argument (#238), so a pushed join
// spans members and its prefixes continue across them. Each value is dealt as it reads, so a bound
// value finds the member holding it however it's spelled.
func agreePartitioned(q Query, b *Base, want []Row, werr error, opts []Option) string {
	const n = 3
	deal := func(rel string, v ns.Value) int {
		s, _ := b.src.Schema(rel)
		read := normalizeTuples(stdUnits, []ns.Tuple{{Vals: []ns.Value{v}}}, s.Types[:min(1, len(s.Types))])[0].Vals[0]
		h := 0
		for _, r := range valueKey(read) {
			h = h*31 + int(r)
		}
		return (h%n + n) % n
	}
	mems := make([]*ns.MemSource, n)
	for i := range mems {
		mems[i] = ns.NewMemSource()
		for _, rel := range b.src.Relations() {
			s, _ := b.src.Schema(rel)
			mems[i].DeclareSchema(rel, s)
		}
	}
	for _, rel := range b.src.Relations() {
		for k, t := range b.src.Tuples(rel) {
			i := k % n
			if len(t.Vals) > 0 {
				i = deal(rel, t.Vals[0])
			}
			mems[i].Add(rel, t)
		}
	}
	federate := func() ([]*pushing, *ns.Federation, error) {
		stores := make([]*pushing, n)
		members := make([]ns.Member, n)
		for i := range members {
			i := i
			stores[i] = pushingAt(mems[i])
			members[i] = ns.Member{Source: stores[i], Holds: func(rel string, bound map[int]ns.Value) bool {
				v, ok := bound[0]
				return !ok || deal(rel, v) == i
			}}
		}
		f, err := ns.NewPartitionedFederation(members...)
		return stores, f, err
	}
	stores, f, err := federate()
	if err != nil {
		return err.Error()
	}
	walking := func() (ns.Source, func() int) {
		stores, f, _ := federate()
		return f, func() int {
			n := 0
			for _, st := range stores {
				n += st.walks()
			}
			return n
		}
	}
	if diff := agreeWalked(b, walking); diff != "" {
		return diff
	}
	if diff := agreeGoals(b, func() (ns.Source, func() (int, int, int)) {
		stores, f, _ := federate()
		return f, func() (int, int, int) {
			pages, sums, alone := 0, 0, 0
			for _, st := range stores {
				pages += st.pages
				sums += st.summaries
				alone += st.alone()
			}
			return pages, sums, alone
		}
	}); diff != "" {
		return diff
	}
	lb := MustBase(b.reg, f)
	if diff := agreeOver(q, b, lb, "partitioned", want, werr, opts); diff != "" {
		return diff
	}
	asked, bound := 0, false
	for _, st := range stores {
		if st.pushes() > 0 {
			asked++
		}
		st.mu.Lock()
		bound = bound || slices.ContainsFunc(st.pushed, func(c ns.Conjunction) bool { return c.Partial && len(c.Bindings) > 0 })
		st.mu.Unlock()
	}
	if asked > 1 {
		partitionedPrograms++
	}
	if bound {
		partitionedBatchPrograms++
	}
	if werr != nil || anyRows(q) {
		return ""
	}
	canon := append(opts[:len(opts):len(opts)], CanonicalCites(), Witnesses())
	w, _ := Naive{}.Eval(bg, q, MustBase(b.reg, f), canon...)
	g, err := SemiNaive{}.Eval(bg, q, MustBase(b.reg, f), canon...)
	if err != nil || !reflect.DeepEqual(w, g) {
		return fmt.Sprintf("planned SemiNaive over a partitioned federation cites differently under CanonicalCites on %v\n naive:  %v\n pushed: %v %v", q, w, g, err)
	}
	return ""
}

// pushedSeed alternates agreePushed's store between taking comparisons and refusing them.
var pushedSeed int

// firstIndexed is a looking store whose index covers a relation's first argument only.
type firstIndexed struct{ *looking }

func (firstIndexed) Indexed(_ string, bound []int) bool { return slices.Contains(bound, 0) }

// streamingFirstIndexed is a firstIndexed store that streams a relation read whole (#233).
type streamingFirstIndexed struct{ firstIndexed }

func (s streamingFirstIndexed) TuplesSeq(_ context.Context, rel string) iter.Seq2[ns.Tuple, error] {
	return func(yield func(ns.Tuple, error) bool) {
		for _, t := range s.Tuples(rel) {
			if !yield(t, nil) {
				return
			}
		}
	}
}

// firstIndexedBatching is a firstIndexed store that also answers lookups in batches.
type firstIndexedBatching struct{ *batching }

func (firstIndexedBatching) Indexed(_ string, bound []int) bool { return slices.Contains(bound, 0) }

// agreeOver requires every evaluator over lb to answer q as Naive did over b, and Naive over lb to
// cite as over b under CanonicalCites. A limit without an order by may answer other rows over lb,
// whose tuples arrive in another order (#194), so its whole answer is compared instead, and its
// limited one checked as agreeOnSomeRows does.
func agreeOver(q Query, b, lb *Base, how string, want []Row, werr error, opts []Option) string {
	if anyRows(q) {
		whole := q
		whole.Limit = 0
		w, werr := Naive{}.Eval(bg, whole, b, opts...)
		if diff := agreeOver(whole, b, lb, how, w, werr, opts); diff != "" {
			return diff
		}
		_, diff, _ := agreeOnSomeRows(q, lb, opts...)
		return diff
	}
	for _, ev := range []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}, SemiNaive{}} {
		got, err := ev.Eval(bg, q, lb, opts...)
		if errText(err) != errText(werr) || !reflect.DeepEqual(binds(got), binds(want)) {
			return fmt.Sprintf("%T %s answers differently from Naive reading whole on %v\n whole:     %v %v\n %s: %v %v", ev, how, q, binds(want), werr, how, binds(got), err)
		}
	}
	if werr != nil {
		return ""
	}
	canon := append(opts[:len(opts):len(opts)], CanonicalCites())
	w, _ := Naive{}.Eval(bg, q, b, canon...)
	g, _ := Naive{}.Eval(bg, q, lb, canon...)
	if !reflect.DeepEqual(w, g) {
		return fmt.Sprintf("Naive %s cites differently under CanonicalCites on %v\n whole:     %v\n %s: %v", how, q, w, how, g)
	}
	return ""
}

// shrink drops rules, literals, goal clauses, edges and nodes one at a time while the case still
// disagrees, and returns the smallest case it reached with its disagreement.
func shrink(c genCase, diff string) (genCase, string) {
	return shrinkWhile(c, diff, func(s genCase) (bool, string) {
		out, d := s.check()
		return out == genDisagreed, d
	})
}

func shrinkWhile(c genCase, diff string, fails func(genCase) (bool, string)) (genCase, string) {
	for {
		smaller := false
		for _, s := range c.smaller() {
			if ok, d := fails(s); ok {
				c, diff, smaller = s, d, true
				break
			}
		}
		if !smaller {
			return c, diff
		}
	}
}

func (c genCase) smaller() []genCase {
	var out []genCase
	with := func(edit func(p *genProgram, f *genFacts)) {
		s := c
		s.prog = c.prog.clone()
		s.facts = genFacts{n: c.facts.n, edges: slices.Clone(c.facts.edges), weights: slices.Clone(c.facts.weights), units: slices.Clone(c.facts.units)}
		edit(&s.prog, &s.facts)
		out = append(out, s)
	}
	for i := range c.prog.rules {
		with(func(p *genProgram, _ *genFacts) { p.rules = slices.Delete(p.rules, i, i+1) })
	}
	// A derived relation read in a body can become a graph relation of its arity, so a relation only
	// read there can then be dropped. Only derived to graph, so every step makes the case smaller.
	for i, r := range c.prog.rules {
		for j, l := range r.body {
			for _, base := range genGraphRels {
				if l.rel != "" && !slices.ContainsFunc(genGraphRels, func(g genRel) bool { return g.name == l.rel }) && len(base.cols) == len(l.args) {
					with(func(p *genProgram, _ *genFacts) { p.rules[i].body[j].rel = base.name })
				}
			}
		}
	}
	heads := map[string]bool{}
	for _, r := range c.prog.rules {
		if !heads[r.head] {
			heads[r.head] = true
			with(func(p *genProgram, _ *genFacts) { p.drop(r.head) })
		}
	}
	for i, r := range c.prog.rules {
		for j := range r.body {
			if len(r.body) > 1 {
				with(func(p *genProgram, _ *genFacts) { p.rules[i].body = slices.Delete(p.rules[i].body, j, j+1) })
			}
		}
	}
	g := c.prog.goal
	for j := range g.body {
		if len(g.body) > 1 {
			with(func(p *genProgram, _ *genFacts) { p.goal.body = slices.Delete(p.goal.body, j, j+1) })
		}
	}
	for j := range g.sel {
		if len(g.sel) > 1 {
			with(func(p *genProgram, _ *genFacts) { p.goal.sel = slices.Delete(p.goal.sel, j, j+1) })
		}
	}
	if g.having != "" {
		with(func(p *genProgram, _ *genFacts) { p.goal.having = "" })
	}
	if g.order != "" {
		with(func(p *genProgram, _ *genFacts) { p.goal.order = "" })
	}
	if g.limit > 0 {
		with(func(p *genProgram, _ *genFacts) { p.goal.limit = 0 })
	}
	if len(g.bind) > 0 {
		with(func(p *genProgram, _ *genFacts) { p.goal.bind = nil })
	}
	if c.witnessed {
		with(func(*genProgram, *genFacts) {})
		out[len(out)-1].witnessed = false
	}
	for i := range c.facts.edges {
		with(func(_ *genProgram, f *genFacts) { f.edges = slices.Delete(f.edges, i, i+1) })
	}
	if c.facts.n > 1 {
		with(func(_ *genProgram, f *genFacts) {
			f.n--
			f.weights = f.weights[:f.n]
			if f.units != nil {
				f.units = f.units[:f.n]
			}
			f.edges = slices.DeleteFunc(f.edges, func(e [2]int) bool { return e[0] == f.n || e[1] == f.n })
		})
	}
	return out
}

// drop removes relation rel: its rules and every literal that reads it. A goal left with nothing
// asks for the nodes instead.
func (p *genProgram) drop(rel string) {
	reads := func(l genLit) bool {
		return l.rel == rel || l.agg != nil && slices.ContainsFunc(l.agg.body, func(in genLit) bool { return in.rel == rel })
	}
	p.rules = slices.DeleteFunc(p.rules, func(r genRule) bool { return r.head == rel })
	for i := range p.rules {
		p.rules[i].body = slices.DeleteFunc(p.rules[i].body, reads)
	}
	p.goal.body = slices.DeleteFunc(p.goal.body, reads)
	if len(p.goal.body) == 0 {
		p.goal = genGoal{body: []genLit{{rel: "node", args: []string{"?g"}}}, sel: []string{"?g"}}
	}
}

func (p genProgram) clone() genProgram {
	c := p
	c.rules = make([]genRule, len(p.rules))
	for i, r := range p.rules {
		c.rules[i] = genRule{head: r.head, args: slices.Clone(r.args), body: slices.Clone(r.body)}
	}
	c.goal.body = slices.Clone(p.goal.body)
	c.goal.sel = slices.Clone(p.goal.sel)
	return c
}

// knownDisagreements are disagreements filed and not yet fixed, recognised by their message, so the
// corpus counts them rather than failing on them. A program is classified by its first disagreement,
// before shrinking, so one known disagreement can hide another. Fixing one means deleting its line
// here.
var knownDisagreements = []struct {
	issue int
	match *regexp.Regexp
	repro genCase // a case that disagrees this way, so a wrong pattern or a fixed bug shows
}{}

func knownDisagreement(diff string) int {
	for _, k := range knownDisagreements {
		if k.match.MatchString(diff) {
			return k.issue
		}
	}
	return 0
}

func envInt(name string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return def
}

// Every evaluator must answer each generated program as Naive does, with the rules both enforces.
// The control is the shape count: each shape the generator aims at has to have been compared, both
// plain and under Witnesses, or the corpus is passing on programs that never reach the rewrites.
//
// It skips under -race unless JAALA_GEN_UNDER_RACE is set (#143): the corpus evaluates one program at
// a time and the engine starts no goroutines, so the detector can find nothing there and made it
// nine times slower. CI runs it in a step of its own without -race; the concurrent tests stay.
func TestGeneratedProgramsAgree(t *testing.T) {
	first, seeds := int64(1), envInt("JAALA_GEN_SEEDS", 300)
	if s := envInt("JAALA_GEN_SEED", 0); s > 0 {
		first, seeds = int64(s), 1
	}
	if raceDetector && seeds > 1 && os.Getenv("JAALA_GEN_UNDER_RACE") == "" {
		t.Skip("the corpus runs without -race (CI's corpus step); JAALA_GEN_UNDER_RACE=1 runs it here")
	}
	compared, known, skipped, failed := map[string]int{}, map[int]int{}, 0, 0
	lookedUpPrograms, federatedPrograms, batchedPrograms, pushedPrograms, partitionedPrograms, walkedPrograms, unitAnswers, mixedAnswers, foldedAnswers = 0, 0, 0, 0, 0, 0, 0, 0, 0
	pagedPrograms, summarizedPrograms, batchPushedPrograms, stepWalkedPrograms, streamedPrograms, partitionedBatchPrograms, alonePushedPrograms = 0, 0, 0, 0, 0, 0, 0
	atPrograms, atStoredPrograms, acrossPrograms, acrossAggregates, acrossLookedPrograms = 0, 0, 0, 0, 0
	overlaidPushed, overlaidBatched, overlaidLookedUp = 0, 0, 0
	fetchedWalkPrograms, federatedWalkPrograms, walkedClosurePrograms = 0, 0, 0
	withUnits := 0
	for seed := first; seed < first+int64(seeds); seed++ {
		rnd := rand.New(rand.NewSource(seed))
		facts := genGraph(rnd)
		prog := newGenerator(rnd, facts).program()
		if facts.units != nil {
			withUnits++
		}
		for _, witnessed := range []bool{false, true} {
			c := genCase{prog: prog, facts: facts, witnessed: witnessed}
			switch out, diff := c.check(); out {
			case genSkipped:
				skipped++
			case genInvalid:
				t.Errorf("seed %d: the generator wrote a program the engine refuses: %v\n %s", seed, diff, prog)
			case genDisagreed:
				if issue := knownDisagreement(diff); issue != 0 {
					known[issue]++
					continue
				}
				failed++
				small, d := shrink(c, diff)
				t.Errorf("seed %d (witnessed %v) disagrees; replay with JAALA_GEN_SEED=%d\n%s\n %s", seed, witnessed, seed, small, d)
			default:
				mode := "plain"
				if witnessed {
					mode = "witnessed"
				}
				compared["program/"+mode]++
				for _, s := range prog.shapes {
					compared[s+"/"+mode]++
				}
			}
			if failed >= envInt("JAALA_GEN_MAX_FAILURES", 5) {
				t.Fatalf("stopping after %d disagreements", failed)
			}
		}
	}
	shapes := []string{"program", "recursion", "negation", "head aggregate", "goal aggregate", "body aggregate", "bound goal", "set-bound goal", "arithmetic", "bound fact", "avg", "closure", "paths", "optional", "list read"}
	var counts, issues []string
	for _, sh := range shapes {
		counts = append(counts, fmt.Sprintf("%s %d/%d", sh, compared[sh+"/plain"], compared[sh+"/witnessed"]))
	}
	for _, k := range knownDisagreements {
		if known[k.issue] > 0 {
			issues = append(issues, fmt.Sprintf("#%d %d", k.issue, known[k.issue]))
		}
	}
	t.Logf("%d seeds\n compared (plain/witnessed): %s\n skipped over budget: %d\n known disagreements, counted not failed: %s\n units: %d programs' facts carry them; %d answers hold a number with a unit, %d an aggregate absent over mixed units, %d folded from mV, kV or A*ohm",
		seeds, strings.Join(counts, ", "), skipped, strings.Join(issues, ", "), withUnits, unitAnswers, mixedAnswers, foldedAnswers)
	if seeds < 50 {
		return
	}
	if compared["arithmetic/plain"] == 0 || compared["arithmetic/witnessed"] == 0 {
		t.Errorf("control: no program computed with arithmetic (#209)")
	}
	if compared["closure/plain"] == 0 || compared["closure/witnessed"] == 0 {
		t.Errorf("control: no program walked a relation with graph.closure (#281)")
	}
	if compared["list read/plain"] == 0 || compared["list read/witnessed"] == 0 {
		t.Errorf("control: no program read back a list a rule collected (#283)")
	}
	if compared["optional/plain"] == 0 || compared["optional/witnessed"] == 0 {
		t.Errorf("control: no program matched optionally (#282)")
	}
	if compared["paths/plain"] == 0 || compared["paths/witnessed"] == 0 {
		t.Errorf("control: no program listed paths with graph.paths (#280)")
	}
	if compared["bound fact/plain"] == 0 || compared["bound fact/witnessed"] == 0 {
		t.Errorf("control: no program bound an edge's fact (#279)")
	}
	if compared["avg/plain"] == 0 || compared["avg/witnessed"] == 0 {
		t.Errorf("control: no program averaged (#284)")
	}
	if lookedUpPrograms == 0 {
		t.Errorf("control: no program made a lookup through a Source answering them")
	}
	if federatedPrograms == 0 {
		t.Errorf("control: no program made a lookup through a Federation's members")
	}
	if batchedPrograms == 0 {
		t.Errorf("control: no program asked a batching Source for a batch")
	}
	if pushedPrograms == 0 {
		t.Errorf("control: no program pushed a conjunction to the Source")
	}
	if stepWalkedPrograms == 0 {
		t.Errorf("control: no graph's walk of a two-literal step was walked by the store")
	}
	if batchPushedPrograms == 0 {
		t.Errorf("control: no program pushed a group for a batch of bindings")
	}
	if pagedPrograms == 0 {
		t.Errorf("control: no program's goal was paged by the store")
	}
	if summarizedPrograms == 0 {
		t.Errorf("control: no program's goal was summarized by the store")
	}
	if walkedPrograms == 0 {
		t.Errorf("control: no program asked a store to walk a relation")
	}
	t.Logf("closures and paths fetched ahead (#396): %d programs batched, %d through a federation, %d graphs walked by the store", fetchedWalkPrograms, federatedWalkPrograms, walkedClosurePrograms)
	if fetchedWalkPrograms == 0 || federatedWalkPrograms == 0 || walkedClosurePrograms == 0 {
		t.Errorf("control: %d programs batched a closure's or a paths call's steps, %d through a federation, and %d graphs' bounded closures were walked by the store (#396)", fetchedWalkPrograms, federatedWalkPrograms, walkedClosurePrograms)
	}
	if alonePushedPrograms == 0 {
		t.Errorf("control: no program pushed a literal alone for the store to filter")
	}
	if partitionedBatchPrograms == 0 {
		t.Errorf("control: no program pushed a batch of bindings across a partitioned federation's members")
	}
	if streamedPrograms == 0 {
		t.Errorf("control: no program's goal streamed a relation into its answer")
	}
	t.Logf("sessions: %d programs pushed to the store beneath one, %d batched, %d looked up a relation it touched", overlaidPushed, overlaidBatched, overlaidLookedUp)
	if overlaidPushed == 0 || overlaidBatched == 0 || overlaidLookedUp == 0 {
		t.Errorf("control: over a session, %d programs pushed to the store, %d batched and %d looked up a relation the session touched", overlaidPushed, overlaidBatched, overlaidLookedUp)
	}
	t.Logf("versions: %d programs answered differently between versions, %d asked a version's store", atPrograms, atStoredPrograms)
	t.Logf("across: %d programs answered rows holding in some versions only, %d of them aggregating, %d looked a store up", acrossPrograms, acrossAggregates, acrossLookedPrograms)
	if acrossAggregates < seeds/10 {
		t.Errorf("control: %d aggregating programs answered across versions with a row holding in some only, over %d seeds", acrossAggregates, seeds)
	}
	if acrossPrograms < seeds/10 {
		t.Errorf("control: %d programs answered across versions with a row holding in some only, over %d seeds", acrossPrograms, seeds)
	}
	if acrossLookedPrograms < seeds/10 {
		t.Errorf("control: %d programs looked a store up across versions over %d seeds", acrossLookedPrograms, seeds)
	}
	if atStoredPrograms < seeds/10 {
		t.Errorf("control: %d programs asked a version's store over %d seeds", atStoredPrograms, seeds)
	}
	if atPrograms < seeds/10 {
		t.Errorf("control: %d programs answered differently between versions over %d seeds", atPrograms, seeds)
	}
	if partitionedPrograms == 0 {
		t.Errorf("control: no program pushed a conjunction across a partitioned federation's members")
	}
	if unitAnswers < seeds/100 {
		t.Errorf("control: %d answers held a unit over %d seeds: units aren't reaching the goals", unitAnswers, seeds)
	}
	if foldedAnswers == 0 {
		t.Errorf("control: no answer over weights served in mV, kV or A*ohm held a number in V, over %d seeds", seeds)
	}
	// An aggregate over mixed units is rare (about 30 in 5000 seeds' two runs), so CI's 300 seeds
	// needn't meet one; ./selfcheck.sh's 5000 do, which is what catches a unit-blind aggregate.
	if seeds >= 1000 && mixedAnswers == 0 {
		t.Errorf("control: no answer held an aggregate over mixed units, over %d seeds", seeds)
	}
	for _, s := range shapes {
		for _, mode := range []string{"plain", "witnessed"} {
			if compared[s+"/"+mode] == 0 {
				t.Errorf("control: no %s program was compared %s", s, mode)
			}
		}
	}
}

// The shrinker has to reach the smallest case that still fails, or a reported disagreement buries
// its cause in a dozen rules. Failing here means "a valid program with a rule that negates", whose
// smallest form is one rule, a one-literal goal, and one node with no edges.
// A seed generates one program, so JAALA_GEN_SEED=n replays what a run reported (#226).
func TestASeedGeneratesOneProgram(t *testing.T) {
	gen := func(seed int64) string {
		rnd := rand.New(rand.NewSource(seed))
		facts := genGraph(rnd)
		p := newGenerator(rnd, facts).program()
		return fmt.Sprintf("%s\n bind: %v\n facts: %s", p, p.goal.bind, facts)
	}
	aggregates := 0
	for seed := int64(1); seed <= int64(envInt("JAALA_GEN_SEEDS", 300)); seed++ {
		first := gen(seed)
		if strings.Contains(first, ": {") {
			aggregates++
		}
		for range 3 {
			if again := gen(seed); again != first {
				t.Fatalf("seed %d generated two programs:\n %s\n %s", seed, first, again)
			}
		}
	}
	if aggregates == 0 {
		t.Error("control: no seed's program has a body aggregate, whose shared variables the map walk chose")
	}
}

func TestShrinkReachesTheSmallestFailingCase(t *testing.T) {
	negates := func(c genCase) (bool, string) {
		if out, _ := c.check(); out == genInvalid {
			return false, ""
		}
		for _, r := range c.prog.rules {
			for _, l := range r.body {
				if l.neg {
					return true, ""
				}
			}
		}
		return false, ""
	}
	for seed := int64(1); seed <= 40; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		facts := genGraph(rnd)
		c := genCase{prog: newGenerator(rnd, facts).program(), facts: facts, witnessed: true}
		if ok, _ := negates(c); !ok || len(c.prog.rules) < 3 || len(c.facts.edges) == 0 {
			continue
		}
		small, _ := shrinkWhile(c, "", negates)
		if len(small.prog.rules) != 1 || len(small.prog.goal.body) != 1 || small.facts.n != 1 || len(small.facts.edges) != 0 || small.witnessed {
			t.Errorf("seed %d shrank to\n%s\nwant one rule, one goal literal, one node, no edges, not witnessed", seed, small)
		}
		return
	}
	t.Fatal("control: no seed in 1..40 gave a program with a negating rule, three rules and an edge")
}

// Each known disagreement's repro must still disagree, and be recognised as that issue: a pattern
// that matches nothing would let the bug through as known, and a fixed bug should leave the list.
func TestKnownDisagreementsStillDisagree(t *testing.T) {
	for _, k := range knownDisagreements {
		out, diff := k.repro.check()
		if out != genDisagreed {
			t.Errorf("#%d: the repro no longer disagrees (outcome %d %s); if #%d is fixed, drop it from knownDisagreements\n%s", k.issue, out, diff, k.issue, k.repro)
			continue
		}
		if got := knownDisagreement(diff); got != k.issue {
			t.Errorf("#%d: the repro's disagreement is recognised as #%d\n%q", k.issue, got, diff)
		}
	}
}

// errorOf runs an evaluator and returns its error, turning a panic into one so the test can say
// which evaluator panicked on which program.
func errorOf(ev Evaluator, q Query, b *Base) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("PANIC: %v", p)
		}
	}()
	_, err = ev.Eval(bg, q, b)
	return err
}

// Every evaluator refuses a broken program with the same query: error and never panics (the first
// step of #89). Each generated program is broken three ways, one literal at a time: an argument
// dropped, an argument added, and a relation renamed to one nothing defines. Each variant is wrong,
// so each has to fail. The planned evaluator used to panic on the first (#133).
func TestBrokenProgramsFailTheSameWay(t *testing.T) {
	seeds := envInt("JAALA_GEN_SEEDS", 300)
	checked, failed := 0, 0
	for seed := int64(1); seed <= int64(seeds); seed++ {
		rnd := rand.New(rand.NewSource(seed))
		facts := genGraph(rnd)
		prog := newGenerator(rnd, facts).program()
		for i, broken := range breakProgram(prog, rnd) {
			text := broken.String()
			q, err := Parse(text)
			if err != nil {
				continue // refused by the parser, the same for every evaluator
			}
			b := baseFor(std(facts.source()))
			var errs []string
			var first error
			for _, ev := range []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}, SemiNaive{}} {
				err := errorOf(ev, q, b)
				if first == nil {
					first = err
				}
				errs = append(errs, fmt.Sprint(err))
			}
			checked++
			ok := strings.HasPrefix(errs[0], "query: ") && errs[1] == errs[0] && errs[2] == errs[0]
			if !ok {
				failed++
				t.Errorf("seed %d: %s\n naive:    %s\n written:  %s\n planned:  %s", seed, broken, errs[0], errs[1], errs[2])
			}
			// The literal breakProgram changed is what the error is about, and Locate finds it (#321).
			// Dropping an argument can drop a negation's only anchor, which is checked first.
			want := []ErrorKind{KindArity, KindArity, KindUnknown}[i]
			var ce *CheckError
			if errors.As(first, &ce) && i == 0 && ce.Kind == KindUnbound {
				continue
			}
			if !errors.As(first, &ce) || ce.Kind != want {
				failed++
				t.Errorf("seed %d: %s\n gave %T %v, want a %s CheckError", seed, broken, first, first, want)
			} else if pos, found := Locate(text, ce); !found || !strings.HasPrefix(text[pos.Offset:], ce.Relation+"(") {
				failed++
				t.Errorf("seed %d: %s\n %v: Locate = %+v %v, not a call to %s", seed, broken, ce, pos, found, ce.Relation)
			}
			if failed >= 5 {
				t.Fatal("stopping after 5")
			}
		}
	}
	if checked < seeds {
		t.Fatalf("control: only %d broken programs reached the evaluators", checked)
	}
	t.Logf("%d broken programs, each refused alike by all three evaluators", checked)
}

// breakProgram returns three wrong versions of p, each changing one literal that names a relation:
// one argument fewer, one more, and a name nothing defines.
func breakProgram(p genProgram, rnd *rand.Rand) []genProgram {
	type site struct{ rule, lit int } // rule -1 is the goal
	var sites []site
	for i, r := range p.rules {
		for j, l := range r.body {
			if l.rel != "" && len(l.args) > 1 {
				sites = append(sites, site{i, j})
			}
		}
	}
	for j, l := range p.goal.body {
		if l.rel != "" && len(l.args) > 1 {
			sites = append(sites, site{-1, j})
		}
	}
	if len(sites) == 0 {
		return nil
	}
	s := sites[rnd.Intn(len(sites))]
	var out []genProgram
	for _, change := range []func(*genLit){
		func(l *genLit) { l.args = l.args[:len(l.args)-1] },
		func(l *genLit) { l.args = append(l.args, `"extra"`) },
		func(l *genLit) { l.rel = "nothing_" + l.rel },
	} {
		c := p.clone()
		body := c.goal.body
		if s.rule >= 0 {
			body = c.rules[s.rule].body
		}
		l := body[s.lit]
		l.args = slices.Clone(l.args)
		change(&l)
		body[s.lit] = l
		out = append(out, c)
	}
	return out
}
