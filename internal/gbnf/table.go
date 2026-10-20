package gbnf

import (
	"fmt"
	"math/rand"
	"strings"
)

// A Rule is one production of a grammar written as a table: the front ends' grammars (nl's program
// syntax, #323, and the Cypher subset, #354) are tables that Render writes as GBNF and a Sampler draws
// texts from, so a test can check every text the grammar accepts against the parser or compiler. A
// table's first rule is its root.
type Rule struct {
	Name string
	E    Expr
}

// An Expr is one of Lit, Ref, Class, Seq, Alt and Rep.
type Expr any

type (
	// Lit is the text itself.
	Lit string
	// Ref is another rule, by name.
	Ref string
	// Class is one character of a GBNF class (Spec, without its brackets: a-z, ^"\\), which a Sampler
	// draws from Sample, every character of it inside Spec.
	Class struct {
		Spec   string
		Sample string
	}
	// Seq is its parts in order.
	Seq []Expr
	// Alt is one of its parts.
	Alt []Expr
	// Rep is E from Min to Max times, Max -1 for unbounded.
	Rep struct {
		E        Expr
		Min, Max int
	}
)

// Opt is e or nothing, Star e any number of times, Plus e at least once, and UpTo e at most n times.
func Opt(e Expr) Rep         { return Rep{e, 0, 1} }
func Star(e Expr) Rep        { return Rep{e, 0, -1} }
func Plus(e Expr) Rep        { return Rep{e, 1, -1} }
func UpTo(e Expr, n int) Rep { return Rep{e, 0, n} }

// List is e, then any more of e each after sep.
func List(e Expr, sep string) Seq { return Seq{e, Star(Seq{Lit(sep), e})} }

// Render writes rules as GBNF, one rule a line, in llama.cpp's form.
func Render(rules []Rule) string {
	var sb strings.Builder
	for _, r := range rules {
		fmt.Fprintf(&sb, "%s ::= %s\n", r.Name, renderExpr(r.E, true))
	}
	return sb.String()
}

func renderExpr(e Expr, top bool) string {
	switch e := e.(type) {
	case Lit:
		return Quote(string(e))
	case Ref:
		return string(e)
	case Class:
		return "[" + e.Spec + "]"
	case Seq:
		parts := make([]string, len(e))
		for i, x := range e {
			parts[i] = renderExpr(x, false)
		}
		if top || len(e) == 1 {
			return strings.Join(parts, " ")
		}
		return "(" + strings.Join(parts, " ") + ")"
	case Alt:
		parts := make([]string, len(e))
		for i, x := range e {
			parts[i] = renderExpr(x, true)
		}
		if top {
			return strings.Join(parts, " | ")
		}
		return "(" + strings.Join(parts, " | ") + ")"
	case Rep:
		inner := renderExpr(e.E, false)
		switch {
		case e.Min == 0 && e.Max == 1:
			return inner + "?"
		case e.Min == 0 && e.Max < 0:
			return inner + "*"
		case e.Min == 1 && e.Max < 0:
			return inner + "+"
		}
		return fmt.Sprintf("%s{%d,%d}", inner, e.Min, e.Max)
	}
	panic(fmt.Sprintf("gbnf: no Expr %T", e))
}

// Quote writes a GBNF string literal.
func Quote(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			sb.WriteByte('\\')
			sb.WriteRune(r)
		case '\n':
			sb.WriteString(`\n`)
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// A Sampler draws texts from a grammar, taking the shortest way out of every rule past maxDepth, so a
// test can check what the grammar accepts against the parser.
type Sampler struct {
	rules    map[string]Expr
	height   map[string]int // the fewest rule steps a rule needs to finish
	rnd      *rand.Rand
	maxDepth int
}

// NewSampler is a Sampler over rules, drawing from seed.
func NewSampler(rules []Rule, seed int64, maxDepth int) *Sampler {
	s := &Sampler{rules: map[string]Expr{}, height: map[string]int{}, rnd: rand.New(rand.NewSource(seed)), maxDepth: maxDepth} // #nosec G404 -- a seeded sampler, so a seed draws the same programs; nothing secret
	for _, r := range rules {
		s.rules[r.Name] = r.E
		s.height[r.Name] = 1 << 20
	}
	for changed := true; changed; {
		changed = false
		for _, r := range rules {
			if h := s.heightOf(r.E) + 1; h < s.height[r.Name] {
				s.height[r.Name], changed = h, true
			}
		}
	}
	return s
}

func (s *Sampler) heightOf(e Expr) int {
	switch e := e.(type) {
	case Ref:
		return s.height[string(e)]
	case Seq:
		h := 0
		for _, x := range e {
			h = max(h, s.heightOf(x))
		}
		return h
	case Alt:
		h := 1 << 20
		for _, x := range e {
			h = min(h, s.heightOf(x))
		}
		return h
	case Rep:
		if e.Min == 0 {
			return 0
		}
		return s.heightOf(e.E)
	}
	return 0
}

// Sample draws one text from the rule name.
func (s *Sampler) Sample(name string) string {
	var sb strings.Builder
	s.expand(&sb, Ref(name), 0)
	return sb.String()
}

func (s *Sampler) expand(sb *strings.Builder, e Expr, depth int) {
	switch e := e.(type) {
	case Lit:
		sb.WriteString(string(e))
	case Ref:
		s.expand(sb, s.rules[string(e)], depth+1)
	case Class:
		sb.WriteByte(e.Sample[s.rnd.Intn(len(e.Sample))])
	case Seq:
		for _, x := range e {
			s.expand(sb, x, depth)
		}
	case Alt:
		if depth <= s.maxDepth {
			s.expand(sb, e[s.rnd.Intn(len(e))], depth)
			return
		}
		best := e[0]
		for _, x := range e[1:] {
			if s.heightOf(x) < s.heightOf(best) {
				best = x
			}
		}
		s.expand(sb, best, depth)
	case Rep:
		n := e.Min
		if depth <= s.maxDepth {
			hi := e.Max
			if hi < 0 {
				hi = e.Min + 3
			}
			n += s.rnd.Intn(hi - e.Min + 1)
		}
		for i := 0; i < n; i++ {
			s.expand(sb, e.E, depth)
		}
	}
}
