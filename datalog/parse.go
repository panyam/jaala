package datalog

import (
	"fmt"
	"github.com/panyam/jaala/ns"
	"slices"
	"strconv"
	"strings"
)

// Parse reads a text query into a Query. Grammar (EBNF; whitespace is insignificant):
//
//	query       = { rule ";" } goal ;                 (* zero or more rules, then one goal *)
//	rule        = head ":-" literals ;                (* head :- body; defines a derived relation *)
//	head        = relation "(" [ harg { "," harg } ] ")" ;
//	harg        = term [ ":" decl ] ;                 (* a declared type: ?n: net, see parseArgType *)
//	goal        = literals [ "=>" projection [ "having" havings ] [ "order" "by" orders ]
//	              [ "limit" count ] [ "offset" count ] ] ;
//	literals    = literal { "," literal } ;
//	literal     = atom | "not" atom | comparison | bodyagg ; (* "not atom" = stratified negation *)
//	bodyagg     = variable "=" aggregate ":" "{" literals "}" ; (* per shared variable's value, #71 *)
//	atom        = relation "(" [ term { "," term } ] ")" ;
//	comparison  = term op term ;
//	op          = "<" | "<=" | "=" | "!=" | ">" | ">=" ;
//	projection  = column { "," column } ;
//	column      = variable | aggregate ;
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
//	string      = '"' { char } '"' ;
//	number      = [ "+" | "-" ] digit { digit } [ "." digit { digit } ] [ ( "e" | "E" ) [ "+" | "-" ] digit { digit } ] ;
//	relation    = ident { "." | "-" | ident } ;       (* a path: net.max_voltage, component-on-net *)
//
// A "#" outside a string starts a comment that runs to the end of the line.
//
// Clauses are separated by ";"; a clause with ":-" is a rule, and the one clause without one is the
// goal. A rule head defines a derived (IDB) relation the goal (or another rule) can then read, and a
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
	rules, goals, err := splitClauses(stripComments(s))
	if err != nil {
		return Query{}, err
	}
	var goalText string
	switch len(goals) {
	case 1:
		goalText = goals[0]
	case 0:
		return Query{}, fmt.Errorf("query: no goal clause (a query needs one clause without %q to ask)", ":-")
	default:
		return Query{}, fmt.Errorf("query: %d goal clauses; a query asks one goal (rules use %q, the goal does not)", len(goals), ":-")
	}
	body, proj, err := splitProjection(goalText)
	if err != nil {
		return Query{}, err
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
	if text, ok := tail["order"]; ok {
		if q.OrderBy, err = parseOrderBy(text); err != nil {
			return Query{}, err
		}
	}
	if text, ok := tail["limit"]; ok {
		if q.Limit, err = parseCount("limit", text, 1); err != nil {
			return Query{}, err
		}
	}
	if text, ok := tail["offset"]; ok {
		if q.Offset, err = parseCount("offset", text, 0); err != nil {
			return Query{}, err
		}
	}
	return q, nil
}

// tailWords are the clauses that may follow the projection, in the order they must be written.
var tailWords = []string{"having", "order", "limit", "offset"}

// splitTail cuts the projection at the clauses that follow it, keyed by keyword, with the columns
// themselves under "". A keyword matches as a bare word only, at paren depth zero, outside quotes and
// bounded on both sides, so a variable whose name merely contains one (?shaving, ?limit) is left
// alone. Each clause may appear once, in tailWords' order, and `order` must be followed by `by`,
// which the "order" entry leaves out.
func splitTail(proj string) (map[string]string, error) {
	type cut struct {
		word  string
		start int // where the keyword starts
		body  int // where its text starts
	}
	var cuts []cut
	depth, inQuote := 0, false
	for i := 0; i < len(proj); i++ {
		switch c := proj[i]; {
		case c == '"':
			inQuote = !inQuote
		case inQuote:
		case c == '(' || c == '{':
			depth++
		case c == ')' || c == '}':
			depth--
		case depth == 0:
			for _, w := range tailWords {
				if isWordAt(proj, i, w) {
					cuts = append(cuts, cut{w, i, i + len(w)})
					break
				}
			}
		}
	}
	out := map[string]string{}
	for n, c := range cuts {
		if _, dup := out[c.word]; dup {
			return nil, fmt.Errorf("query: %s appears twice after %q", clauseName(c.word), "=>")
		}
		if n > 0 && slices.Index(tailWords, c.word) < slices.Index(tailWords, cuts[n-1].word) {
			return nil, fmt.Errorf("query: %s comes after %s, not before it (the order is having, order by, limit, offset)", clauseName(cuts[n-1].word), clauseName(c.word))
		}
		end := len(proj)
		if n+1 < len(cuts) {
			end = cuts[n+1].start
		}
		text := proj[c.body:end]
		if c.word == "order" {
			rest, ok := cutWord(strings.TrimSpace(text)+" ", "by")
			if !ok {
				return nil, fmt.Errorf("query: order needs by, as in %q", "order by ?n desc")
			}
			text = rest
		}
		out[c.word] = text
	}
	out[""] = proj
	if len(cuts) > 0 {
		out[""] = proj[:cuts[0].start]
	}
	return out, nil
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
func parseOrderBy(s string) ([]Order, error) {
	var out []Order
	for _, piece := range splitTop(s, ",") {
		piece = strings.TrimSpace(piece)
		desc := false
		for _, dir := range []string{"asc", "desc"} {
			if i := len(piece) - len(dir); i > 0 && isWordAt(piece, i, dir) {
				piece, desc = strings.TrimSpace(piece[:i]), dir == "desc"
				break
			}
		}
		if piece == "" {
			return nil, fmt.Errorf("query: order by needs a column, as in %q", "order by ?n desc")
		}
		t, err := parseSelItem(piece)
		if err != nil {
			return nil, fmt.Errorf("query: order by: %w", err)
		}
		out = append(out, Order{Term: t, Desc: desc})
	}
	return out, nil
}

// parseCount reads limit's or offset's row count, a whole number no smaller than min.
func parseCount(word, s string, min int) (int, error) {
	s = strings.TrimSpace(s)
	n, err := strconv.Atoi(s)
	if err != nil || n < min || strings.HasPrefix(s, "+") {
		want := "a whole number"
		if min > 0 {
			want = "a positive whole number"
		}
		return 0, fmt.Errorf("query: %s %q is not %s", word, s, want)
	}
	return n, nil
}

// isWordAt reports whether word sits at s[i:] with a non-identifier character (or the string edge) on
// either side, so "having" matches and "?shaving" and "having_x" do not.
func isWordAt(s string, i int, word string) bool {
	if !strings.HasPrefix(s[i:], word) {
		return false
	}
	if i > 0 && isIdentByte(s[i-1]) {
		return false
	}
	if j := i + len(word); j < len(s) && isIdentByte(s[j]) {
		return false
	}
	return true
}

// cutWord strips a leading keyword and the whitespace after it, reporting whether it was there. It
// requires the whitespace, so `distinct ?x` is the modifier and a variable literally named
// `?distinctxyz` is not mistaken for one.
func cutWord(s, word string) (rest string, ok bool) {
	if !strings.HasPrefix(s, word) {
		return s, false
	}
	rest = s[len(word):]
	if rest == "" || !isSpaceByte(rest[0]) {
		return s, false
	}
	return rest, true
}

func isSpaceByte(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func isIdentByte(c byte) bool {
	return c == '_' || c == '?' || c == '.' || c == '-' ||
		(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// parseHaving reads the comma-separated group filters. Each is a comparison whose LEFT side is an
// aggregate: the right side is an ordinary term, so `count(?n) < 2` and `count(?n) = ?limit` both
// parse, and the evaluator rejects the second when nothing binds ?limit per group.
func parseHaving(s string) ([]Compare, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []Compare
	for _, piece := range splitTop(s, ",") {
		piece = strings.TrimSpace(piece)
		if piece == "" {
			return nil, fmt.Errorf("query: having needs a comparison, as in %q", "having count(?n) > 1")
		}
		c, err := parseHavingOne(piece)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

type havingParseError struct {
	piece string
	cause error
}

func (e *havingParseError) Error() string {
	return fmt.Sprintf("query: having %q: %s", e.piece, strings.TrimPrefix(e.cause.Error(), "query: "))
}

func (e *havingParseError) Unwrap() error { return e.cause }

// parseHavingOne reads one group filter. The left side goes through parseSelItem (the projection's
// term parser, which is the one that knows aggregates) rather than parseTerm, so an aggregate stays
// spellable HERE and stays unspellable in the goal body, where it would have nothing to reduce.
func parseHavingOne(piece string) (Compare, error) {
	for _, op := range compareOps {
		parts := splitTop(piece, op)
		if len(parts) != 2 {
			continue
		}
		left, err := parseSelItem(strings.TrimSpace(parts[0]))
		if err != nil {
			return Compare{}, &havingParseError{piece: piece, cause: err}
		}
		if left.Agg == nil {
			return Compare{}, fmt.Errorf("query: having %q filters ?%s, which is a group key rather than an aggregate — a comparison over plain variables belongs in the goal, before the %q", piece, left.Var, "=>")
		}
		right, err := parseTerm(parts[1])
		if err != nil {
			return Compare{}, &havingParseError{piece: piece, cause: err}
		}
		return Compare{Left: left, Op: op, Right: right}, nil
	}
	return Compare{}, fmt.Errorf("query: having %q is not a comparison (want an aggregate, an operator and a value, as in %q)", piece, "count(?n) < 2")
}

// ParseRules reads text that holds rules only, such as a derived module's body (see
// Language). It accepts the same clause syntax as Parse and refuses a goal clause, since
// rules with nothing to ask are a definition rather than a query.
func ParseRules(s string) ([]Rule, error) {
	rules, goals, err := splitClauses(stripComments(s))
	if err != nil {
		return nil, err
	}
	if len(goals) > 0 {
		return nil, fmt.Errorf("query: %q is a goal; rules-only text defines relations and asks nothing", goals[0])
	}
	return rules, nil
}

// stripComments blanks every "#" comment, from the "#" to the end of its line, unless the "#" sits
// inside a string. Blanking rather than deleting keeps the clause text otherwise intact.
func stripComments(s string) string {
	if !strings.Contains(s, "#") {
		return s
	}
	b := []byte(s)
	inQuote, inComment := false, false
	for i, c := range b {
		switch {
		case inComment:
			if c == '\n' {
				inComment = false
			} else {
				b[i] = ' '
			}
		case c == '"':
			inQuote = !inQuote
		case c == '#' && !inQuote:
			inComment = true
			b[i] = ' '
		}
	}
	return string(b)
}

// splitClauses separates text into its rule definitions and its goal clauses. Clauses are separated
// by ";"; a clause containing ":-" is a rule (head :- body), and every other clause is a goal. The
// caller decides how many goals it accepts: a query asks exactly one, a module none.
func splitClauses(s string) (rules []Rule, goals []string, err error) {
	for _, clause := range splitTop(s, ";") {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		parts := splitTop(clause, ":-")
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
			return nil, nil, fmt.Errorf("query: clause %q has more than one %q", clause, ":-")
		}
	}
	return rules, goals, nil
}

// parseRule parses one "head :- body" clause into a Rule. The head is a single atom; the body is a
// conjunction of literals, the same grammar a goal body uses.
func parseRule(headText, bodyText string) (Rule, error) {
	head, types, err := parseHead(headText)
	if err != nil {
		return Rule{}, err
	}
	lits, err := parseLiterals(bodyText)
	if err != nil {
		return Rule{}, err
	}
	return Rule{Head: head, Body: Body{Literals: lits}, HeadTypes: types}, nil
}

// parseHead parses a rule head, whose arguments may declare their types: `x(?n: net, ?v)`. The
// declarations come back by position, nil when the head declares none. A KindFrom or Owner must name
// another variable of the same head, since a type can only point at something the relation carries.
func parseHead(s string) (Atom, []ns.ArgType, error) {
	open := strings.IndexByte(s, '(')
	if open < 0 || !strings.HasSuffix(strings.TrimSpace(s), ")") || !strings.Contains(s, ":") {
		a, err := parseHeadAtom(s)
		return a, nil, err
	}
	inner := strings.TrimSpace(s)
	inner = inner[open+1 : len(inner)-1]
	var plain []string
	var types []ns.ArgType
	declared := false
	for _, a := range splitTop(inner, ",") {
		if strings.TrimSpace(a) == "" {
			return Atom{}, nil, fmt.Errorf("query: empty term")
		}
		var t ns.ArgType
		if parts := splitTop(a, ":"); len(parts) == 2 {
			var err error
			if t, err = parseArgType(parts[1]); err != nil {
				return Atom{}, nil, err
			}
			if v := strings.TrimSpace(parts[0]); len(v) < 2 || v[0] != '?' || v == "?_" {
				return Atom{}, nil, fmt.Errorf("query: only a ?variable can declare a type, not %q", v)
			}
			a, declared = parts[0], true
		}
		plain = append(plain, a)
		types = append(types, t)
	}
	head, err := parseHeadAtom(strings.TrimSpace(s[:open]) + "(" + strings.Join(plain, ",") + ")")
	if err != nil || !declared {
		return head, nil, err
	}
	for i, t := range types {
		for _, ref := range []string{t.KindFrom, t.Owner} {
			if ref == "" {
				continue
			}
			if ref == string(head.Args[i].Var) || !slices.ContainsFunc(head.Args, func(a Term) bool { return string(a.Var) == ref }) {
				return Atom{}, nil, fmt.Errorf("query: %s's type %q must name another variable of its head", head.Relation, t)
			}
		}
	}
	return head, types, nil
}

// splitProjection splits a query on the top-level "=>" into body and projection (the projection
// empty when absent). "=>" is unambiguous — no comparison operator is "=>".
func splitProjection(s string) (body, proj string, err error) {
	parts := splitTop(s, "=>")
	switch len(parts) {
	case 1:
		return parts[0], "", nil
	case 2:
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("query: more than one %q projection separator", "=>")
	}
}

func parseLiterals(body string) ([]Literal, error) {
	if strings.TrimSpace(body) == "" {
		return nil, fmt.Errorf("query: empty query")
	}
	var lits []Literal
	for _, piece := range splitTop(body, ",") {
		piece = strings.TrimSpace(piece)
		if piece == "" {
			return nil, fmt.Errorf("query: empty literal")
		}
		lit, err := parseLiteral(piece)
		if err != nil {
			return nil, err
		}
		lits = append(lits, lit)
	}
	return lits, nil
}

// parseLiteral parses one literal: a "not R(...)" is a negated atom, `?v = func(?x) : { body }` an
// aggregate over a body of its own, another parenthesised piece a positive atom, anything else a
// comparison.
func parseLiteral(s string) (Literal, error) {
	if parts := splitTop(s, ":"); len(parts) == 2 && strings.HasPrefix(strings.TrimSpace(parts[1]), "{") {
		return parseBodyAggregate(parts[0], parts[1])
	}
	if rest, isNeg := strings.CutPrefix(strings.TrimSpace(s), "not "); isNeg {
		atom, err := parseAtom(strings.TrimSpace(rest))
		if err != nil {
			return Literal{}, err
		}
		return Literal{Neg: &atom}, nil
	}
	if strings.Contains(s, "(") {
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
func parseBodyAggregate(left, right string) (Literal, error) {
	right = strings.TrimSpace(right)
	if !strings.HasSuffix(right, "}") {
		return Literal{}, fmt.Errorf("query: an aggregate's body %q must close with }", right)
	}
	inner := right[1 : len(right)-1]
	sides := splitTop(left, "=")
	if len(sides) != 2 {
		return Literal{}, fmt.Errorf("query: %q must name the variable an aggregate binds, as ?v = count(?x) : { ... }", strings.TrimSpace(left))
	}
	v, err := parseTerm(sides[0])
	if err != nil {
		return Literal{}, err
	}
	if v.Var == "" || v.Var == "_" {
		return Literal{}, fmt.Errorf("query: an aggregate binds a ?variable, not %s", v)
	}
	fn := strings.TrimSpace(sides[1])
	if strings.IndexByte(fn, '(') < 0 {
		return Literal{}, fmt.Errorf("query: %q is not an aggregate, as count(?x)", fn)
	}
	agg, err := parseAggregate(fn)
	if err != nil {
		return Literal{}, err
	}
	if strings.TrimSpace(inner) == "" {
		return Literal{}, fmt.Errorf("query: the aggregate binding ?%s has an empty body", v.Var)
	}
	lits, err := parseLiterals(inner)
	if err != nil {
		return Literal{}, err
	}
	return Literal{Agg: &BodyAggregate{Result: v.Var, Agg: *agg.Agg, Body: Body{Literals: lits}}}, nil
}

func parseAtom(s string) (Atom, error) { return parseAtomArgs(s, parseTerm) }

// parseHeadAtom parses a rule head, whose arguments may also be aggregates: degree(?n, count(?m)).
func parseHeadAtom(s string) (Atom, error) {
	return parseAtomArgs(s, func(a string) (Term, error) {
		if a = strings.TrimSpace(a); a != "" && a[0] != '"' && strings.IndexByte(a, '(') >= 0 {
			return parseAggregate(a)
		}
		return parseTerm(a)
	})
}

func parseAtomArgs(s string, term func(string) (Term, error)) (Atom, error) {
	open := strings.IndexByte(s, '(')
	if open < 0 || !strings.HasSuffix(strings.TrimSpace(s), ")") {
		return Atom{}, fmt.Errorf("query: malformed atom %q (want reln(args))", s)
	}
	rel := strings.TrimSpace(s[:open])
	if !isRelation(rel) {
		return Atom{}, fmt.Errorf("query: bad relation name %q", rel)
	}
	inner := strings.TrimSpace(s)
	inner = inner[open+1 : len(inner)-1]
	if strings.TrimSpace(inner) == "" {
		return Atom{Relation: rel}, nil
	}
	var args []Term
	for _, a := range splitTop(inner, ",") {
		t, err := term(a)
		if err != nil {
			return Atom{}, err
		}
		args = append(args, t)
	}
	return Atom{Relation: rel, Args: args}, nil
}

var compareOps = []string{"<=", ">=", "!=", "<", ">", "="} // longest first

func parseComparison(s string) (Compare, error) {
	for _, op := range compareOps {
		if parts := splitTop(s, op); len(parts) == 2 {
			l, err := parseTerm(parts[0])
			if err != nil {
				return Compare{}, err
			}
			r, err := parseTerm(parts[1])
			if err != nil {
				return Compare{}, err
			}
			return Compare{Left: l, Op: op, Right: r}, nil
		}
	}
	return Compare{}, fmt.Errorf("query: %q is neither an atom nor a comparison", strings.TrimSpace(s))
}

func parseTerm(s string) (Term, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return Term{}, fmt.Errorf("query: empty term")
	case s == "_":
		return Term{Var: "_"}, nil
	case s[0] == '?':
		name := s[1:]
		if err := checkVarName(name); err != nil {
			return Term{}, err
		}
		return Term{Var: Var(name)}, nil
	case s[0] == '"':
		if len(s) < 2 || s[len(s)-1] != '"' {
			return Term{}, fmt.Errorf("query: unterminated string %q", s)
		}
		return Term{Const: &ns.Value{S: s[1 : len(s)-1]}}, nil
	default:
		if f, err := strconv.ParseFloat(s, 64); err == nil && isDecimal(s) {
			return Term{Const: &ns.Value{S: s, Num: &f}}, nil
		}
		return Term{}, fmt.Errorf("query: bare identifier %q — a term must be a ?variable, a \"string\", or a number", s)
	}
}

// parseSelect parses the projection columns: each is a ?variable (a group key) or an aggregate
// func(?variable), func in {count,min,max,sum}.
func parseSelect(proj string) ([]Term, error) {
	proj = strings.TrimSpace(proj)
	if proj == "" {
		return nil, nil
	}
	var sel []Term
	for _, p := range splitTop(proj, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("query: the projection has an empty column, as in %q", "=> ?a, ?b")
		}
		t, err := parseSelItem(p)
		if err != nil {
			return nil, err
		}
		sel = append(sel, t)
	}
	return sel, nil
}

func parseSelItem(p string) (Term, error) {
	if strings.IndexByte(p, '(') >= 0 {
		return parseAggregate(p)
	}
	if p == "" || p[0] != '?' || len(p) == 1 {
		return Term{}, fmt.Errorf("query: projection column %q must be a ?variable or an aggregate", p)
	}
	if err := checkVarName(p[1:]); err != nil {
		return Term{}, err
	}
	return Term{Var: Var(p[1:])}, nil
}

// checkVarName refuses a variable name that isn't an ident (#163): letters, digits and _, so `?)(`
// is an error rather than a variable named ")(".
func checkVarName(name string) error {
	if name == "" {
		return fmt.Errorf("query: empty variable name")
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; !(c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return fmt.Errorf("query: variable ?%s: a variable's name is letters, digits and _", name)
		}
	}
	return nil
}

// isDecimal reports whether s is a number as the grammar spells one: digits, an optional fraction and
// an optional exponent, after an optional sign. strconv.ParseFloat also reads inf, nan, hex floats and
// a bare leading or trailing dot, which the language doesn't (#163).
func isDecimal(s string) bool {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	digits := func() int {
		n := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			n++
		}
		return n
	}
	if digits() == 0 {
		return false
	}
	if i < len(s) && s[i] == '.' {
		i++
		if digits() == 0 {
			return false
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if digits() == 0 {
			return false
		}
	}
	return i == len(s)
}

// parseAggregate parses func([distinct] ?x), the form an aggregate takes in a projection, a having
// and a rule head.
func parseAggregate(p string) (Term, error) {
	i := strings.IndexByte(p, '(')
	fn := strings.TrimSpace(p[:i])
	if !strings.HasSuffix(p, ")") {
		return Term{}, fmt.Errorf("query: malformed aggregate %q", p)
	}
	inner := strings.TrimSpace(p[i+1 : len(p)-1])
	distinct := false
	if rest, ok := cutWord(inner, "distinct"); ok {
		distinct, inner = true, strings.TrimSpace(rest)
	}
	if len(inner) < 2 || inner[0] != '?' {
		return Term{}, fmt.Errorf("query: aggregate %s(...) expects a ?variable, got %q", fn, inner)
	}
	if err := checkVarName(inner[1:]); err != nil {
		return Term{}, err
	}
	return Term{Agg: &Aggregate{Func: fn, Var: Var(inner[1:]), Distinct: distinct}}, nil
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

// splitTop splits s on sep at the top level: not inside parentheses or braces and not inside a
// double-quoted string. Returns the whole string as one element when sep does not occur at the top level.
func splitTop(s, sep string) []string {
	var parts []string
	depth, inQuote, start := 0, false, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case inQuote:
			// skip
		case c == '(' || c == '{':
			depth++
		case c == ')' || c == '}':
			depth--
		case depth == 0 && strings.HasPrefix(s[i:], sep):
			parts = append(parts, s[start:i])
			i += len(sep) - 1
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}
