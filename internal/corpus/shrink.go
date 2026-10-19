package corpus

import (
	"fmt"
	"math/rand"
	"slices"
)

// A Case is one program over one graph, evaluated plain or under Witnesses, which turns inlining and
// factoring off, so the two runs take different rewrites.
type Case struct {
	Prog      Program
	Facts     Facts
	Witnessed bool
}

func (c Case) String() string {
	return fmt.Sprintf(" shrunk program: %s\n facts: %s\n bind: %v, witnessed: %v", c.Prog, c.Facts, c.Prog.goal.bind, c.Witnessed)
}

// Shrink drops rules, literals, goal clauses, edges and nodes one at a time while fails still holds
// for the smaller case, and returns the smallest case it reached with what fails last said.
func Shrink(c Case, diff string, fails func(Case) (bool, string)) (Case, string) {
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

func (c Case) smaller() []Case {
	var out []Case
	with := func(edit func(p *Program, f *Facts)) {
		s := c
		s.Prog = c.Prog.clone()
		s.Facts = Facts{n: c.Facts.n, edges: slices.Clone(c.Facts.edges), weights: slices.Clone(c.Facts.weights), units: slices.Clone(c.Facts.units)}
		edit(&s.Prog, &s.Facts)
		out = append(out, s)
	}
	for i := range c.Prog.rules {
		with(func(p *Program, _ *Facts) { p.rules = slices.Delete(p.rules, i, i+1) })
	}
	// A derived relation read in a body can become a graph relation of its arity, so a relation only
	// read there can then be dropped. Only derived to graph, so every step makes the case smaller.
	for i, r := range c.Prog.rules {
		for j, l := range r.body {
			for _, base := range genGraphRels {
				if l.rel != "" && !slices.ContainsFunc(genGraphRels, func(g genRel) bool { return g.name == l.rel }) && len(base.cols) == len(l.args) {
					with(func(p *Program, _ *Facts) { p.rules[i].body[j].rel = base.name })
				}
			}
		}
	}
	heads := map[string]bool{}
	for _, r := range c.Prog.rules {
		if !heads[r.head] {
			heads[r.head] = true
			with(func(p *Program, _ *Facts) { p.drop(r.head) })
		}
	}
	for i, r := range c.Prog.rules {
		for j := range r.body {
			if len(r.body) > 1 {
				with(func(p *Program, _ *Facts) { p.rules[i].body = slices.Delete(p.rules[i].body, j, j+1) })
			}
		}
	}
	g := c.Prog.goal
	for j := range g.body {
		if len(g.body) > 1 {
			with(func(p *Program, _ *Facts) { p.goal.body = slices.Delete(p.goal.body, j, j+1) })
		}
	}
	for j := range g.sel {
		if len(g.sel) > 1 {
			with(func(p *Program, _ *Facts) { p.goal.sel = slices.Delete(p.goal.sel, j, j+1) })
		}
	}
	if g.having != "" {
		with(func(p *Program, _ *Facts) { p.goal.having = "" })
	}
	if g.order != "" {
		with(func(p *Program, _ *Facts) { p.goal.order = "" })
	}
	if g.limit > 0 {
		with(func(p *Program, _ *Facts) { p.goal.limit = 0 })
	}
	if len(g.bind) > 0 {
		with(func(p *Program, _ *Facts) { p.goal.bind = nil })
	}
	if c.Witnessed {
		with(func(*Program, *Facts) {})
		out[len(out)-1].Witnessed = false
	}
	for i := range c.Facts.edges {
		with(func(_ *Program, f *Facts) { f.edges = slices.Delete(f.edges, i, i+1) })
	}
	if c.Facts.n > 1 {
		with(func(_ *Program, f *Facts) {
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
func (p *Program) drop(rel string) {
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

func (p Program) clone() Program {
	c := p
	c.rules = make([]genRule, len(p.rules))
	for i, r := range p.rules {
		c.rules[i] = genRule{head: r.head, args: slices.Clone(r.args), body: slices.Clone(r.body)}
	}
	c.goal.body = slices.Clone(p.goal.body)
	c.goal.sel = slices.Clone(p.goal.sel)
	return c
}

// Break returns three wrong versions of p, each changing one literal that names a relation:
// one argument fewer, one more, and a name nothing defines.
func Break(p Program, rnd *rand.Rand) []Program {
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
	var out []Program
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
