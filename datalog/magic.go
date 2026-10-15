package datalog

import (
	"slices"
	"sort"
	"strconv"
	"strings"
)

// magic is SemiNaive's demand-driven rewrite, the magic-sets transformation, run after unfold and
// before plan. A derived relation called with bound arguments is evaluated only for the values the
// query demands, rather than in full and then filtered. Asked reach("v0", ?x), the rewrite derives
//
//	m_reach_bf("v0").
//	reach_bf(?a, ?b) :- m_reach_bf(?a), edge(?a, ?b);
//	reach_bf(?a, ?c) :- m_reach_bf(?a), reach_bf(?a, ?b), edge(?b, ?c);
//
// so only reach tuples starting at v0 are computed. The "bf" adornment records which arguments are
// bound (b) and free (f) at a call; the magic relation m_reach_bf holds the bound values demanded so
// far. A rule passes demand on to what it calls: in reach(?a, ?c) :- edge(?a, ?b), reach(?b, ?c),
// demand for ?a becomes demand for each ?b, m_reach_bf(?b) :- m_reach_bf(?a), edge(?a, ?b).
// Everything is still ordinary rules, so semi-naive evaluation, strata and planning apply unchanged.
//
// The literals that run before a call (its prefix) are what the magic rule passing demand into it
// reads, and the rule they came from reads them too. So that they run once, a prefix that does more
// than read the guard is stored in a supplementary relation (supplementary magic sets, #54), which
// both read:
//
//	s1(?a, ?b) :- m_reach_bf(?a), edge(?a, ?b);
//	m_reach_bf(?b) :- s1(?a, ?b);
//	reach_bf(?a, ?c) :- s1(?a, ?b), reach_bf(?b, ?c);
//
// Right-linear recursion called from the goal with constants is factored instead (see factor): plain
// magic sets would derive a full answer set for every node the recursion passes through.
//
// Demand also starts from a constant in a rule that nothing demands, such as one the query defines and
// asks for with nothing bound (#57): uses(?t) :- covers(?t, "p.write") calls covers_fb, demanded by the
// fact m_covers_fb("p.write"). See fromConstants.
//
// Which arguments are bound at a call depends on the order a body runs in, so each body is ordered by
// the planner first, starting from what its head has bound. A call with nothing bound, from a body the
// rewrite guards, is demanded all the same (#60): it calls reach_ff, whose rules are guarded by the
// zero-argument m_reach_ff(), derived from the caller's guard. When no caller is demanded, nothing in
// reach is derived. A call with nothing bound from the goal, or from a rule evaluated in full, would
// demand it unconditionally, so it is left calling the original relation, which is evaluated in full
// as before. So is a call to a relation whose rule aggregates (#4). When the original is evaluated in
// full anyway, because something still reads it, the all-free calls read it too (see foldFree), so a
// relation is never derived twice.
//
// Demand passes through relations that use negation (#34), and into a negated call: in
// nocov(?c) :- p(?c), not cov(?c), demand for nocov becomes demand for cov at the ?c that reach the
// negation. Demand into a negation can make a stratified program unstratifiable (a recursive caller's
// demand for cov would depend on the caller, which negates cov), so when the rewritten program does not
// stratify, the rewrite is made again with negated calls reading their relations in full. That one
// usually stratifies, but not always (#93): demand flowing down from a rule above an aggregate can
// reach the relation the aggregate reduces, and a negation can still sit in a cycle the demand closes.
// When neither rewrite stratifies, the program runs without demand, as written, which stratify has
// already accepted.
//
// Nor does it run with demand when the rewrite would derive one relation under two adornments (see
// severalAdornments).
//
// Magic tuples record what was asked, not what produced an answer, so their citations must not reach
// an answer: SemiNaive's fixpoint derives them without citations (see SemiNaive.materialize). A
// supplementary tuple is a prefix's result, so it keeps its citations, and under Witnesses the
// witnesses of the literals it stands for (see idbTuple.parts). Under CanonicalCites there are none: a
// supplementary relation is a set over the prefix's variables, so two derivations differing only in a
// `_` would be stored as one, and the one kept would not be the one Naive's full comparison keeps.
func magic(b *Base, q Query) Query {
	for _, intoNeg := range []bool{true, false} {
		out := magicWith(b, q, intoNeg)
		if severalAdornments(out.Rules) {
			return q
		}
		if _, err := stratify(out.Rules, derivedArity(out.Rules)); err == nil {
			return out
		}
	}
	return q
}

// severalAdornments reports whether the rewrite derives some relation under more than one adornment.
// Each adornment is a copy of the relation with its own demand, and when callers ask for it bound in
// different places the copies between them can cover all of it, twice, plus the demand that drove
// them: demand for one points-to variable cost three times the whole analysis (#96). Reading the
// program as written is never worse than that.
func severalAdornments(rules []Rule) bool {
	seen := map[string]string{}
	for _, r := range rules {
		rel, adorn, ok := strings.Cut(r.Head.Relation, "\x00/")
		if !ok {
			continue
		}
		if prev, ok := seen[rel]; ok && prev != adorn {
			return true
		}
		seen[rel] = adorn
	}
	return false
}

// magicWith makes the rewrite, pushing demand into negated calls when intoNeg is set (see magic).
func magicWith(b *Base, q Query, intoNeg bool) Query {
	m := &magician{b: b, byHead: map[string][]Rule{}, done: map[string]bool{}, intoNeg: intoNeg}
	for _, r := range q.Rules {
		m.byHead[r.Head.Relation] = append(m.byHead[r.Head.Relation], r)
	}
	m.asGoal = asGoal(b, q, m.byHead)
	out := q
	if len(out.Select) == 0 {
		out.Select = defaultSelect(q.Goal)
	}
	// A supplementary relation is a set, so the goal keeps its own prefix when an aggregate counts
	// its bindings (see countsBindings).
	supply := !countsBindings(out.Select, q.Having) && !b.canonical()
	out.Goal = Body{Literals: m.body(nil, planGoal(b, q.Goal).Literals, nil, supply)}
	m.drain()
	originals := make([]Rule, len(q.Rules))
	for i, r := range q.Rules {
		r.Body = m.fromConstants(r)
		originals[i] = r
	}
	m.drain()
	if len(m.rules) == 0 {
		return q
	}
	out.Rules = append(originals, m.rules...)
	return m.foldFree(q, dropUnreached(q, out))
}

// foldFree points the all-free calls of a relation back at the original, and drops the all-free
// relation, whenever the rewritten program still reads the original: it is evaluated in full anyway,
// and deriving it a second time behind a guard would only add work. Folding one relation back can
// leave another's original read, through the original rules it now reaches, so it repeats until none
// changes.
func (m *magician) foldFree(before, q Query) Query {
	free := m.free
	for {
		read := reached(q)
		folded := map[string]string{} // all-free relation -> original
		var gone []string
		var left []adorned
		for _, f := range free {
			if !read[f.rel] {
				left = append(left, f)
				continue
			}
			folded[adornedName(f.rel, f.adorn)] = f.rel
			gone = append(gone, magicName(f.rel, f.adorn))
		}
		if len(folded) == 0 {
			return q
		}
		free = left
		rename := func(lits []Literal) []Literal {
			out := make([]Literal, len(lits))
			for i, l := range lits {
				switch {
				case l.Pos != nil && folded[l.Pos.Relation] != "":
					a := Atom{Relation: folded[l.Pos.Relation], Args: l.Pos.Args}
					l = Literal{Pos: &a, at: l.at}
				case l.Neg != nil && folded[l.Neg.Relation] != "":
					a := Atom{Relation: folded[l.Neg.Relation], Args: l.Neg.Args}
					l = Literal{Neg: &a, at: l.at}
				}
				out[i] = l
			}
			return out
		}
		var rules []Rule
		for _, r := range q.Rules {
			if folded[r.Head.Relation] != "" || slices.Contains(gone, r.Head.Relation) {
				continue
			}
			r.Body = Body{Literals: rename(r.Body.Literals)}
			rules = append(rules, r)
		}
		q.Rules = withoutOrphans(rules)
		q.Goal = Body{Literals: rename(q.Goal.Literals)}
		q = dropUnreached(before, q)
	}
}

// withoutOrphans drops every rule that reads a relation the rewrite made and no rule defines any
// more, such as a supplementary relation over a magic relation foldFree removed. Such a rule can never
// fire, and evaluating it would read an unknown relation. A rewrite's relations are only ever read
// positively, so dropping the reader changes nothing else; it repeats, since a dropped rule can leave
// another relation without rules.
func withoutOrphans(rules []Rule) []Rule {
	for {
		defined := map[string]bool{}
		for _, r := range rules {
			defined[r.Head.Relation] = true
		}
		kept := rules[:0:0]
		for _, r := range rules {
			orphan := false
			for _, l := range r.Body.Literals {
				if l.Pos != nil && strings.Contains(l.Pos.Relation, "\x00") && !defined[l.Pos.Relation] {
					orphan = true
					break
				}
			}
			if !orphan {
				kept = append(kept, r)
			}
		}
		if len(kept) == len(rules) {
			return rules
		}
		rules = kept
	}
}

// sameAtom reports whether two atoms are the same relation over the same terms.
func sameAtom(a, b Atom) bool {
	if a.Relation != b.Relation || len(a.Args) != len(b.Args) {
		return false
	}
	for i := range a.Args {
		x, y := a.Args[i], b.Args[i]
		if x.Var != y.Var || (x.Const == nil) != (y.Const == nil) || (x.Const != nil && !valueEq(*x.Const, *y.Const)) {
			return false
		}
	}
	return true
}

// drain adorns the rules of every relation demand has reached so far.
func (m *magician) drain() {
	for len(m.queue) > 0 {
		a := m.queue[0]
		m.queue = m.queue[1:]
		m.adornRules(a.rel, a.adorn)
	}
}

// fromConstants rewrites the calls a rule makes with constants, for a rule evaluated in full (#57):
// nothing demands its head, but a constant in its body is demand all the same. Such a call is adorned
// by its constants alone, so its demand rule is a fact, which depends on nothing and so cannot make
// the program unstratifiable; this holds for a negated call as much as a positive one. Arguments that
// earlier literals bind are not used: a rule evaluated in full binds them for every row, which would
// demand as much as the whole relation. A call into the rule's own recursion is left alone, since that
// relation is being evaluated in full already.
//
// The exception is a rule unfold would have inlined into the goal but didn't, because the Eval is
// witnessed (see asGoal): its body gets the demand the goal's would have, sideways.
func (m *magician) fromConstants(r Rule) Body {
	if m.asGoal[r.Head.Relation] {
		return m.sideways(r)
	}
	none := map[Var]bool{}
	lits := make([]Literal, len(r.Body.Literals))
	for i, lit := range r.Body.Literals {
		switch {
		case lit.Pos != nil && !m.reaches(lit.Pos.Relation, r.Head.Relation):
			if call, ok := m.factor(*lit.Pos, none); ok {
				lit = Literal{Pos: &call, at: lit.at}
			} else if m.wants(*lit.Pos, none, false) {
				call := m.call(*lit.Pos, none, nil)
				lit = Literal{Pos: &call, at: lit.at}
			}
		case lit.Neg != nil && !m.reaches(lit.Neg.Relation, r.Head.Relation) && m.wants(*lit.Neg, none, false):
			call := m.call(*lit.Neg, none, nil)
			lit = Literal{Neg: &call, at: lit.at}
		}
		lits[i] = lit
	}
	return Body{Literals: lits}
}

// sideways rewrites a rule evaluated in full that stands in for the goal (#270): a call its earlier
// literals bind, in planned order, is demanded by them, as the goal's calls are, so a generator's
// bindings reach the relations it calls (Declaire's q09r: graph.reach, then http.route). A call bound
// by constants alone still gets a fact for its demand.
func (m *magician) sideways(r Rule) Body {
	none := map[Var]bool{}
	bound := map[Var]bool{}
	supply := !m.b.canonical() && !countsBindings(r.Head.Args, nil)
	var front []Literal
	pos, negs := splitNegations(planBody(m.b, r.Body, nil).Literals)
	for _, lit := range pos {
		if lit.Pos != nil {
			if a := *lit.Pos; !m.reaches(a.Relation, r.Head.Relation) && m.wants(a, bound, false) {
				var call Atom
				if readsAny(a, bound) {
					if supply && doesWork(front) {
						front = []Literal{m.supplement(front, bound)}
					}
					call = m.call(a, bound, front)
				} else {
					call = m.call(a, none, nil)
				}
				lit = Literal{Pos: &call, at: lit.at}
			}
			bindAll(lit.Pos, bound)
		}
		front = append(front, lit)
	}
	for _, lit := range negs {
		if !m.reaches(lit.Neg.Relation, r.Head.Relation) {
			switch {
			case m.intoNeg && readsAny(*lit.Neg, bound) && m.wants(*lit.Neg, bound, false):
				call := m.call(*lit.Neg, bound, front)
				lit = Literal{Neg: &call, at: lit.at}
			case m.wants(*lit.Neg, none, false):
				call := m.call(*lit.Neg, none, nil)
				lit = Literal{Neg: &call, at: lit.at}
			}
		}
		front = append(front, lit)
	}
	return Body{Literals: front}
}

// asGoal lists the relations unfold would have inlined into the goal, and into what it inlined there,
// had the Eval not been witnessed (see unfold). Witnesses keep a relation's node, so they turn
// inlining off, but they shouldn't turn off the demand the inlined body would have carried.
func asGoal(b *Base, q Query, byHead map[string][]Rule) map[string]bool {
	out := map[string]bool{}
	if !b.witnessing() || countsBindings(q.Select, q.Having) {
		return out
	}
	u := &unfolder{byHead: byHead, recursive: recursiveRelations(q.Rules), reg: b.reg}
	queue := q.Goal.Literals
	for len(queue) > 0 {
		lit := queue[0]
		queue = queue[1:]
		if lit.Pos == nil || out[lit.Pos.Relation] {
			continue
		}
		if _, ok := u.inline(*lit.Pos); ok {
			out[lit.Pos.Relation] = true
			if r := byHead[lit.Pos.Relation][0]; !countsBindings(r.Head.Args, nil) {
				queue = append(queue, r.Body.Literals...)
			}
		}
	}
	return out
}

// readsAny reports whether a call has an argument some earlier literal binds.
func readsAny(a Atom, bound map[Var]bool) bool {
	for _, t := range a.Args {
		if t.Var != "" && t.Var != "_" && bound[t.Var] {
			return true
		}
	}
	return false
}

// derivedArity is the arity of each relation the rules derive, as stratify takes it.
func derivedArity(rules []Rule) map[string]int {
	out := map[string]int{}
	for _, r := range rules {
		out[r.Head.Relation] = len(r.Head.Args)
	}
	return out
}

type magician struct {
	b        *Base
	byHead   map[string][]Rule
	intoNeg  bool            // push demand into negated calls
	done     map[string]bool // adorned relations already queued
	queue    []adorned
	rules    []Rule          // magic, supplementary, adorned and factored rules, in the order made
	factored int             // goal calls factored so far, numbering their relations
	supplied int             // supplementary relations made so far, numbering them
	free     []adorned       // all-free adornments made (#60), for foldFree
	asGoal   map[string]bool // relations whose rules get the goal's demand (see asGoal)
}

type adorned struct{ rel, adorn string }

// body rewrites a planned body for demand: guard is the demand it runs under (nil in the goal), entry
// what that binds. Each call to a derived relation that binds an argument becomes a call to the adorned
// relation, with a magic rule passing the demand in; a goal call may be factored instead. When supply
// is set, a prefix that does more than read demand is stored once in a supplementary relation, which
// the magic rule and the rest of the body both read.
func (m *magician) body(guard *Atom, lits []Literal, entry map[Var]bool, supply bool) []Literal {
	bound := map[Var]bool{}
	for v := range entry {
		bound[v] = true
	}
	var front []Literal // what runs before the next literal, from the guard or the last supplementary relation
	if guard != nil {
		front = []Literal{{Pos: guard}}
	}
	demand := func(a Atom) (Atom, bool) {
		if guard == nil {
			if call, ok := m.factor(a, bound); ok {
				return call, true
			}
		}
		if !m.wants(a, bound, guard != nil) {
			return a, false
		}
		if supply && doesWork(front) {
			front = []Literal{m.supplement(front, bound)}
		}
		return m.call(a, bound, front), true
	}
	pos, negs := splitNegations(lits)
	for _, lit := range pos {
		if lit.Pos != nil {
			if call, ok := demand(*lit.Pos); ok {
				lit = Literal{Pos: &call, at: lit.at}
			}
			bindAll(lit.Pos, bound)
		}
		front = append(front, lit)
	}
	var outNegs []Literal
	for _, lit := range negs {
		if m.intoNeg && m.wants(*lit.Neg, bound, guard != nil) {
			call, _ := demand(*lit.Neg)
			lit = Literal{Neg: &call, at: lit.at}
		}
		outNegs = append(outNegs, lit)
	}
	return append(front, outNegs...)
}

// wants reports whether a call can be rewritten for demand: it reads a derived relation and binds at
// least one argument, or binds none from a body the rewrite guards (#60).
func (m *magician) wants(a Atom, bound map[Var]bool, guarded bool) bool {
	if rules, derived := m.byHead[a.Relation]; !derived || m.aggregates(a.Relation) || len(rules[0].Head.Args) != len(a.Args) {
		return false // a wrong arity is refused before any rewrite (checkWrittenArity); never index past it
	}
	adorn, _ := adornment(a, bound)
	return guarded || strings.Contains(adorn, "b")
}

// aggregates reports whether rel is an aggregating relation (#4), which is read in full: its head's
// aggregate positions are not values a caller can demand, and a supplementary relation in its body
// would turn the bindings a count reduces into a set.
func (m *magician) aggregates(rel string) bool {
	for _, r := range m.byHead[rel] {
		if r.aggregates() {
			return true
		}
	}
	return false
}

// call adds the magic rule passing a call's demand in, reading front (the guard and the literals that
// run before the call), and returns the call to the adorned relation.
//
// A magic rule that would only restate its own guard, as a recursive call passing on the demand it
// received does, is left out.
func (m *magician) call(a Atom, bound map[Var]bool, front []Literal) Atom {
	adorn, demanded := adornment(a, bound)
	head := Atom{Relation: magicName(a.Relation, adorn), Args: demanded}
	if !(len(front) == 1 && front[0].Pos != nil && sameAtom(*front[0].Pos, head)) {
		m.rules = append(m.rules, Rule{Head: head, Body: Body{Literals: append([]Literal(nil), front...)}})
	}
	if key := adornedName(a.Relation, adorn); !m.done[key] {
		m.done[key] = true
		m.queue = append(m.queue, adorned{a.Relation, adorn})
		if !strings.Contains(adorn, "b") {
			m.free = append(m.free, adorned{a.Relation, adorn})
		}
	}
	return Atom{Relation: adornedName(a.Relation, adorn), Args: a.Args}
}

// supplement stores front in a new supplementary relation over every variable bound so far, and
// returns the literal reading it.
func (m *magician) supplement(front []Literal, bound map[Var]bool) Literal {
	var vars []string
	for v := range bound {
		if v != "_" {
			vars = append(vars, string(v))
		}
	}
	sort.Strings(vars)
	head := Atom{Relation: supPrefix + strconv.Itoa(m.supplied)}
	m.supplied++
	for _, v := range vars {
		head.Args = append(head.Args, Term{Var: Var(v)})
	}
	m.rules = append(m.rules, Rule{Head: head, Body: Body{Literals: append([]Literal(nil), front...)}})
	return Literal{Pos: &head}
}

// doesWork reports whether front reads anything besides demand, so storing it once saves work.
func doesWork(front []Literal) bool {
	for _, l := range front {
		if l.Pos != nil && !isGuard(l.Pos.Relation) {
			return true
		}
	}
	return false
}

// adornRules makes the adorned version of each rule of rel: guarded by its magic relation, with the
// calls in its body rewritten for the demand they now receive.
func (m *magician) adornRules(rel, adorn string) {
	for _, r := range m.byHead[rel] {
		entry := map[Var]bool{}
		var demanded []Term
		for i, t := range r.Head.Args {
			if adorn[i] == 'b' {
				demanded = append(demanded, t)
				if t.Var != "" && t.Var != "_" {
					entry[t.Var] = true
				}
			}
		}
		guard := &Atom{Relation: magicName(rel, adorn), Args: demanded}
		m.rules = append(m.rules, Rule{
			Head:      Atom{Relation: adornedName(rel, adorn), Args: r.Head.Args},
			Body:      Body{Literals: m.body(guard, planBody(m.b, r.Body, entry).Literals, entry, !m.b.canonical())},
			Hops:      r.Hops,
			HeadTypes: r.HeadTypes,
			text:      r.text,
		})
	}
}

// adornment is a call's pattern of bound (b) and free (f) arguments, with the bound terms.
func adornment(a Atom, bound map[Var]bool) (string, []Term) {
	var sb strings.Builder
	var demanded []Term
	for _, t := range a.Args {
		if t.Const != nil || (t.Var != "" && t.Var != "_" && bound[t.Var]) {
			sb.WriteByte('b')
			demanded = append(demanded, t)
		} else {
			sb.WriteByte('f')
		}
	}
	return sb.String(), demanded
}

// The names a rewrite gives its relations hold a byte no query can spell.
const magicPrefix = "\x00m:"

func magicName(rel, adorn string) string   { return magicPrefix + rel + "/" + adorn }
func adornedName(rel, adorn string) string { return rel + "\x00/" + adorn }
func isMagic(rel string) bool              { return strings.HasPrefix(rel, magicPrefix) }

// supPrefix names a supplementary relation (see magic).
const supPrefix = "\x00s:"

func isSupplementary(rel string) bool { return strings.HasPrefix(rel, supPrefix) }

// isGuard reports whether rel is what a rewritten body starts from: a magic relation, a supplementary
// one, or a factored reachable set. A rule the rewrite guards with one reads it first.
func isGuard(rel string) bool {
	return isMagic(rel) || isSupplementary(rel) || strings.Contains(rel, fromSep)
}
