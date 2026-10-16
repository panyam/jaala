package datalog

import (
	"fmt"
	"github.com/panyam/jaala/ns"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Parse reads a text query into a Query. Grammar (EBNF; whitespace is insignificant):
//
//	query       = { rule end } goal [ "." ] ;         (* zero or more rules, then one goal *)
//	end         = ";" | "." ;                         (* a "." only before whitespace or the end, #276 *)
//	rule        = head ":-" literals ;                (* head :- body; defines a derived relation *)
//	head        = relation "(" [ harg { "," harg } ] ")" ;
//	harg        = term [ ":" decl ] | aggregate | expr ; (* a declared type: ?n: net, see parseArgType *)
//	goal        = literals [ "=>" projection [ "having" havings ] [ "order" "by" orders ]
//	              [ "limit" count ] [ "offset" count ] ]
//	            | ( "?-" | "=>" ) literals ;          (* asks literals with no projection, #276 *)
//	literals    = literal { "," literal } ;
//	literal     = atom | ( "not" | "!" ) atom | comparison | bodyagg ; (* "not atom" = stratified negation *)
//	bodyagg     = variable "=" aggregate ":" "{" literals "}" ; (* per shared variable's value, #71 *)
//	atom        = relation "(" [ term { "," term } ] ")" ;
//	comparison  = operand op operand ;
//	operand     = term | expr ;
//	expr        = sum ;                               (* arithmetic over num.* and functions, #5 *)
//	sum         = product { ( "+" | "-" ) product } ;
//	product     = unary { ( "*" | "/" ) unary } ;
//	unary       = "-" unary | "(" expr ")" | call | term ;
//	call        = relation "(" [ expr { "," expr } ] ")" ; (* a function relation; its last argument is the result *)
//	op          = "<" | "<=" | "=" | "!=" | ">" | ">=" ;
//	projection  = column { "," column } ;
//	column      = variable | aggregate | expr ;          (* an expr column is labelled as written without ?s, #206 *)
//	aggregate   = aggfunc "(" [ "distinct" ] variable ")" ; (* grouped by the variable columns *)
//	aggfunc     = "count" | "min" | "max" | "sum" | "list" ;
//	havings     = having { "," having } ;
//	having      = aggregate op term ;                 (* filters GROUPS, after the reduce *)
//	orders      = order { "," order } ;
//	order       = column [ "asc" | "desc" ] ;         (* a selected column; ties keep the default order *)
//	count       = digit { digit } ;
//	term        = variable | string | number ;
//	variable    = "?" ident | "_" ;
//	ident       = ( letter | digit | "_" ) { letter | digit | "_" } ;  (* ASCII *)
//	string      = '"' { char | "\\" char } '"' ;  (* kept as written: \" is a backslash and a quote *)
//	number      = [ "+" | "-" ] digit { digit } [ "." digit { digit } ] [ ( "e" | "E" ) [ "+" | "-" ] digit { digit } ] ;
//	relation    = ident { "." | "-" | ident } ;       (* a path: net.max_voltage, component-on-net *)
//
// A "#" outside a string starts a comment that runs to the end of the line. The text is read as tokens
// first (lex.go, #167), and an error is a *ParseError saying where the piece it is about starts.
//
// Clauses are separated by ";", or ended by "." as Datalog and Prolog end them; a clause with ":-" is
// a rule, and the one clause without one is the goal. A goal may also be written as Prolog and
// agents write it, `?- r(?a)` or `=> r(?a)`, meaning `r(?a)`, and a "." between a goal and its
// "=>" is read as if absent. A rule head defines a derived (IDB) relation the goal (or another rule) can then read, and a
// rule whose body reads its own head is recursion (evaluated to a stratified fixpoint). Some relation
// names are predicates the host registered rather than fact-base relations: reaches(from, net)
// (transitive connectivity) and the string filters str.contains/str.prefix/str.suffix(?value,
// "pattern"). A dotted name is a path in the vocabulary's tree (see ns.Vocabulary). They parse as
// ordinary atoms; the evaluator dispatches them. A rule head may not redefine a built-in or an EDB
// relation.
//
// A comparison in the LITERALS and one after `having` read alike and are applied at different times,
// which is the distinction to hold on to. A literal comparison filters BINDINGS, before any grouping;
// a having filters GROUPS, after the reduce. So `?c < 2` in the body narrows the facts that reach the
// group, and `having count(?n) < 2` narrows the groups the reduce produced. Only the second can ask
// about a count, because before grouping there is nothing to count.
//
// The answer is ordered and cut last: `order by` sorts the rows the having kept, and `limit` and
// `offset` take a page of them. Without `order by` the answer is in the default order (absent values,
// then numbers by value, then strings by text), which is also what breaks a tie under `order by`.
//
// This covers the whole bounded fragment the evaluator serves: user-defined (recursive, stratified)
// rules, conjunction, comparison, the built-in reaches and string predicates, stratified negation,
// aggregation, and post-aggregation filtering.
// It parses to the query.Query IR; for
//
//	component.mpn(?r,"REG-24"), net.max_voltage(?n,?v), ?v < 30 => ?r, ?n
//
// the result is Query{Goal.Literals: [Atom component.mpn(?r,"REG-24"), Atom net.max_voltage(?n,?v),
// Compare(?v < 30)], Select: [?r, ?n]}. And `component-on-net(?r,?n) => ?n, count(?r)` parses to a
// Goal with one Atom and Select [Term{Var:"n"}, Term{Agg: &Aggregate{Func:"count", Var:"r"}}].
func Parse(s string) (Query, error) {
	src := lex(s)
	rules, goals, err := src.clauses()
	if err != nil {
		return Query{}, err
	}
	var parts []span
	switch {
	case len(goals) == 2 && goals[1].is(0, "=>") && goals[0].count("=>") == 0:
		parts = append([]span{goals[0]}, goals[1].split("=>")[1:]...) // `edge(?a, ?b). => ?a`: an ending before the projection (#276)
	case len(goals) == 1:
		parts = goals[0].split("=>")
	case len(goals) == 0:
		return Query{}, src.all().errorf("query: no goal clause (a query needs one clause without %q to ask)", ":-")
	default:
		return Query{}, goals[1].errorf("query: %d goal clauses; a query asks one goal (rules use %q, the goal does not)", len(goals), ":-")
	}
	if len(parts) > 2 {
		return Query{}, parts[2].errorf("query: more than one %q projection separator", "=>")
	}
	body, proj := parts[0], parts[0].tokens(parts[0].hi-parts[0].lo, parts[0].hi-parts[0].lo)
	if len(parts) == 2 {
		proj = parts[1]
	}
	if body.empty() && !proj.empty() {
		if err := goalAfterArrow(proj); err != nil {
			return Query{}, err
		}
		body, proj = proj, proj.tokens(proj.hi-proj.lo, proj.hi-proj.lo)
	}
	lits, err := parseLiterals(body)
	if err != nil {
		return Query{}, err
	}
	tail, err := splitTail(proj)
	if err != nil {
		return Query{}, err
	}
	sel, err := parseSelect(tail[""])
	if err != nil {
		return Query{}, err
	}
	having, err := parseHaving(tail["having"])
	if err != nil {
		return Query{}, err
	}
	q := Query{Rules: rules, Goal: Body{Literals: lits}, Select: sel, Having: having}
	if sp, ok := tail["order"]; ok {
		if q.OrderBy, err = parseOrderBy(sp); err != nil {
			return Query{}, err
		}
	}
	if sp, ok := tail["after"]; ok {
		if q.After, err = parseAfter(sp); err != nil {
			return Query{}, err
		}
	}
	if sp, ok := tail["limit"]; ok {
		if q.Limit, err = parseCount("limit", sp, 1); err != nil {
			return Query{}, err
		}
	}
	if sp, ok := tail["offset"]; ok {
		if q.Offset, err = parseCount("offset", sp, 0); err != nil {
			return Query{}, err
		}
	}
	return q, nil
}

// goalAfterArrow checks a goal written after "=>" with nothing before it, as `=> r(?u, ?l)` (#276),
// which asks for every row of r: the goal r(?u, ?l), whose columns are its variables. Columns with no
// goal before them (`=> ?b, count(?a)`) are refused, saying where the goal goes.
func goalAfterArrow(sp span) error {
	lits, err := parseLiterals(sp)
	columns := err != nil
	for _, l := range lits {
		columns = columns || l.Pos != nil && validAggFunc(l.Pos.Relation)
	}
	if columns {
		return sp.errorf("query: %q has no goal before it; the goal goes first, as in %q", "=> "+sp.text(), "edge(?a, ?b) => ?b, count(?a)")
	}
	return nil
}

// tailWords are the clauses that may follow the projection, in the order they must be written.
var tailWords = []string{"having", "order", "after", "limit", "offset"}

// splitTail cuts the projection at the clauses that follow it, keyed by keyword, with the columns
// themselves under "". A keyword is a word outside brackets with no name beside it, so a variable or
// relation whose name merely contains one (?shaving, ?limit, x.having) is left alone. Each clause may
// appear once, in tailWords' order, and `order` must be followed by `by`, which the "order" entry
// leaves out.
func splitTail(proj span) (map[string]span, error) {
	type cut struct {
		word string
		at   int // where the keyword starts in the text
	}
	var cuts []cut
	text := proj.src.text
	proj.top(func(_ int, t token) {
		if t.kind == tokString || t.kind == tokSymbol {
			return
		}
		for i := t.pos; i < t.end; i++ {
			for _, w := range tailWords {
				if wordAt(text[proj.from:proj.to], i-proj.from, w) {
					cuts = append(cuts, cut{w, i})
					break
				}
			}
		}
	})
	out := map[string]span{}
	for n, c := range cuts {
		kw := proj.slice(c.at, c.at+len(c.word))
		if _, dup := out[c.word]; dup {
			return nil, kw.errorf("query: %s appears twice after %q", clauseName(c.word), "=>")
		}
		if n > 0 && slices.Index(tailWords, c.word) < slices.Index(tailWords, cuts[n-1].word) {
			return nil, kw.errorf("query: %s comes after %s, not before it (the order is having, order by, after, limit, offset)", clauseName(cuts[n-1].word), clauseName(c.word))
		}
		end := proj.to
		if n+1 < len(cuts) {
			end = cuts[n+1].at
		}
		sp := proj.slice(c.at+len(c.word), end)
		if c.word == "order" {
			by := sp.trim()
			if t := by.text() + " "; !strings.HasPrefix(t, "by") || !isSpace(t[2]) {
				return nil, kw.errorf("query: order needs by, as in %q", "order by ?n desc")
			}
			sp = sp.slice(by.from+2, sp.to)
		}
		out[c.word] = sp
	}
	out[""] = proj
	if len(cuts) > 0 {
		out[""] = proj.slice(proj.from, cuts[0].at)
	}
	return out, nil
}

// wordAt reports whether word sits at s[i:] with no name byte (isIdentByte) on either side, so
// "having" matches and "?shaving" and "having_x" do not. It reads bytes, so a keyword beside a letter
// beyond ASCII still stands alone.
func wordAt(s string, i int, word string) bool {
	if !strings.HasPrefix(s[i:], word) || i > 0 && isIdentByte(s[i-1]) {
		return false
	}
	j := i + len(word)
	return j == len(s) || !isIdentByte(s[j])
}

// clauseName is a tail keyword as written: "order" is "order by".
func clauseName(word string) string {
	if word == "order" {
		return "order by"
	}
	return word
}

// parseOrderBy reads the comma-separated sort columns, each a projection column optionally followed
// by asc or desc. Whether it names a selected column is checked with the projection (validateOrder).
func parseOrderBy(sp span) ([]Order, error) {
	var out []Order
	for _, piece := range sp.split(",") {
		piece = piece.trim()
		desc := false
		for _, dir := range []string{"asc", "desc"} {
			if i := len(piece.text()) - len(dir); i > 0 && wordAt(piece.text(), i, dir) {
				piece, desc = piece.slice(piece.from, piece.from+i).trim(), dir == "desc"
				break
			}
		}
		if piece.empty() {
			return nil, piece.errorf("query: order by needs a column, as in %q", "order by ?n desc")
		}
		t, err := parseSelItem(piece)
		if err != nil {
			return nil, within("order by", err)
		}
		out = append(out, Order{Term: t, Desc: desc})
	}
	return out, nil
}

// parseAfter reads an after clause's values, one per order by column, which validateOrder matches up.
func parseAfter(sp span) ([]ns.Value, error) {
	var out []ns.Value
	for _, piece := range sp.split(",") {
		t, err := parseTerm(piece)
		if err != nil {
			return nil, err
		}
		if t.Const == nil {
			return nil, piece.errorf("query: after takes values, as in %q, and %s is not one", `after 812, "db"`, piece.text())
		}
		out = append(out, *t.Const)
	}
	return out, nil
}

// parseCount reads limit's or offset's row count, a whole number no smaller than min.
func parseCount(word string, sp span, min int) (int, error) {
	s := sp.text()
	n, err := strconv.Atoi(s)
	if err != nil || n < min || strings.HasPrefix(s, "+") {
		want := "a whole number"
		if min > 0 {
			want = "a positive whole number"
		}
		return 0, sp.errorf("query: %s %q is not %s", word, s, want)
	}
	return n, nil
}

// parseHaving reads the comma-separated group filters. Each is a comparison whose LEFT side is an
// aggregate: the right side is an ordinary term, so `count(?n) < 2` and `count(?n) = ?limit` both
// parse, and the evaluator rejects the second when nothing binds ?limit per group.
func parseHaving(sp span) ([]Compare, error) {
	if sp.empty() {
		return nil, nil
	}
	var out []Compare
	for _, piece := range sp.split(",") {
		piece = piece.trim()
		if piece.empty() {
			return nil, piece.errorf("query: having needs a comparison, as in %q", "having count(?n) > 1")
		}
		c, err := parseHavingOne(piece)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// parseHavingOne reads one group filter. The left side goes through parseSelItem (the projection's
// term parser, which is the one that knows aggregates) rather than parseTerm, so an aggregate stays
// spellable HERE and stays unspellable in the goal body, where it would have nothing to reduce.
func parseHavingOne(piece span) (Compare, error) {
	for _, op := range compareOps {
		if piece.count(op) != 1 {
			continue
		}
		parts := piece.split(op)
		left, err := parseSelItem(parts[0].trim())
		if err != nil {
			return Compare{}, within(fmt.Sprintf("having %q", piece.text()), err)
		}
		if left.Agg == nil {
			return Compare{}, piece.errorf("query: having %q filters ?%s, which is a group key rather than an aggregate — a comparison over plain variables belongs in the goal, before the %q", piece.text(), left.Var, "=>")
		}
		right, err := parseTerm(parts[1])
		if err != nil {
			return Compare{}, within(fmt.Sprintf("having %q", piece.text()), err)
		}
		return Compare{Left: left, Op: op, Right: right}, nil
	}
	return Compare{}, piece.errorf("query: having %q is not a comparison (want an aggregate, an operator and a value, as in %q)", piece.text(), "count(?n) < 2")
}

// ParseRules reads text that holds rules only, such as a derived module's body (see
// Language). It accepts the same clause syntax as Parse and refuses a goal clause, since
// rules with nothing to ask are a definition rather than a query. Its errors are *ParseError, as
// Parse's are.
func ParseRules(s string) ([]Rule, error) {
	rules, goals, err := lex(s).clauses()
	if err != nil {
		return nil, err
	}
	if len(goals) > 0 {
		return nil, goals[0].errorf("query: %q is a goal; rules-only text defines relations and asks nothing", goals[0].text())
	}
	return rules, nil
}

// clauses separates the text into its rule definitions and its goal clauses. A clause ends at a ";"
// outside brackets, or at a "." before whitespace or the end, the ending Datalog and Prolog write
// (#276): a "." in a path (str.suffix) or a number (1.5) is followed by a letter or digit. A clause
// with ":-" is a rule (head :- body), and every other clause is a goal, parsed by the caller, which
// decides how many it accepts: a query asks exactly one, a module none.
func (src *source) clauses() (rules []Rule, goals []span, err error) {
	all := src.all()
	start := 0
	var clauses []span
	all.top(func(i int, t token) {
		if t.kind == tokSymbol && (t.text == ";" || t.text == "." && t.flag) {
			clauses = append(clauses, all.tokens(start, i))
			start = i + 1
		}
	})
	clauses = append(clauses, all.tokens(start, all.hi))
	for _, clause := range clauses {
		clause = clause.trim()
		if clause.empty() {
			continue
		}
		if t := clause.toks(); t[0].text == "?" && len(t) > 1 && t[1].text == "-" && t[1].pos == t[0].end {
			goals = append(goals, clause.after(1).trim())
			continue
		}
		parts := clause.split(":-")
		switch len(parts) {
		case 1:
			goals = append(goals, clause)
		case 2:
			rule, rerr := parseRule(parts[0], parts[1])
			if rerr != nil {
				return nil, nil, rerr
			}
			rules = append(rules, rule)
		default:
			return nil, nil, clause.errorf("query: clause %q has more than one %q", clause.text(), ":-")
		}
	}
	return rules, goals, nil
}

// parseRule parses one "head :- body" clause into a Rule. The head is a single atom; the body is a
// conjunction of literals, the same grammar a goal body uses.
func parseRule(headSp, bodySp span) (Rule, error) {
	head, types, err := parseHead(headSp)
	if err != nil {
		return Rule{}, err
	}
	if at := bodySp.occurrences("=>"); len(at) > 0 {
		return Rule{}, bodySp.cut(at[0]).errorf("query: the rule for %s holds %q; end the rule with %q or %q, then write the goal", head.Relation, "=>", ";", ".")
	}
	lits, err := parseLiterals(bodySp)
	if err != nil {
		return Rule{}, err
	}
	return Rule{Head: head, Body: Body{Literals: lits}, HeadTypes: types}, nil
}

// parseHead parses a rule head, whose arguments may declare their types: `x(?n: net, ?v)`. The
// declarations come back by position, nil when the head declares none. A KindFrom or Owner must name
// another variable of the same head, since a type can only point at something the relation carries.
// Every declaration is read before the relation and its arguments, so a bad one is reported first.
func parseHead(sp span) (Atom, []ns.ArgType, error) {
	name, inner, ok := sp.parens()
	if !ok || !strings.Contains(sp.raw(), ":") {
		a, err := parseHeadAtom(sp)
		return a, nil, err
	}
	var plain []span
	var types []ns.ArgType
	declared := false
	for _, a := range inner.split(",") {
		if a.empty() {
			return Atom{}, nil, a.errorf("query: empty term")
		}
		var t ns.ArgType
		if parts := a.split(":"); len(parts) == 2 {
			var err error
			if t, err = parseArgType(parts[1]); err != nil {
				return Atom{}, nil, parts[1].wrap(err)
			}
			v := parts[0].trim()
			if text := v.text(); len(text) < 2 || text[0] != '?' || text == "?_" || isExpression(v) {
				return Atom{}, nil, v.errorf("query: only a ?variable can declare a type, not %q", text)
			}
			a, declared = v, true
		}
		plain = append(plain, a)
		types = append(types, t)
	}
	head, err := atomOver(name, plain, headTerm)
	if err != nil || !declared {
		return head, nil, err
	}
	for i, t := range types {
		for _, ref := range []string{t.KindFrom, t.Owner} {
			if ref == "" {
				continue
			}
			if ref == string(head.Args[i].Var) || !slices.ContainsFunc(head.Args, func(a Term) bool { return string(a.Var) == ref }) {
				return Atom{}, nil, sp.errorf("query: %s's type %q must name another variable of its head", head.Relation, t)
			}
		}
	}
	return head, types, nil
}

func parseLiterals(body span) ([]Literal, error) {
	if body.empty() {
		return nil, body.errorf("query: empty query")
	}
	var lits []Literal
	for _, piece := range body.split(",") {
		piece = piece.trim()
		if piece.empty() {
			return nil, piece.errorf("query: empty literal")
		}
		lit, err := parseLiteral(piece)
		if err != nil {
			return nil, err
		}
		lits = append(lits, lit)
	}
	return lits, nil
}

// parseLiteral parses one literal: a "not R(...)" or "!R(...)" is a negated atom, `?v = func(?x) : {
// body }` an aggregate over a body of its own, another parenthesised piece a positive atom, anything
// else a comparison.
func parseLiteral(s span) (Literal, error) {
	if parts := s.split(":"); len(parts) == 2 && parts[1].is(0, "{") {
		return parseBodyAggregate(parts[0], parts[1])
	}
	var rest span
	isNeg := false
	switch t := s.first(); {
	case t.kind == tokWord && t.text == "not" && s.hi-s.lo > 1 && s.src.text[t.end] == ' ':
		rest, isNeg = s.after(0).trim(), true
	case t.kind == tokSymbol && t.text == "!":
		rest, isNeg = s.after(0).trim(), true
	case t.kind == tokSymbol && t.text == "!=":
		rest, isNeg = s.cut(t.pos+1).trim(), true
	}
	if isNeg {
		atom, err := parseAtom(rest)
		if err != nil {
			return Literal{}, err
		}
		return Literal{Neg: &atom}, nil
	}
	if strings.Contains(s.raw(), "(") && !hasTopComparison(s) {
		atom, err := parseAtom(s)
		if err != nil {
			return Literal{}, err
		}
		return Literal{Pos: &atom}, nil
	}
	cmp, err := parseComparison(s)
	if err != nil {
		return Literal{}, err
	}
	return Literal{Compare: &cmp}, nil
}

// parseBodyAggregate parses `?v = func([distinct] ?x) : { body }`, split at its top-level colon.
func parseBodyAggregate(left, right span) (Literal, error) {
	right = right.trim()
	if !right.is(right.hi-right.lo-1, "}") {
		return Literal{}, right.errorf("query: an aggregate's body %q must close with }", right.text())
	}
	inner := right.tokens(1, right.hi-right.lo-1)
	left = left.trim()
	if left.count("=") != 1 {
		return Literal{}, left.errorf("query: %q must name the variable an aggregate binds, as ?v = count(?x) : { ... }", left.text())
	}
	sides := left.split("=")
	v, err := parseTerm(sides[0])
	if err != nil {
		return Literal{}, err
	}
	if v.Var == "" || v.Var == "_" {
		return Literal{}, sides[0].errorf("query: an aggregate binds a ?variable, not %s", v)
	}
	fn := sides[1].trim()
	if !strings.Contains(fn.raw(), "(") {
		return Literal{}, fn.errorf("query: %q is not an aggregate, as count(?x)", fn.text())
	}
	agg, err := parseAggregate(fn)
	if err != nil {
		return Literal{}, err
	}
	if inner.empty() {
		return Literal{}, inner.errorf("query: the aggregate binding ?%s has an empty body", v.Var)
	}
	lits, err := parseLiterals(inner)
	if err != nil {
		return Literal{}, err
	}
	return Literal{Agg: &BodyAggregate{Result: v.Var, Agg: *agg.Agg, Body: Body{Literals: lits}}}, nil
}

func parseAtom(s span) (Atom, error) { return parseAtomArgs(s, parseTerm) }

// parseHeadAtom parses a rule head, whose arguments may also be aggregates: degree(?n, count(?m)).
func parseHeadAtom(s span) (Atom, error) { return parseAtomArgs(s, headTerm) }

func headTerm(a span) (Term, error) {
	a = a.trim()
	if text := a.text(); text != "" && text[0] != '"' && strings.Contains(text, "(") && !isExpression(a) {
		return parseAggregate(a)
	}
	if isExpression(a) {
		return parseOperand(a)
	}
	return parseTerm(a)
}

// isExpression reports whether a head argument or a projection column is arithmetic (#206) rather
// than a term or an aggregate. A whole column name(...) with an undotted name is an aggregate's
// syntax, so avg(?m) and count(0) still fail as aggregates; a dotted call, num.add(?r, 1), or an
// operator over terms is arithmetic.
func isExpression(s span) bool {
	s = s.trim()
	if _, err := parseTerm(s); err == nil {
		return false
	}
	t, err := parseOperand(s)
	if err != nil || t.Expr == nil {
		return false
	}
	return t.Expr.Op != "call" || strings.Contains(t.Expr.Func, ".")
}

// parseAtomArgs parses relation(args), the relation being the text before the first "(".
func parseAtomArgs(s span, term func(span) (Term, error)) (Atom, error) {
	name, inner, ok := s.parens()
	if !ok {
		return Atom{}, s.errorf("query: malformed atom %q (want reln(args))", s.raw())
	}
	return atomOver(name, inner.split(","), term)
}

// parens cuts name(inner) at its first "(" and last ")". They are found in the text, so a stray quote
// (r(v1", ?b)) leaves the brackets where the writer put them, and the string it opens is what's
// refused, inside the arguments; a "(" or ")" inside a string ends up in a name or an argument, which
// is refused too.
func (s span) parens() (name, inner span, ok bool) {
	t := s.trim()
	open := strings.IndexByte(t.raw(), '(')
	if open < 0 || !strings.HasSuffix(t.raw(), ")") {
		return span{}, span{}, false
	}
	return t.slice(t.from, t.from+open), t.slice(t.from+open+1, t.to-1), true
}

// atomOver makes the atom of the relation relSp names over args.
func atomOver(relSp span, args []span, term func(span) (Term, error)) (Atom, error) {
	rel := relSp.text()
	if !isRelation(rel) {
		return Atom{}, relSp.errorf("query: bad relation name %q", rel)
	}
	if len(args) == 1 && args[0].empty() {
		return Atom{Relation: rel}, nil
	}
	var out []Term
	for _, a := range args {
		t, err := term(a)
		if err != nil {
			return Atom{}, err
		}
		out = append(out, t)
	}
	return Atom{Relation: rel, Args: out}, nil
}

var compareOps = []string{"<=", ">=", "!=", "<", ">", "="} // longest first

// hasTopComparison reports whether s has one comparison operator outside brackets, so ?y = f(?x) is
// a comparison over a call rather than an atom.
func hasTopComparison(s span) bool {
	for _, op := range compareOps {
		if s.count(op) == 1 {
			return true
		}
	}
	return false
}

// parseComparison splits s at the first operator, longest first, that occurs exactly once outside
// brackets. Counting the operators inside longer ones too means that one is always a whole symbol.
func parseComparison(s span) (Compare, error) {
	for _, op := range compareOps {
		if s.count(op) != 1 {
			continue
		}
		parts := s.split(op)
		l, err := parseOperand(parts[0])
		if err != nil {
			return Compare{}, err
		}
		r, err := parseOperand(parts[1])
		if err != nil {
			return Compare{}, err
		}
		return Compare{Left: l, Op: op, Right: r}, nil
	}
	return Compare{}, s.errorf("query: %q is neither an atom nor a comparison", s.text())
}

// parseTerm reads a term: one ?variable, _, string or number token, a number possibly signed. Anything
// else is refused, saying what it reads as (termError).
func parseTerm(s span) (Term, error) {
	s = s.trim()
	t := s.toks()
	switch {
	case len(t) == 1 && t[0].kind == tokVar && len(t[0].text) > 1:
		return Term{Var: Var(t[0].text[1:])}, nil
	case len(t) == 1 && t[0].kind == tokWord && t[0].text == "_":
		return Term{Var: "_"}, nil
	case len(t) == 1 && t[0].kind == tokString && t[0].flag:
		return Term{Const: &ns.Value{S: t[0].text[1 : len(t[0].text)-1]}}, nil
	case len(t) == 1 && t[0].kind == tokNumber:
		if c, ok := numberConst("", t[0]); ok {
			return *c, nil
		}
	case len(t) == 2 && t[1].kind == tokNumber && t[1].pos == t[0].end && (s.is(0, "-") || s.is(0, "+")):
		if c, ok := numberConst(t[0].text, t[1]); ok {
			return *c, nil
		}
	}
	return Term{}, termError(s)
}

// termError says why a span is no term, by what its text starts with.
func termError(s span) error {
	text := s.text()
	switch {
	case text == "":
		return s.errorf("query: empty term")
	case text[0] == '?':
		if err := checkVarName(text[1:]); err != nil {
			return s.errorf("%s", err.Error())
		}
	case text[0] == '"':
		if len(text) < 2 || text[len(text)-1] != '"' || stringEnd(text[1:]) < 0 {
			return s.errorf("query: unterminated string %q", text)
		}
		return s.errorf("query: %s is more than one string; put a backslash before a quote inside a string", text)
	case isWord(text):
		return s.errorf("query: bare identifier %q — a term must be a ?variable, a \"string\", or a number; write ?%s for a variable or %q for text", text, text, text)
	}
	return s.errorf("query: bare identifier %q — a term must be a ?variable, a \"string\", or a number", text)
}

// parseSelect parses the projection columns: each is a ?variable (a group key), an aggregate
// func(?variable), func in {count,min,max,sum,list}, or arithmetic over variables (#206).
func parseSelect(proj span) ([]Term, error) {
	if proj.empty() {
		return nil, nil
	}
	var sel []Term
	for _, p := range proj.split(",") {
		p = p.trim()
		if p.empty() {
			return nil, p.errorf("query: the projection has an empty column, as in %q", "=> ?a, ?b")
		}
		t, err := parseSelItem(p)
		if err != nil {
			return nil, err
		}
		sel = append(sel, t)
	}
	return sel, nil
}

func parseSelItem(p span) (Term, error) {
	if isExpression(p) {
		return parseOperand(p)
	}
	text := p.text()
	if strings.Contains(text, "(") {
		return parseAggregate(p)
	}
	if text == "" || text[0] != '?' || len(text) == 1 {
		return Term{}, p.errorf("query: projection column %q must be a ?variable or an aggregate", text)
	}
	if t := p.toks(); len(t) == 1 && t[0].kind == tokVar {
		return Term{Var: Var(text[1:])}, nil
	}
	return Term{}, termError(p)
}

// checkVarName refuses a variable name that isn't an ident (#163): letters, digits and _, so `?)(`
// is an error rather than a variable named ")(".
func checkVarName(name string) error {
	if name == "" {
		return fmt.Errorf("query: empty variable name")
	}
	for i := 0; i < len(name); i++ {
		if !isNameByte(name[i]) {
			return fmt.Errorf("query: variable ?%s: a variable's name is letters, digits and _", name)
		}
	}
	return nil
}

// isWord reports whether s is a name a variable or a text could spell: a letter, then letters, digits
// and _. inf and nan aren't, since they read as numbers that #163 refuses.
func isWord(s string) bool {
	if s == "" || !unicode.IsLetter(rune(s[0])) || checkVarName(s) != nil {
		return false
	}
	_, err := strconv.ParseFloat(s, 64)
	return err != nil
}

// parseAggregate parses func([distinct] ?x), the form an aggregate takes in a projection, a having
// and a rule head. The function is the text before the "(", checked when the query is (validAggFunc).
func parseAggregate(p span) (Term, error) {
	p = p.trim()
	name, inner, ok := p.parens()
	if !ok || strings.Contains(name.text(), `"`) {
		return Term{}, p.errorf("query: malformed aggregate %q", p.text())
	}
	fn := name.text()
	inner = inner.trim()
	distinct := false
	if t := inner.toks(); len(t) > 1 && t[0].kind == tokWord && t[0].text == "distinct" && isSpace(p.src.text[t[0].end]) {
		distinct, inner = true, inner.after(0).trim()
	}
	text := inner.text()
	if len(text) < 2 || text[0] != '?' {
		return Term{}, inner.errorf("query: aggregate %s(...) expects a ?variable, got %q", fn, text)
	}
	if t := inner.toks(); len(t) != 1 || t[0].kind != tokVar {
		return Term{}, termError(inner)
	}
	return Term{Agg: &Aggregate{Func: fn, Var: Var(text[1:]), Distinct: distinct}}, nil
}

// isRelation reports whether name is a valid relation identifier (letters, digits, '.', '-', '_').
func isRelation(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
