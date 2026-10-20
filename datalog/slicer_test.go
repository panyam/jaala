package datalog

// The parser as it stood before the tokenizer (#167), frozen as a reference: FuzzParseAgreesWithSlicer
// holds the new parser to its answers. Every name carries the sl prefix. Generated from parse.go,
// exprs.go's slParseOperand and slExprParser, and signature.go's slParseArgType at 274fa36; don't edit it.

import (
	"fmt"
	"github.com/panyam/jaala/ns"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// slParse reads a text query into a Query. Grammar (EBNF; whitespace is insignificant):
//
//	query       = { rule end } goal [ "." ] ;         (* zero or more rules, then one goal *)
//	end         = ";" | "." ;                         (* a "." only before whitespace or the end, #276 *)
//	rule        = head ":-" literals ;                (* head :- body; defines a derived relation *)
//	head        = relation "(" [ harg { "," harg } ] ")" ;
//	harg        = term [ ":" decl ] | aggregate | expr ; (* a declared type: ?n: net, see slParseArgType *)
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
//	string      = '"' { char } '"' ;
//	number      = [ "+" | "-" ] digit { digit } [ "." digit { digit } ] [ ( "e" | "E" ) [ "+" | "-" ] digit { digit } ] ;
//	relation    = ident { "." | "-" | ident } ;       (* a path: net.max_voltage, component-on-net *)
//
// A "#" outside a string starts a comment that runs to the end of the line.
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
func slParse(s string) (Query, error) {
	rules, goals, err := slSplitClauses(slStripComments(s))
	if err != nil {
		return Query{}, err
	}
	if len(goals) == 2 && strings.HasPrefix(goals[1], "=>") && len(slSplitTop(goals[0], "=>")) == 1 {
		goals = []string{goals[0] + " " + goals[1]} // `edge(?a, ?b). => ?a`: an ending before the projection (#276)
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
	body, proj, err := slSplitProjection(goalText)
	if err != nil {
		return Query{}, err
	}
	if strings.TrimSpace(body) == "" && strings.TrimSpace(proj) != "" {
		if body, err = slGoalAfterArrow(proj); err != nil {
			return Query{}, err
		}
		proj = ""
	}
	lits, err := slParseLiterals(body)
	if err != nil {
		return Query{}, err
	}
	tail, err := slSplitTail(proj)
	if err != nil {
		return Query{}, err
	}
	sel, err := slParseSelect(tail[""])
	if err != nil {
		return Query{}, err
	}
	having, err := slParseHaving(tail["having"])
	if err != nil {
		return Query{}, err
	}
	q := Query{Rules: rules, Goal: Body{Literals: lits}, Select: sel, Having: having}
	if text, ok := tail["order"]; ok {
		if q.OrderBy, err = slParseOrderBy(text); err != nil {
			return Query{}, err
		}
	}
	if text, ok := tail["after"]; ok {
		if q.After, err = slParseAfter(text); err != nil {
			return Query{}, err
		}
	}
	if text, ok := tail["limit"]; ok {
		if q.Limit, err = slParseCount("limit", text, 1); err != nil {
			return Query{}, err
		}
	}
	if text, ok := tail["offset"]; ok {
		if q.Offset, err = slParseCount("offset", text, 0); err != nil {
			return Query{}, err
		}
	}
	return q, nil
}

// slGoalAfterArrow reads a goal written after "=>" with nothing before it, as `=> r(?u, ?l)` (#276),
// which asks for every row of r: the goal r(?u, ?l), whose columns are its variables. Columns with no
// goal before them (`=> ?b, count(?a)`) are refused, saying where the goal goes.
func slGoalAfterArrow(text string) (string, error) {
	lits, err := slParseLiterals(text)
	columns := err != nil
	for _, l := range lits {
		columns = columns || l.Pos != nil && validAggFunc(l.Pos.Relation)
	}
	if columns {
		return "", fmt.Errorf("query: %q has no goal before it; the goal goes first, as in %q", "=> "+strings.TrimSpace(text), "edge(?a, ?b) => ?b, count(?a)")
	}
	return text, nil
}

// slTailWords are the clauses that may follow the projection, in the order they must be written.
var slTailWords = []string{"having", "order", "after", "limit", "offset"}

// slSplitTail cuts the projection at the clauses that follow it, keyed by keyword, with the columns
// themselves under "". A keyword matches as a bare word only, at paren depth zero, outside quotes and
// bounded on both sides, so a variable whose name merely contains one (?shaving, ?limit) is left
// alone. Each clause may appear once, in slTailWords' order, and `order` must be followed by `by`,
// which the "order" entry leaves out.
func slSplitTail(proj string) (map[string]string, error) {
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
			if c == '\\' {
				i++ // #314's change
			}
		case c == '(' || c == '{':
			depth++
		case c == ')' || c == '}':
			depth--
		case depth == 0:
			for _, w := range slTailWords {
				if slIsWordAt(proj, i, w) {
					cuts = append(cuts, cut{w, i, i + len(w)})
					break
				}
			}
		}
	}
	out := map[string]string{}
	for n, c := range cuts {
		if _, dup := out[c.word]; dup {
			return nil, fmt.Errorf("query: %s appears twice after %q", slClauseName(c.word), "=>")
		}
		if n > 0 && slices.Index(slTailWords, c.word) < slices.Index(slTailWords, cuts[n-1].word) {
			return nil, fmt.Errorf("query: %s comes after %s, not before it (the order is having, order by, after, limit, offset)", slClauseName(cuts[n-1].word), slClauseName(c.word))
		}
		end := len(proj)
		if n+1 < len(cuts) {
			end = cuts[n+1].start
		}
		text := proj[c.body:end]
		if c.word == "order" {
			rest, ok := slCutWord(strings.TrimSpace(text)+" ", "by")
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

// slClauseName is a tail keyword as written: "order" is "order by".
func slClauseName(word string) string {
	if word == "order" {
		return "order by"
	}
	return word
}

// slParseOrderBy reads the comma-separated sort columns, each a projection column optionally followed
// by asc or desc. Whether it names a selected column is checked with the projection (validateOrder).
func slParseOrderBy(s string) ([]Order, error) {
	var out []Order
	for _, piece := range slSplitTop(s, ",") {
		piece = strings.TrimSpace(piece)
		desc := false
		for _, dir := range []string{"asc", "desc"} {
			if i := len(piece) - len(dir); i > 0 && slIsWordAt(piece, i, dir) {
				piece, desc = strings.TrimSpace(piece[:i]), dir == "desc"
				break
			}
		}
		if piece == "" {
			return nil, fmt.Errorf("query: order by needs a column, as in %q", "order by ?n desc")
		}
		t, err := slParseSelItem(piece)
		if err != nil {
			return nil, &slContextParseError{context: "order by", cause: err}
		}
		out = append(out, Order{Term: t, Desc: desc})
	}
	return out, nil
}

// slParseCount reads limit's or offset's row count, a whole number no smaller than min.
// slParseAfter reads an after clause's values, one per order by column, which validateOrder matches up.
func slParseAfter(s string) ([]ns.Value, error) {
	var out []ns.Value
	for _, piece := range slSplitTop(s, ",") {
		t, err := slParseTerm(piece)
		if err != nil {
			return nil, err
		}
		if t.Const == nil {
			return nil, fmt.Errorf("query: after takes values, as in %q, and %s is not one", `after 812, "db"`, strings.TrimSpace(piece))
		}
		out = append(out, *t.Const)
	}
	return out, nil
}

func slParseCount(word, s string, min int) (int, error) {
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

// slIsWordAt reports whether word sits at s[i:] with a non-identifier character (or the string edge) on
// either side, so "having" matches and "?shaving" and "having_x" do not.
func slIsWordAt(s string, i int, word string) bool {
	if !strings.HasPrefix(s[i:], word) {
		return false
	}
	if i > 0 && slIsIdentByte(s[i-1]) {
		return false
	}
	if j := i + len(word); j < len(s) && slIsIdentByte(s[j]) {
		return false
	}
	return true
}

// slCutWord strips a leading keyword and the whitespace after it, reporting whether it was there. It
// requires the whitespace, so `distinct ?x` is the modifier and a variable literally named
// `?distinctxyz` is not mistaken for one.
func slCutWord(s, word string) (rest string, ok bool) {
	if !strings.HasPrefix(s, word) {
		return s, false
	}
	rest = s[len(word):]
	if rest == "" || !slIsSpaceByte(rest[0]) {
		return s, false
	}
	return rest, true
}

func slIsSpaceByte(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func slIsIdentByte(c byte) bool {
	return c == '_' || c == '?' || c == '.' || c == '-' ||
		(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// slParseHaving reads the comma-separated group filters. Each is a comparison whose LEFT side is an
// aggregate: the right side is an ordinary term, so `count(?n) < 2` and `count(?n) = ?limit` both
// parse, and the evaluator rejects the second when nothing binds ?limit per group.
func slParseHaving(s string) ([]Compare, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []Compare
	for _, piece := range slSplitTop(s, ",") {
		piece = strings.TrimSpace(piece)
		if piece == "" {
			return nil, fmt.Errorf("query: having needs a comparison, as in %q", "having count(?n) > 1")
		}
		c, err := slParseHavingOne(piece)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

type slContextParseError struct {
	context string
	cause   error
}

func (e *slContextParseError) Error() string {
	return fmt.Sprintf("query: %s: %s", e.context, strings.TrimPrefix(e.cause.Error(), "query: "))
}

func (e *slContextParseError) Unwrap() error { return e.cause }

// slParseHavingOne reads one group filter. The left side goes through slParseSelItem (the projection's
// term parser, which is the one that knows aggregates) rather than slParseTerm, so an aggregate stays
// spellable HERE and stays unspellable in the goal body, where it would have nothing to reduce.
func slParseHavingOne(piece string) (Compare, error) {
	for _, op := range slCompareOps {
		parts := slSplitTop(piece, op)
		if len(parts) != 2 {
			continue
		}
		left, err := slParseSelItem(strings.TrimSpace(parts[0]))
		if err != nil {
			return Compare{}, &slContextParseError{context: fmt.Sprintf("having %q", piece), cause: err}
		}
		if left.Agg == nil {
			return Compare{}, fmt.Errorf("query: having %q filters ?%s, which is a group key rather than an aggregate — a comparison over plain variables belongs in the goal, before the %q", piece, left.Var, "=>")
		}
		right, err := slParseTerm(parts[1])
		if err != nil {
			return Compare{}, &slContextParseError{context: fmt.Sprintf("having %q", piece), cause: err}
		}
		return Compare{Left: left, Op: op, Right: right}, nil
	}
	return Compare{}, fmt.Errorf("query: having %q is not a comparison (want an aggregate, an operator and a value, as in %q)", piece, "count(?n) < 2")
}

// slParseRules reads text that holds rules only, such as a derived module's body (see
// Language). It accepts the same clause syntax as slParse and refuses a goal clause, since
// rules with nothing to ask are a definition rather than a query.
//
//lint:ignore U1000 the old parser is frozen as it was (see the file's comment), so nothing in it is deleted
func slParseRules(s string) ([]Rule, error) {
	rules, goals, err := slSplitClauses(slStripComments(s))
	if err != nil {
		return nil, err
	}
	if len(goals) > 0 {
		return nil, fmt.Errorf("query: %q is a goal; rules-only text defines relations and asks nothing", goals[0])
	}
	return rules, nil
}

// slStripComments blanks every "#" comment, from the "#" to the end of its line, unless the "#" sits
// inside a string. Blanking rather than deleting keeps the clause text otherwise intact.
func slStripComments(s string) string {
	if !strings.Contains(s, "#") {
		return s
	}
	b := []byte(s)
	inQuote, inComment := false, false
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case inComment:
			if c == '\n' {
				inComment = false
			} else {
				b[i] = ' '
			}
		case c == '"':
			inQuote = !inQuote
		case c == '\\' && inQuote:
			i++ // #314's change
		case c == '#' && !inQuote:
			inComment = true
			b[i] = ' '
		}
	}
	return string(b)
}

// slSplitClauses separates text into its rule definitions and its goal clauses. Clauses are separated
// by ";"; a clause containing ":-" is a rule (head :- body), and every other clause is a goal. The
// caller decides how many goals it accepts: a query asks exactly one, a module none.
func slSplitClauses(s string) (rules []Rule, goals []string, err error) {
	for _, clause := range slSplitEnds(s) {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(clause, "?-"); ok {
			goals = append(goals, strings.TrimSpace(rest))
			continue
		}
		parts := slSplitTop(clause, ":-")
		switch len(parts) {
		case 1:
			goals = append(goals, clause)
		case 2:
			rule, rerr := slParseRule(parts[0], parts[1])
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

// slSplitEnds splits text into clauses at each top-level ";", and at each top-level "." followed by
// whitespace or the end, the ending Datalog and Prolog write (#276). A "." inside a relation path
// (str.suffix) or a number (1.5) is followed by a letter or digit, so it never ends a clause.
func slSplitEnds(s string) []string {
	var parts []string
	for _, piece := range slSplitTop(s, ";") {
		start := 0
		for _, i := range slTopIndexes(piece, '.') {
			if i+1 == len(piece) || slIsSpace(piece[i+1]) {
				parts = append(parts, piece[start:i])
				start = i + 1
			}
		}
		parts = append(parts, piece[start:])
	}
	return parts
}

// slTopIndexes returns the positions of c outside strings and brackets, as slSplitTop reads them.
func slTopIndexes(s string, c byte) []int {
	var out []int
	depth, inQuote := 0, false
	for i := 0; i < len(s); i++ {
		switch b := s[i]; {
		case b == '"':
			inQuote = !inQuote
		case inQuote:
			if b == '\\' {
				i++ // #314's change
			}
		case b == '(' || b == '{':
			depth++
		case b == ')' || b == '}':
			depth--
		case depth == 0 && b == c:
			out = append(out, i)
		}
	}
	return out
}

func slIsSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// slParseRule parses one "head :- body" clause into a Rule. The head is a single atom; the body is a
// conjunction of literals, the same grammar a goal body uses.
func slParseRule(headText, bodyText string) (Rule, error) {
	head, types, err := slParseHead(headText)
	if err != nil {
		return Rule{}, err
	}
	if len(slSplitTop(bodyText, "=>")) > 1 {
		return Rule{}, fmt.Errorf("query: the rule for %s holds %q; end the rule with %q or %q, then write the goal", head.Relation, "=>", ";", ".")
	}
	lits, err := slParseLiterals(bodyText)
	if err != nil {
		return Rule{}, err
	}
	return Rule{Head: head, Body: Body{Literals: lits}, HeadTypes: types}, nil
}

// slParseHead parses a rule head, whose arguments may declare their types: `x(?n: net, ?v)`. The
// declarations come back by position, nil when the head declares none. A KindFrom or Owner must name
// another variable of the same head, since a type can only point at something the relation carries.
func slParseHead(s string) (Atom, []ns.ArgType, error) {
	open := strings.IndexByte(s, '(')
	if open < 0 || !strings.HasSuffix(strings.TrimSpace(s), ")") || !strings.Contains(s, ":") {
		a, err := slParseHeadAtom(s)
		return a, nil, err
	}
	inner := strings.TrimSpace(s)
	inner = inner[open+1 : len(inner)-1]
	var plain []string
	var types []ns.ArgType
	declared := false
	for _, a := range slSplitTop(inner, ",") {
		if strings.TrimSpace(a) == "" {
			return Atom{}, nil, fmt.Errorf("query: empty term")
		}
		var t ns.ArgType
		if parts := slSplitTop(a, ":"); len(parts) == 2 {
			var err error
			if t, err = slParseArgType(parts[1]); err != nil {
				return Atom{}, nil, err
			}
			if v := strings.TrimSpace(parts[0]); len(v) < 2 || v[0] != '?' || v == "?_" || slIsExpression(v) {
				return Atom{}, nil, fmt.Errorf("query: only a ?variable can declare a type, not %q", v)
			}
			a, declared = parts[0], true
		}
		plain = append(plain, a)
		types = append(types, t)
	}
	head, err := slParseHeadAtom(strings.TrimSpace(s[:open]) + "(" + strings.Join(plain, ",") + ")")
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

// slSplitProjection splits a query on the top-level "=>" into body and projection (the projection
// empty when absent). "=>" is unambiguous — no comparison operator is "=>".
func slSplitProjection(s string) (body, proj string, err error) {
	parts := slSplitTop(s, "=>")
	switch len(parts) {
	case 1:
		return parts[0], "", nil
	case 2:
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("query: more than one %q projection separator", "=>")
	}
}

func slParseLiterals(body string) ([]Literal, error) {
	if strings.TrimSpace(body) == "" {
		return nil, fmt.Errorf("query: empty query")
	}
	var lits []Literal
	for _, piece := range slSplitTop(body, ",") {
		piece = strings.TrimSpace(piece)
		if piece == "" {
			return nil, fmt.Errorf("query: empty literal")
		}
		lit, err := slParseLiteral(piece)
		if err != nil {
			return nil, err
		}
		lits = append(lits, lit)
	}
	return lits, nil
}

// slParseLiteral parses one literal: a "not R(...)" is a negated atom, `?v = func(?x) : { body }` an
// aggregate over a body of its own, another parenthesised piece a positive atom, anything else a
// comparison.
func slParseLiteral(s string) (Literal, error) {
	if parts := slSplitTop(s, ":"); len(parts) == 2 && strings.HasPrefix(strings.TrimSpace(parts[1]), "{") {
		return slParseBodyAggregate(parts[0], parts[1])
	}
	rest, isNeg := strings.CutPrefix(strings.TrimSpace(s), "not ")
	if bang, ok := strings.CutPrefix(strings.TrimSpace(s), "!"); ok {
		rest, isNeg = bang, true
	}
	if isNeg {
		atom, err := slParseAtom(strings.TrimSpace(rest))
		if err != nil {
			return Literal{}, err
		}
		return Literal{Neg: &atom}, nil
	}
	if strings.Contains(s, "(") && !slHasTopComparison(s) {
		atom, err := slParseAtom(s)
		if err != nil {
			return Literal{}, err
		}
		return Literal{Pos: &atom}, nil
	}
	cmp, err := slParseComparison(s)
	if err != nil {
		return Literal{}, err
	}
	return Literal{Compare: &cmp}, nil
}

// slParseBodyAggregate parses `?v = func([distinct] ?x) : { body }`, split at its top-level colon.
func slParseBodyAggregate(left, right string) (Literal, error) {
	right = strings.TrimSpace(right)
	if !strings.HasSuffix(right, "}") {
		return Literal{}, fmt.Errorf("query: an aggregate's body %q must close with }", right)
	}
	inner := right[1 : len(right)-1]
	sides := slSplitTop(left, "=")
	if len(sides) != 2 {
		return Literal{}, fmt.Errorf("query: %q must name the variable an aggregate binds, as ?v = count(?x) : { ... }", strings.TrimSpace(left))
	}
	v, err := slParseTerm(sides[0])
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
	agg, err := slParseAggregate(fn)
	if err != nil {
		return Literal{}, err
	}
	if strings.TrimSpace(inner) == "" {
		return Literal{}, fmt.Errorf("query: the aggregate binding ?%s has an empty body", v.Var)
	}
	lits, err := slParseLiterals(inner)
	if err != nil {
		return Literal{}, err
	}
	return Literal{Agg: &BodyAggregate{Result: v.Var, Agg: *agg.Agg, Body: Body{Literals: lits}}}, nil
}

func slParseAtom(s string) (Atom, error) { return slParseAtomArgs(s, slParseTerm) }

// slParseHeadAtom parses a rule head, whose arguments may also be aggregates: degree(?n, count(?m)).
func slParseHeadAtom(s string) (Atom, error) {
	return slParseAtomArgs(s, func(a string) (Term, error) {
		if a = strings.TrimSpace(a); a != "" && a[0] != '"' && strings.IndexByte(a, '(') >= 0 && !slIsExpression(a) {
			return slParseAggregate(a)
		}
		if slIsExpression(a) {
			return slParseOperand(a)
		}
		return slParseTerm(a)
	})
}

// slIsExpression reports whether a head argument or a projection column is arithmetic (#206) rather
// than a term or an aggregate. A whole column name(...) with an undotted name is an aggregate's
// syntax, so avg(?m) and count(0) still fail as aggregates; a dotted call, num.add(?r, 1), or an
// operator over terms is arithmetic.
func slIsExpression(s string) bool {
	s = strings.TrimSpace(s)
	if _, err := slParseTerm(s); err == nil {
		return false
	}
	t, err := slParseOperand(s)
	if err != nil || t.Expr == nil {
		return false
	}
	return t.Expr.Op != "call" || strings.Contains(t.Expr.Func, ".")
}

func slParseAtomArgs(s string, term func(string) (Term, error)) (Atom, error) {
	open := strings.IndexByte(s, '(')
	if open < 0 || !strings.HasSuffix(strings.TrimSpace(s), ")") {
		return Atom{}, fmt.Errorf("query: malformed atom %q (want reln(args))", s)
	}
	rel := strings.TrimSpace(s[:open])
	if !slIsRelation(rel) {
		return Atom{}, fmt.Errorf("query: bad relation name %q", rel)
	}
	inner := strings.TrimSpace(s)
	inner = inner[open+1 : len(inner)-1]
	if strings.TrimSpace(inner) == "" {
		return Atom{Relation: rel}, nil
	}
	var args []Term
	for _, a := range slSplitTop(inner, ",") {
		t, err := term(a)
		if err != nil {
			return Atom{}, err
		}
		args = append(args, t)
	}
	return Atom{Relation: rel, Args: args}, nil
}

var slCompareOps = []string{"<=", ">=", "!=", "<", ">", "="} // longest first

// slHasTopComparison reports whether s has a comparison operator outside parentheses and quotes, so
// ?y = f(?x) is a comparison over a call rather than an atom.
func slHasTopComparison(s string) bool {
	for _, op := range slCompareOps {
		if len(slSplitTop(s, op)) == 2 {
			return true
		}
	}
	return false
}

func slParseComparison(s string) (Compare, error) {
	for _, op := range slCompareOps {
		if parts := slSplitTop(s, op); len(parts) == 2 {
			l, err := slParseOperand(parts[0])
			if err != nil {
				return Compare{}, err
			}
			r, err := slParseOperand(parts[1])
			if err != nil {
				return Compare{}, err
			}
			return Compare{Left: l, Op: op, Right: r}, nil
		}
	}
	return Compare{}, fmt.Errorf("query: %q is neither an atom nor a comparison", strings.TrimSpace(s))
}

func slParseTerm(s string) (Term, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return Term{}, fmt.Errorf("query: empty term")
	case s == "_":
		return Term{Var: "_"}, nil
	case s[0] == '?':
		name := s[1:]
		if err := slCheckVarName(name); err != nil {
			return Term{}, err
		}
		return Term{Var: Var(name)}, nil
	case s[0] == '"':
		if len(s) < 2 || s[len(s)-1] != '"' || stringEnd(s[1:]) < 0 { // #314's change: \" doesn't end it
			return Term{}, fmt.Errorf("query: unterminated string %q", s)
		}
		if stringEnd(s[1:]) != len(s)-2 { // #167's one change, #314's backslash
			return Term{}, fmt.Errorf("query: %s is more than one string; put a backslash before a quote inside a string", s)
		}
		return Term{Const: &ns.Value{S: s[1 : len(s)-1]}}, nil
	default:
		if f, err := strconv.ParseFloat(s, 64); err == nil && slIsDecimal(s) {
			return Term{Const: &ns.Value{S: s, Num: &f}}, nil
		}
		// A number with a unit after it, as in 3.3V or 1kohm (#5, #79): the unit is read through the
		// vocabulary when the query is checked (coerceConstants).
		// A bare e or E reads as an exponent left unfinished, which #163 refuses (1eV is a unit).
		if num, unit := slSplitUnitSuffix(s); unit != "" && unit != "e" && unit != "E" && slIsDecimal(num) {
			if f, err := strconv.ParseFloat(num, 64); err == nil {
				return Term{Const: &ns.Value{S: num, Num: &f, BaseUnit: unit}}, nil
			}
		}
		if slIsWord(s) {
			return Term{}, fmt.Errorf("query: bare identifier %q — a term must be a ?variable, a \"string\", or a number; write ?%s for a variable or %q for text", s, s, s)
		}
		return Term{}, fmt.Errorf("query: bare identifier %q — a term must be a ?variable, a \"string\", or a number", s)
	}
}

// slParseSelect parses the projection columns: each is a ?variable (a group key), an aggregate
// func(?variable), func in {count,min,max,sum,list}, or arithmetic over variables (#206).
func slParseSelect(proj string) ([]Term, error) {
	proj = strings.TrimSpace(proj)
	if proj == "" {
		return nil, nil
	}
	var sel []Term
	for _, p := range slSplitTop(proj, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("query: the projection has an empty column, as in %q", "=> ?a, ?b")
		}
		t, err := slParseSelItem(p)
		if err != nil {
			return nil, err
		}
		sel = append(sel, t)
	}
	return sel, nil
}

func slParseSelItem(p string) (Term, error) {
	if slIsExpression(p) {
		return slParseOperand(p)
	}
	if strings.IndexByte(p, '(') >= 0 {
		return slParseAggregate(p)
	}
	if p == "" || p[0] != '?' || len(p) == 1 {
		return Term{}, fmt.Errorf("query: projection column %q must be a ?variable or an aggregate", p)
	}
	if err := slCheckVarName(p[1:]); err != nil {
		return Term{}, err
	}
	return Term{Var: Var(p[1:])}, nil
}

// slCheckVarName refuses a variable name that isn't an ident (#163): letters, digits and _, so `?)(`
// is an error rather than a variable named ")(".
func slCheckVarName(name string) error {
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

// slIsWord reports whether s is a name a variable or a text could spell: a letter, then letters, digits
// and _. inf and nan aren't, since they read as numbers that #163 refuses.
func slIsWord(s string) bool {
	if s == "" || !unicode.IsLetter(rune(s[0])) || slCheckVarName(s) != nil {
		return false
	}
	_, err := strconv.ParseFloat(s, 64)
	return err != nil
}

// slSplitUnitSuffix splits the letters at the end of s off it: 3.3V is 3.3 and V. Letters only, so a
// compound unit is written as arithmetic: 3m / 1s.
func slSplitUnitSuffix(s string) (num, unit string) {
	i := len(s)
	for i > 0 {
		r, w := utf8.DecodeLastRuneInString(s[:i])
		if !unicode.IsLetter(r) {
			break
		}
		i -= w
	}
	return s[:i], s[i:]
}

// slIsDecimal reports whether s is a number as the grammar spells one: digits, an optional fraction and
// an optional exponent, after an optional sign. strconv.ParseFloat also reads inf, nan, hex floats and
// a bare leading or trailing dot, which the language doesn't (#163).
func slIsDecimal(s string) bool {
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

// slParseAggregate parses func([distinct] ?x), the form an aggregate takes in a projection, a having
// and a rule head.
func slParseAggregate(p string) (Term, error) {
	i := strings.IndexByte(p, '(')
	fn := strings.TrimSpace(p[:i])
	if !strings.HasSuffix(p, ")") || strings.Contains(fn, `"`) { // #167's second change
		return Term{}, fmt.Errorf("query: malformed aggregate %q", p)
	}
	inner := strings.TrimSpace(p[i+1 : len(p)-1])
	distinct := false
	if rest, ok := slCutWord(inner, "distinct"); ok {
		distinct, inner = true, strings.TrimSpace(rest)
	}
	if len(inner) < 2 || inner[0] != '?' {
		return Term{}, fmt.Errorf("query: aggregate %s(...) expects a ?variable, got %q", fn, inner)
	}
	if err := slCheckVarName(inner[1:]); err != nil {
		return Term{}, err
	}
	return Term{Agg: &Aggregate{Func: fn, Var: Var(inner[1:]), Distinct: distinct}}, nil
}

// slIsRelation reports whether name is a valid relation identifier (letters, digits, '.', '-', '_').
func slIsRelation(name string) bool {
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

// slSplitTop splits s on sep at the top level: not inside parentheses or braces and not inside a
// double-quoted string. Returns the whole string as one element when sep does not occur at the top level.
func slSplitTop(s, sep string) []string {
	var parts []string
	depth, inQuote, start := 0, false, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case inQuote:
			if c == '\\' {
				i++ // #314's change
			}
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

// slParseOperand reads a comparison's operand: a term as slParseTerm reads it, or an expression. A plain
// term parses exactly as before, so its errors don't move.
func slParseOperand(s string) (Term, error) {
	t, err := slParseTerm(s)
	if err == nil {
		return t, nil
	}
	p := &slExprParser{s: s}
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

type slExprParser struct {
	s string
	i int
}

func (p *slExprParser) skip() {
	for p.i < len(p.s) && p.s[p.i] == ' ' {
		p.i++
	}
}

func (p *slExprParser) peek() byte {
	if p.skip(); p.i < len(p.s) {
		return p.s[p.i]
	}
	return 0
}

func (p *slExprParser) expr() (Term, error) {
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

func (p *slExprParser) term() (Term, error) {
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

func (p *slExprParser) unary() (Term, error) {
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

func (p *slExprParser) primary() (Term, error) {
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
		end := stringEnd(p.s[p.i+1:]) // #314's change
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
	if p.peek() == '(' && word[0] != '?' && slIsRelation(word) {
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
	return slParseTerm(word)
}

// slParseArgType reads a declaration, the text after ":" in a rule head argument:
//
//	decl   = "?" ident                       (* kind from that argument, per row *)
//	       | "string" | "number" [ "[" unit "]" ]
//	       | ident [ "(" "?" ident ")" ]     (* entity kind, optionally located by an owner *)
//	       | vocab ;
//	vocab  = "{" string { "," string } "}" ;  (* may also follow any of the above *)
//
// "string" and "number" are therefore not available as entity kinds.
func slParseArgType(s string) (ns.ArgType, error) {
	s = strings.TrimSpace(s)
	var t ns.ArgType
	if i := strings.IndexByte(s, '{'); i >= 0 {
		if !strings.HasSuffix(s, "}") {
			return t, fmt.Errorf("query: unterminated vocabulary in %q", s)
		}
		for _, item := range slSplitTop(s[i+1:len(s)-1], ",") {
			item = strings.TrimSpace(item)
			if len(item) < 2 || item[0] != '"' || stringEnd(item[1:]) != len(item)-2 { // #167's one change, #314's backslash
				return t, fmt.Errorf("query: vocabulary entry %q must be a \"string\"", item)
			}
			t.Domain = append(t.Domain, item[1:len(item)-1])
		}
		s = strings.TrimSpace(s[:i])
		if s == "" {
			t.Type = ns.TypeString
			return t, nil
		}
	}
	switch {
	case s == "":
		return t, fmt.Errorf("query: empty type declaration")
	case s[0] == '?':
		if !slIsIdent(s[1:]) {
			return t, fmt.Errorf("query: bad kind variable %q", s)
		}
		t.KindFrom = s[1:]
	case s == ns.TypeString || s == ns.TypeNumber:
		t.Type = s
	case strings.HasPrefix(s, ns.TypeNumber+"[") && strings.HasSuffix(s, "]"):
		t.Type, t.Unit = ns.TypeNumber, strings.TrimSpace(s[len(ns.TypeNumber)+1:len(s)-1])
	case strings.HasSuffix(s, ")"):
		open := strings.IndexByte(s, '(')
		owner := strings.TrimSpace(s[open+1 : len(s)-1])
		if open < 0 || len(owner) < 2 || owner[0] != '?' || !slIsIdent(owner[1:]) || !slIsIdent(strings.TrimSpace(s[:open])) {
			return t, fmt.Errorf("query: bad type declaration %q (an owner is written kind(?var))", s)
		}
		t.Kind, t.Owner = strings.TrimSpace(s[:open]), owner[1:]
	case slIsIdent(s):
		t.Kind = s
	default:
		return t, fmt.Errorf("query: bad type declaration %q", s)
	}
	return t, nil
}

func slIsIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
