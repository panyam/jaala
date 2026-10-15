package datalog

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/panyam/jaala/ns"
)

// Arithmetic is syntax over functions (#5). An Expr, legal as a comparison's operand, is an operator
// over terms, or a call to a function relation, whose last argument is its result:
//
//	?y = ?a * (?b + ?c)               num.add(?b, ?c, ?t), num.mul(?a, ?t, ?y)
//	?r = elec.parallel(?r1, ?r2) * 2  elec.parallel(?r1, ?r2, ?t), num.mul(?t, 2, ?r)
//
// lowerExpressions writes every expression as those calls on fresh variables before anything else
// reads the program, so every evaluator sees relations. The functions are stdlib's num.*, whose modes
// run them in any direction they can: ?v = ?i * ?r finds whichever of the three is missing. A lowered
// call waits in a body until one of its modes is satisfied (deferComparisons), so the order an
// expression is written in doesn't matter.

// An Expr is Op over Args: "+", "-", "*", "/" over two, "neg" over one, or "call" of Func.
type Expr struct {
	Op   string
	Func string
	Args []Term
}

// exprFuncs are the stdlib functions an operator is written in.
var exprFuncs = map[string]string{"+": "num.add", "-": "num.sub", "*": "num.mul", "/": "num.div", "neg": "num.neg"}

func (e *Expr) String() string {
	child := func(t Term) string {
		if t.Expr != nil && t.Expr.Op != "call" && t.Expr.Op != "neg" {
			return "(" + t.String() + ")"
		}
		return t.String()
	}
	switch e.Op {
	case "call":
		args := make([]string, len(e.Args))
		for i, a := range e.Args {
			args[i] = a.String()
		}
		return e.Func + "(" + strings.Join(args, ", ") + ")"
	case "neg":
		return "-" + child(e.Args[0])
	}
	return child(e.Args[0]) + " " + e.Op + " " + child(e.Args[1])
}

// parseOperand reads a comparison's operand: a term as parseTerm reads it, or an expression. A plain
// term parses exactly as before, so its errors don't move.
func parseOperand(s string) (Term, error) {
	t, err := parseTerm(s)
	if err == nil {
		return t, nil
	}
	p := &exprParser{s: s}
	e, perr := p.expr()
	if perr == nil {
		if p.skip(); p.i < len(p.s) {
			perr = fmt.Errorf("query: expression %q: unexpected %q", strings.TrimSpace(s), p.s[p.i:])
		}
	}
	if perr != nil || e.Expr == nil {
		if perr != nil && strings.ContainsAny(s, "+-*/(") {
			return Term{}, perr
		}
		return Term{}, err
	}
	return e, nil
}

type exprParser struct {
	s string
	i int
}

func (p *exprParser) skip() {
	for p.i < len(p.s) && p.s[p.i] == ' ' {
		p.i++
	}
}

func (p *exprParser) peek() byte {
	if p.skip(); p.i < len(p.s) {
		return p.s[p.i]
	}
	return 0
}

func (p *exprParser) expr() (Term, error) {
	t, err := p.term()
	for err == nil && (p.peek() == '+' || p.peek() == '-') {
		op := string(p.s[p.i])
		p.i++
		var r Term
		if r, err = p.term(); err == nil {
			t = Term{Expr: &Expr{Op: op, Args: []Term{t, r}}}
		}
	}
	return t, err
}

func (p *exprParser) term() (Term, error) {
	t, err := p.unary()
	for err == nil && (p.peek() == '*' || p.peek() == '/') {
		op := string(p.s[p.i])
		p.i++
		var r Term
		if r, err = p.unary(); err == nil {
			t = Term{Expr: &Expr{Op: op, Args: []Term{t, r}}}
		}
	}
	return t, err
}

func (p *exprParser) unary() (Term, error) {
	if p.peek() != '-' {
		return p.primary()
	}
	p.i++
	t, err := p.unary()
	if err != nil {
		return Term{}, err
	}
	if t.Const != nil && t.Const.Num != nil {
		f := -*t.Const.Num
		c := *t.Const
		c.Num, c.S = &f, "-"+c.S
		return Term{Const: &c}, nil
	}
	return Term{Expr: &Expr{Op: "neg", Args: []Term{t}}}, nil
}

func (p *exprParser) primary() (Term, error) {
	switch c := p.peek(); {
	case c == 0:
		return Term{}, fmt.Errorf("query: expression %q ends early", strings.TrimSpace(p.s))
	case c == '(':
		p.i++
		t, err := p.expr()
		if err != nil {
			return Term{}, err
		}
		if p.peek() != ')' {
			return Term{}, fmt.Errorf("query: expression %q: unclosed (", strings.TrimSpace(p.s))
		}
		p.i++
		return t, nil
	case c == '"':
		end := strings.IndexByte(p.s[p.i+1:], '"')
		if end < 0 {
			return Term{}, fmt.Errorf("query: unterminated string %q", p.s[p.i:])
		}
		t := Term{Const: &ns.Value{S: p.s[p.i+1 : p.i+1+end]}}
		p.i += end + 2
		return t, nil
	}
	start := p.i
	for p.i < len(p.s) {
		r, w := utf8.DecodeRuneInString(p.s[p.i:])
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '?') {
			break
		}
		p.i += w
	}
	word := p.s[start:p.i]
	if word == "" {
		return Term{}, fmt.Errorf("query: expression %q: unexpected %q", strings.TrimSpace(p.s), p.s[p.i:])
	}
	if p.peek() == '(' && word[0] != '?' && isRelation(word) {
		p.i++
		var args []Term
		for p.peek() != ')' {
			a, err := p.expr()
			if err != nil {
				return Term{}, err
			}
			args = append(args, a)
			if p.peek() == ',' {
				p.i++
			} else if p.peek() != ')' {
				return Term{}, fmt.Errorf("query: expression %q: want , or ) after %s's argument", strings.TrimSpace(p.s), word)
			}
		}
		p.i++
		return Term{Expr: &Expr{Op: "call", Func: word, Args: args}}, nil
	}
	return parseTerm(word)
}

// lowerExpressions writes each comparison over an expression as calls to the functions it uses,
// in the goal and every rule, before anything else reads the program. ?y = expr with ?y a variable
// computes into ?y itself, so it binds ?y or tests it.
func lowerExpressions(q Query, reg *ns.Vocabulary) (Query, error) {
	q = computeColumns(q)
	if !slices.ContainsFunc(q.Rules, func(r Rule) bool { return hasExpr(r.Body) }) && !hasExpr(q.Goal) {
		return q, nil
	}
	l := &exprLowerer{reg: reg}
	out := q
	out.Rules = make([]Rule, len(q.Rules))
	for i, r := range q.Rules {
		if hasExpr(r.Body) {
			if r.text == "" {
				r.text = r.String()
			}
			body, err := l.body(r.Body)
			if err != nil {
				return Query{}, err
			}
			r.Body = body
		}
		out.Rules[i] = r
	}
	if hasExpr(q.Goal) {
		if len(out.Select) == 0 {
			out.Select = defaultSelect(q.Goal) // the answer's columns are the written goal's
		}
		body, err := l.body(q.Goal)
		if err != nil {
			return Query{}, err
		}
		out.Goal = body
	}
	return out, nil
}

// computeColumns writes arithmetic in a rule head or the projection (#206) as a comparison computing
// it into a variable named for it, which lowerExpressions then lowers like any other: r(?x, ?a * 2) :-
// body is r(?x, ?"a * 2") :- body, ?"a * 2" = ?a * 2, and => ?a * ?b projects ?"a * b", the column's
// label, computed at the end of the goal. No query can spell such a name, and a message about it reads
// as written. A head keeps its written text, so a witness shows the rule as written.
func computeColumns(q Query) Query {
	headExpr := func(r Rule) bool { return slices.ContainsFunc(r.Head.Args, func(t Term) bool { return t.Expr != nil }) }
	selExpr := slices.ContainsFunc(q.Select, func(t Term) bool { return t.Expr != nil })
	if !slices.ContainsFunc(q.Rules, headExpr) && !selExpr {
		return q
	}
	out := q
	out.Rules = slices.Clone(q.Rules)
	for i, r := range out.Rules {
		if !headExpr(r) {
			continue
		}
		if r.text == "" {
			r.text = r.String()
		}
		r.Head.Args = slices.Clone(r.Head.Args)
		r.Body.Literals = slices.Clone(r.Body.Literals)
		for j, t := range r.Head.Args {
			if t.Expr != nil {
				v := Term{Var: colLabel(t)} // an error about it names it as written
				r.Head.Args[j] = v
				r.Body.Literals = append(r.Body.Literals, Literal{Compare: &Compare{Left: v, Op: "=", Right: t}})
			}
		}
		out.Rules[i] = r
	}
	if selExpr {
		out.Select = slices.Clone(q.Select)
		out.Goal.Literals = slices.Clone(q.Goal.Literals)
		for i, t := range out.Select {
			if t.Expr != nil {
				v := Term{Var: colLabel(t)}
				out.Select[i] = v
				out.Goal.Literals = append(out.Goal.Literals, Literal{Compare: &Compare{Left: v, Op: "=", Right: t}})
			}
		}
	}
	return out
}

// exprLabel writes an expression as written, without its variables' ?s: the answer column it names.
func exprLabel(t Term) string {
	switch {
	case t.Expr == nil && t.Var != "":
		return string(t.Var)
	case t.Expr == nil:
		return t.String()
	}
	e := t.Expr
	child := func(c Term) string {
		if c.Expr != nil && c.Expr.Op != "call" && c.Expr.Op != "neg" {
			return "(" + exprLabel(c) + ")"
		}
		return exprLabel(c)
	}
	switch e.Op {
	case "call":
		args := make([]string, len(e.Args))
		for i, a := range e.Args {
			args[i] = exprLabel(a)
		}
		return e.Func + "(" + strings.Join(args, ", ") + ")"
	case "neg":
		return "-" + child(e.Args[0])
	}
	return child(e.Args[0]) + " " + e.Op + " " + child(e.Args[1])
}

// lowerExprRules is lowerExpressions for rules alone, a module's.
func lowerExprRules(rules []Rule, reg *ns.Vocabulary) ([]Rule, error) {
	q, err := lowerExpressions(Query{Rules: rules}, reg)
	return q.Rules, err
}

func hasExpr(b Body) bool {
	for _, l := range b.Literals {
		if c := l.Compare; c != nil && (c.Left.Expr != nil || c.Right.Expr != nil) {
			return true
		}
	}
	return false
}

type exprLowerer struct {
	reg   *ns.Vocabulary
	fresh int
}

func (l *exprLowerer) body(b Body) (Body, error) {
	var out []Literal
	for _, lit := range b.Literals {
		c := lit.Compare
		if c == nil || (c.Left.Expr == nil && c.Right.Expr == nil) {
			out = append(out, lit)
			continue
		}
		var calls []Literal
		left, right := c.Left, c.Right
		switch {
		case c.Op == "=" && left.Expr != nil && right.Var != "" && right.Var != "_":
			left, right = right, left
			fallthrough
		case c.Op == "=" && left.Var != "" && left.Var != "_" && right.Expr != nil:
			if err := l.compute(*right.Expr, left, &calls); err != nil {
				return Body{}, err
			}
			out = append(out, calls...)
			continue
		}
		var err error
		if left, err = l.value(left, &calls); err != nil {
			return Body{}, err
		}
		if right, err = l.value(right, &calls); err != nil {
			return Body{}, err
		}
		cmp := Compare{Left: left, Op: c.Op, Right: right}
		out = append(out, calls...)
		out = append(out, Literal{Compare: &cmp, at: lit.at})
	}
	return Body{Literals: out}, nil
}

// value is a term an operand computes to: the term itself, or a fresh variable its calls bind.
func (l *exprLowerer) value(t Term, calls *[]Literal) (Term, error) {
	if t.Expr == nil {
		return t, nil
	}
	l.fresh++
	v := Term{Var: Var("\x00e" + strconv.Itoa(l.fresh))}
	return v, l.compute(*t.Expr, v, calls)
}

// compute appends the calls that compute e into out.
func (l *exprLowerer) compute(e Expr, out Term, calls *[]Literal) error {
	args := make([]Term, 0, len(e.Args)+1)
	for _, a := range e.Args {
		v, err := l.value(a, calls)
		if err != nil {
			return err
		}
		args = append(args, v)
	}
	fn := e.Func
	if e.Op != "call" {
		fn = exprFuncs[e.Op]
		if _, ok := l.reg.Predicate(fn); !ok {
			return fmt.Errorf("query: arithmetic needs %s, which stdlib.Arithmetic registers", fn)
		}
	}
	*calls = append(*calls, Literal{Pos: &Atom{Relation: fn, Args: append(args, out)}, computed: true})
	return nil
}

// checkComputedRecursion refuses a rule that computes a value with arithmetic and reads its own
// recursive relation: n(?y) :- n(?x), ?y = ?x + 1 has no fixpoint (#5; #207 would allow it under an
// explicit bound).
func checkComputedRecursion(rules []Rule) error {
	reads := map[string][]string{}
	for _, r := range rules {
		for _, a := range ruleAtoms(r.Body) {
			reads[r.Head.Relation] = append(reads[r.Head.Relation], a.Relation)
		}
	}
	reaches := func(from, to string) bool {
		seen := map[string]bool{}
		stack := []string{from}
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if n == to {
				return true
			}
			if seen[n] {
				continue
			}
			seen[n] = true
			stack = append(stack, reads[n]...)
		}
		return false
	}
	for _, r := range rules {
		computes := false
		for _, l := range r.Body.Literals {
			computes = computes || l.computed
		}
		if !computes {
			continue
		}
		for _, a := range ruleAtoms(r.Body) {
			if reaches(a.Relation, r.Head.Relation) {
				return fmt.Errorf("query: rule %q computes with arithmetic and reads %s, which reaches it again, so it might never finish", displayName(r.Head.Relation), displayName(a.Relation))
			}
		}
	}
	return nil
}
