package cypher

import (
	"strconv"
	"strings"
)

// parse reads all of Neo4j 5's query syntax (#382). The subset it builds a tree for (EBNF, keywords
// in any case):
//
//	statement = [ "CYPHER" options | "PROFILE" ] query { "UNION" [ "ALL" ] query } [ ";" ] ;
//	query     = { reading } { "WITH" items [ "WHERE" expr ] { reading } } return ;   (* reading first *)
//	reading   = match | call ;
//	match     = "MATCH" pattern { "," pattern } [ "WHERE" expr ] ;
//	call      = "CALL" name [ "(" [ expr { "," expr } ] ")" ] [ "YIELD" ( "*" | name [ "AS" name ] { "," ... } ) [ "WHERE" expr ] ] ;
//	pattern   = node { rel node } ;
//	node      = "(" [ name ] [ ( ":" | "IS" ) labels ] [ props ] [ "WHERE" expr ] ")" ;
//	labels    = name { ( ":" | "&" ) name } ;
//	rel       = ( "-" | "<-" ) [ "[" [ name ] [ ":" name { "|" [ ":" ] name } ] [ range ] [ props ] [ "WHERE" expr ] "]" ] ( "-" | "->" ) ;
//	range     = "*" [ int ] [ ".." [ int ] ] ;
//	props     = "{" name ":" expr { "," name ":" expr } "}" ;
//	return    = "RETURN" [ "DISTINCT" ] ( "*" [ "," item { "," item } ] | item { "," item } )
//	            [ "ORDER" "BY" expr [ "ASC" | "DESC" ] { "," ... } ] [ ( "SKIP" | "OFFSET" ) int ] [ "LIMIT" int ] ;
//	item      = expr [ "AS" name ] ;
//
// A WHERE inside a node or relationship of a MATCH is ANDed into the clause's WHERE. Expressions
// follow openCypher's precedence: OR, XOR, AND, NOT, comparison (with the string tests, IN, IS NULL
// and IS Label), + and -, * / % and ^, unary minus, then a property, a label test, a call (split's
// [i] too), EXISTS { } or COUNT { } (an optional MATCH, patterns and a WHERE), a literal (hex and
// octal integers too), a parameter, a parenthesized expression or a pattern.
//
// The rest of Neo4j 5's queries it reads too (refused.go), recording a refusal naming the gap. The
// first refusal in the text is parse's error, but only once the whole text has parsed, so a mistake
// anywhere in it is reported before a gap. Backfilling a gap is building a node where its parse
// function records the refusal. A command that isn't a query (SHOW, GRANT, CREATE INDEX, ...) is
// refused at its first word.
type parser struct {
	toks []token
	i    int
	text string
	// refused is the first refusal in the text so far, which parse returns when the text parses.
	refused *Error
	// braces is how many subqueries' { } the parser is inside, where a query may end without RETURN.
	braces int
}

func parse(text string) (statement, error) {
	toks, err := lex(text)
	if err != nil {
		return statement{}, err
	}
	p := &parser{toks: toks, text: text}
	st, perr := p.statement()
	if perr != nil {
		return statement{}, perr
	}
	if p.refused != nil {
		return statement{}, p.refused
	}
	return st, nil
}

// refuse records e, Cypher a program can't express yet, and parsing goes on past it. It returns e as
// an expression, for a refused expression's place in the tree.
func (p *parser) refuse(e *Error) expr {
	if p.refused == nil || e.Pos.Offset < p.refused.Pos.Offset {
		p.refused = e
	}
	return unsupported{pos: e.Pos, err: e}
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) peekAt(n int) token {
	if p.i+n < len(p.toks) {
		return p.toks[p.i+n]
	}
	return p.toks[len(p.toks)-1]
}
func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tEOF {
		p.i++
	}
	return t
}

func (p *parser) acceptSym(s string) bool {
	if p.peek().sym(s) {
		p.i++
		return true
	}
	return false
}

func (p *parser) accept(kw string) bool {
	if p.peek().is(kw) {
		p.i++
		return true
	}
	return false
}

func (p *parser) expectSym(s, what string) *Error {
	if p.acceptSym(s) {
		return nil
	}
	return p.unexpected(what)
}

func (p *parser) expect(kw, what string) *Error {
	if p.accept(kw) {
		return nil
	}
	return p.unexpected(what)
}

func (p *parser) unexpected(want string) *Error {
	t := p.peek()
	if t.kind == tEOF {
		return errAt(t.pos, "expected %s, at the end of the query", want)
	}
	return errAt(t.pos, "expected %s, found %q", want, t.text)
}

func (p *parser) name(what string) (string, *Error) {
	t := p.peek()
	if t.kind != tIdent {
		return "", p.unexpected(what)
	}
	p.i++
	return t.val, nil
}

// exprs reads expressions separated by commas.
func (p *parser) exprs() ([]expr, *Error) {
	var out []expr
	for {
		e, err := p.expr()
		if err != nil {
			return nil, err
		}
		out = append(out, e)
		if !p.acceptSym(",") {
			return out, nil
		}
	}
}

func (p *parser) statement() (statement, *Error) {
	var st statement
	if err := p.prefix(); err != nil {
		return st, err
	}
	parts, err := p.union()
	st.parts = parts
	if err != nil {
		return st, err
	}
	p.acceptSym(";")
	if t := p.peek(); t.kind != tEOF {
		return st, errAt(t.pos, "unexpected %q after RETURN", t.text)
	}
	return st, nil
}

// union reads queries joined by UNION or UNION ALL.
func (p *parser) union() ([]query, *Error) {
	var parts []query
	for {
		q, err := p.query()
		if err != nil {
			return parts, err
		}
		parts = append(parts, q)
		if !p.accept("UNION") {
			return parts, nil
		}
		p.accept("ALL")
	}
}

// query reads one UNION part: stages of MATCH and CALL clauses, each closed by a WITH, the last by
// RETURN. A query that writes, ends with FINISH or a procedure's CALL, or sits inside a subquery's
// { } may end without RETURN, which is refused.
func (p *parser) query() (query, *Error) {
	q := query{pos: p.peek().pos}
	var cur stage
	var ends *Pos // where the clause starts after which the query may end without RETURN
	for {
		t := p.peek()
		if t.kind == tEOF || t.sym("}") || t.sym(";") || t.is("UNION") {
			switch {
			case ends != nil:
				p.refuse(gapLater.at(*ends, "a query with no RETURN"))
				return q, nil
			case p.braces > 0 && t.sym("}"):
				return q, nil
			case len(q.stages) == 0 && len(cur.clauses) == 0:
				return q, p.unexpected("MATCH")
			}
			return q, p.unexpected("WHERE, another MATCH, WITH or RETURN")
		}
		handled, canEnd, err := p.refusedClause()
		if err != nil {
			return q, err
		}
		if handled {
			if canEnd {
				ends = &t.pos
			} else {
				ends = nil
			}
			continue
		}
		ends = nil
		switch {
		case t.is("MATCH"):
			p.i++
			m, err := p.match(t.pos)
			if err != nil {
				return q, err
			}
			cur.clauses = append(cur.clauses, readClause{match: &m})
		case t.is("CALL"):
			p.i++
			cc, err := p.callClause(t.pos)
			if err != nil {
				return q, err
			}
			cur.clauses = append(cur.clauses, readClause{call: &cc})
			ends = &t.pos
		case t.is("WITH"), t.is("RETURN"):
			if len(q.stages) == 0 && len(cur.clauses) == 0 {
				p.refuse(gapLater.at(t.pos, "a %s before any MATCH", strings.ToUpper(t.val)))
			}
			p.i++
			r, err := p.returnClause(t.pos)
			if err != nil {
				return q, err
			}
			cur.proj = r
			q.stages = append(q.stages, cur)
			cur = stage{}
			if !t.is("WITH") {
				return q, nil
			}
			cur.proj.with = true
			q.stages[len(q.stages)-1].proj.with = true
			if p.accept("WHERE") {
				e, err := p.expr()
				if err != nil {
					return q, err
				}
				q.stages[len(q.stages)-1].proj.where = e
			}
		default:
			if len(q.stages) == 0 && len(cur.clauses) == 0 {
				return q, p.unexpected("MATCH")
			}
			return q, p.unexpected("WHERE, another MATCH, WITH or RETURN")
		}
	}
}

func (p *parser) match(pos Pos) (matchClause, *Error) {
	m := matchClause{pos: pos}
	pats, err := p.patterns()
	if err != nil {
		return m, err
	}
	m.patterns = pats
	if p.accept("WHERE") {
		e, err := p.expr()
		if err != nil {
			return m, err
		}
		m.where = e
	}
	m.where = andAll(append(inlineWheres(m.patterns), m.where)...)
	return m, nil
}

// patterns reads patterns separated by commas.
func (p *parser) patterns() ([]pattern, *Error) {
	var out []pattern
	for {
		pat, err := p.pattern()
		if err != nil {
			return nil, err
		}
		out = append(out, pat)
		if !p.acceptSym(",") {
			return out, nil
		}
	}
}

// inlineWheres are the WHEREs written inside pats' nodes and relationships, in written order.
func inlineWheres(pats []pattern) []expr {
	var out []expr
	for _, pat := range pats {
		for i, n := range pat.nodes {
			if n.where != nil {
				out = append(out, n.where)
			}
			if i < len(pat.rels) && pat.rels[i].where != nil {
				out = append(out, pat.rels[i].where)
			}
		}
	}
	return out
}

// andAll is the AND of the non-nil es, nil when there are none.
func andAll(es ...expr) expr {
	var out expr
	for _, e := range es {
		switch {
		case e == nil:
		case out == nil:
			out = e
		default:
			out = binary{pos: e.at(), op: "AND", l: out, r: e}
		}
	}
	return out
}

// callClause reads a procedure call after CALL: name[(args)] [YIELD name [AS alias], ... | *]
// [WHERE], or a CALL { } subquery, refused.
func (p *parser) callClause(pos Pos) (callClause, *Error) {
	cc := callClause{pos: pos}
	if p.peek().sym("{") || p.peek().sym("(") {
		p.refuse(gapLater.at(pos, "a CALL { } subquery"))
		return cc, p.callSubquery()
	}
	name, err := p.name("a procedure's name after CALL")
	if err != nil {
		return cc, err
	}
	for p.acceptSym(".") {
		n, err := p.name("a procedure's name")
		if err != nil {
			return cc, err
		}
		name += "." + n
	}
	cc.name = name
	if p.acceptSym("(") && !p.acceptSym(")") {
		if cc.args, err = p.exprs(); err != nil {
			return cc, err
		}
		if err := p.expectSym(")", ") to end the call"); err != nil {
			return cc, err
		}
	}
	if p.accept("YIELD") {
		if p.acceptSym("*") {
			cc.star = true
		} else {
			for {
				t := p.peek()
				n, err := p.name("a name after YIELD")
				if err != nil {
					return cc, err
				}
				y := yieldItem{pos: t.pos, name: n}
				if p.accept("AS") {
					if y.alias, err = p.name("a name after AS"); err != nil {
						return cc, err
					}
				}
				cc.yields = append(cc.yields, y)
				if !p.acceptSym(",") {
					break
				}
			}
		}
		if p.accept("WHERE") {
			e, err := p.expr()
			if err != nil {
				return cc, err
			}
			cc.where = e
		}
	}
	return cc, nil
}

func (p *parser) returnClause(pos Pos) (returnClause, *Error) {
	r := returnClause{pos: pos, distinct: p.accept("DISTINCT")}
	if p.acceptSym("*") {
		r.star = true
	}
	if !r.star || p.acceptSym(",") {
		for {
			start := p.peek().pos.Offset
			e, err := p.expr()
			if err != nil {
				return r, err
			}
			item := returnItem{e: e, text: strings.TrimSpace(p.text[start:p.peek().pos.Offset])}
			if p.accept("AS") {
				if item.alias, err = p.name("a column name after AS"); err != nil {
					return r, err
				}
			}
			r.items = append(r.items, item)
			if !p.acceptSym(",") {
				break
			}
		}
	}
	if p.accept("ORDER") {
		if !p.accept("BY") {
			return r, p.unexpected("BY after ORDER")
		}
		for {
			e, err := p.expr()
			if err != nil {
				return r, err
			}
			o := orderItem{e: e}
			switch {
			case p.accept("DESC"), p.accept("DESCENDING"):
				o.desc = true
			case p.accept("ASC"), p.accept("ASCENDING"):
			}
			r.order = append(r.order, o)
			if !p.acceptSym(",") {
				break
			}
		}
	}
	if t := p.peek(); p.accept("SKIP") || p.accept("OFFSET") {
		n, err := p.count(strings.ToUpper(t.val))
		if err != nil {
			return r, err
		}
		r.skip = &n
	}
	if p.accept("LIMIT") {
		t := p.peek()
		n, err := p.count("LIMIT")
		if err != nil {
			return r, err
		}
		if n == 0 {
			return r, errAt(t.pos, "LIMIT 0 answers nothing: leave the query out")
		}
		r.limit = &n
	}
	if r.skip != nil && *r.skip == 0 {
		r.skip = nil // skips nothing, and jaala's offset is positive
	}
	return r, nil
}

// count reads the number after SKIP, LIMIT or a range's * or ... A parameter or an expression there
// is refused, counting 1 so parsing goes on.
func (p *parser) count(what string) (int, *Error) {
	t := p.peek()
	more := p.peekAt(1).kind == tSym && strings.Contains("+-*/%^", p.peekAt(1).text)
	switch {
	case t.kind == tNumber && !more:
		n, err := strconv.Atoi(t.val)
		if err != nil || n < 0 {
			return 0, errAt(t.pos, "%s takes a whole number, not %s", what, t.text)
		}
		p.i++
		return n, nil
	case t.kind == tParam && !more:
		p.refuse(gapLater.at(t.pos, "%s takes a number written in the query, not a parameter", what))
		p.i++
		return 1, nil
	case t.kind == tNumber, t.kind == tParam, t.kind == tIdent && !t.quoted && !clauseWords[strings.ToUpper(t.val)], t.sym("("):
		p.refuse(gapLater.at(t.pos, "%s takes a number written in the query, not an expression", what))
		_, err := p.additive()
		return 1, err
	}
	return 0, p.unexpected("a number after " + what)
}

// clauseWords begin a clause or a clause's part, so they end what comes before them.
var clauseWords = map[string]bool{
	"MATCH": true, "OPTIONAL": true, "WITH": true, "RETURN": true, "UNWIND": true, "CALL": true,
	"CREATE": true, "MERGE": true, "SET": true, "DELETE": true, "DETACH": true, "NODETACH": true,
	"REMOVE": true, "FOREACH": true, "USE": true, "LOAD": true, "FINISH": true, "UNION": true,
	"WHERE": true, "ORDER": true, "SKIP": true, "OFFSET": true, "LIMIT": true,
}

// pattern reads a chain of nodes and relationships. A path selector before it or a shortestPath around
// it keeps the matches it would have, since a match answers its nodes and not its paths, and a
// quantified relationship is a variable length. A path variable and a quantified path pattern are read
// and refused.
func (p *parser) pattern() (pattern, *Error) {
	t := p.peek()
	if t.kind == tIdent && p.peekAt(1).sym("=") {
		p.refuse(gapPaths.at(t.pos, "a path variable (%s = ...)", t.val))
		p.i += 2
	}
	if err := p.pathSelector(); err != nil {
		return pattern{}, err
	}
	if t = p.peek(); t.is("shortestPath") || t.is("allShortestPaths") {
		p.i++
		if err := p.expectSym("(", "( after "+t.val); err != nil {
			return pattern{}, err
		}
		pat, err := p.pattern()
		if err != nil {
			return pattern{}, err
		}
		return pat, p.expectSym(")", ") to end "+t.val)
	}
	pat := pattern{pos: p.peek().pos}
	needNode := true
	for {
		if p.peek().sym("(") && p.peekAt(1).sym("(") {
			if err := p.quantifiedPath(); err != nil {
				return pat, err
			}
			pat.refused, needNode = true, false
			if p.peek().sym("(") {
				needNode = true // a node or another quantified path written right after it
			}
			continue
		}
		if needNode {
			n, err := p.node()
			if err != nil {
				return pat, err
			}
			pat.nodes = append(pat.nodes, n)
			needNode = false
			continue
		}
		if !(p.peek().sym("-") || (p.peek().sym("<") && p.peekAt(1).sym("-"))) {
			return pat, nil
		}
		r, err := p.rel()
		if err != nil {
			return pat, err
		}
		if q := p.peek(); p.quantifier(false, &r) && r.min == -2 {
			return pat, errAt(q.pos, "a relationship has a length (*) and a quantifier: write one")
		}
		pat.rels = append(pat.rels, r)
		needNode = true
	}
}

func (p *parser) node() (nodePat, *Error) {
	n := nodePat{pos: p.peek().pos}
	if err := p.expectSym("(", "( to start a node"); err != nil {
		return n, err
	}
	if t := p.peek(); t.kind == tIdent && !t.is("WHERE") && !t.is("IS") {
		n.v = p.next().val
	}
	if t := p.peek(); t.sym(":") || t.is("IS") {
		p.i++
		labels, err := p.labels(t.pos, true)
		if err != nil {
			return n, err
		}
		n.labels = labels
	}
	if p.peek().sym("{") {
		props, err := p.props()
		if err != nil {
			return n, err
		}
		n.props = props
	} else if t := p.peek(); t.kind == tParam {
		p.refuse(gapLists.at(t.pos, "a map parameter as a node's properties"))
		p.i++
	}
	if p.accept("WHERE") {
		e, err := p.expr()
		if err != nil {
			return n, err
		}
		n.where = e
	}
	return n, p.expectSym(")", ") to end the node")
}

// labels reads a node's label expression after the : or IS at pos: a conjunction of names (A:B or
// A&B) is its labels, and anything else (| ! % or parentheses) is refused.
func (p *parser) labels(pos Pos, colons bool) ([]string, *Error) {
	names, op, err := p.labelExpr(colons)
	if op == '|' || op == '!' {
		p.refuse(gapLater.at(pos, "a label expression with | ! %% or parentheses"))
	}
	return names, err
}

// labelExpr reads a label or type expression after : or IS: its names, and how they combine: 0 for
// one name, '&' for a conjunction (A&B, or A:B where colons may), '|' for a disjunction (A|B, or A|:B
// as types are written), and '!' for anything else (a negation, %, parentheses, or & and | mixed).
func (p *parser) labelExpr(colons bool) ([]string, byte, *Error) {
	var names []string
	var op byte
	combine := func(o byte) {
		switch {
		case op == 0:
			op = o
		case op != o:
			op = '!'
		}
	}
	var factor func() *Error
	factor = func() *Error {
		switch {
		case p.acceptSym("!"):
			combine('!')
			return factor()
		case p.acceptSym("%"):
			combine('!')
			return nil
		case p.acceptSym("("):
			ns, _, err := p.labelExpr(colons)
			if err != nil {
				return err
			}
			names = append(names, ns...)
			combine('!')
			return p.expectSym(")", ") to end the label expression")
		}
		n, err := p.name("a label after :")
		if err != nil {
			return err
		}
		names = append(names, n)
		return nil
	}
	if err := factor(); err != nil {
		return nil, 0, err
	}
	for {
		t := p.peek()
		switch {
		case t.sym("&"), colons && t.sym(":") && !p.peekAt(1).sym(":"):
			p.i++
			combine('&')
		case t.sym("|") && !p.peekAt(1).sym("|"):
			p.i++
			p.acceptSym(":")
			combine('|')
		default:
			return names, op, nil
		}
		if err := factor(); err != nil {
			return nil, 0, err
		}
	}
}

func (p *parser) rel() (relPat, *Error) {
	r := relPat{pos: p.peek().pos, dir: 0}
	left := false
	if p.acceptSym("<") {
		left = true
	}
	if err := p.expectSym("-", "- in a relationship"); err != nil {
		return r, err
	}
	if p.acceptSym("[") {
		if t := p.peek(); t.kind == tIdent && !t.is("WHERE") && !t.is("IS") {
			r.v = p.next().val
		}
		if t := p.peek(); t.sym(":") || t.is("IS") {
			p.i++
			types, op, err := p.labelExpr(false)
			if err != nil {
				return r, err
			}
			if op == '&' || op == '!' {
				p.refuse(gapLater.at(t.pos, "a relationship type expression with & ! %% or parentheses"))
			}
			r.types = types
		}
		if p.acceptSym("*") {
			r.varLen, r.min, r.max = true, 1, -1
			if t := p.peek(); t.kind == tNumber {
				n, err := p.count("*")
				if err != nil {
					return r, err
				}
				r.min, r.max = n, n
			}
			if p.acceptSym("..") {
				r.max = -1
				if t := p.peek(); t.kind == tNumber {
					n, err := p.count("..")
					if err != nil {
						return r, err
					}
					r.max = n
				}
			}
		}
		if p.peek().sym("{") {
			props, err := p.props()
			if err != nil {
				return r, err
			}
			r.props = props
		} else if t := p.peek(); t.kind == tParam {
			p.refuse(gapLists.at(t.pos, "a map parameter as a relationship's properties"))
			p.i++
		}
		if p.accept("WHERE") {
			e, err := p.expr()
			if err != nil {
				return r, err
			}
			r.where = e
		}
		if err := p.expectSym("]", "] to end the relationship"); err != nil {
			return r, err
		}
	}
	if err := p.expectSym("-", "- to end the relationship"); err != nil {
		return r, err
	}
	right := p.acceptSym(">")
	switch {
	case left && right:
		r.dir = 0
	case left:
		r.dir = -1
	case right:
		r.dir = 1
	}
	return r, nil
}

func (p *parser) props() ([]propEntry, *Error) {
	p.next() // {
	var out []propEntry
	if p.acceptSym("}") {
		return out, nil
	}
	for {
		t := p.peek()
		k, err := p.name("a property name")
		if err != nil {
			return nil, err
		}
		if err := p.expectSym(":", ": after the property name"); err != nil {
			return nil, err
		}
		v, err := p.expr()
		if err != nil {
			return nil, err
		}
		out = append(out, propEntry{pos: t.pos, key: k, val: v})
		if !p.acceptSym(",") {
			break
		}
	}
	return out, p.expectSym("}", "} to end the properties")
}

func (p *parser) expr() (expr, *Error) { return p.or() }

func (p *parser) or() (expr, *Error) {
	l, err := p.xor()
	for err == nil && p.peek().is("OR") {
		t := p.next()
		var r expr
		if r, err = p.xor(); err == nil {
			l = binary{pos: t.pos, op: "OR", l: l, r: r}
		}
	}
	return l, err
}

func (p *parser) xor() (expr, *Error) {
	l, err := p.and()
	for err == nil && p.peek().is("XOR") {
		t := p.next()
		var r expr
		if r, err = p.and(); err == nil {
			l = binary{pos: t.pos, op: "XOR", l: l, r: r}
		}
	}
	return l, err
}

func (p *parser) and() (expr, *Error) {
	l, err := p.not()
	for err == nil && p.peek().is("AND") {
		t := p.next()
		var r expr
		if r, err = p.not(); err == nil {
			l = binary{pos: t.pos, op: "AND", l: l, r: r}
		}
	}
	return l, err
}

func (p *parser) not() (expr, *Error) {
	if t := p.peek(); t.is("NOT") {
		p.i++
		e, err := p.not()
		return negate{pos: t.pos, e: e}, err
	}
	return p.comparison()
}

var comparisons = []string{"=", "<>", "!=", "<=", ">=", "<", ">", "=~"}

func (p *parser) comparison() (expr, *Error) {
	l, err := p.additive()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		op := ""
		for _, c := range comparisons {
			if t.sym(c) {
				op = c
			}
		}
		switch {
		case op != "":
			p.i++
		case t.is("STARTS") && p.peekAt(1).is("WITH"), t.is("ENDS") && p.peekAt(1).is("WITH"):
			p.i += 2
			op = strings.ToUpper(t.val) + " WITH"
		case t.is("CONTAINS"), t.is("IN"):
			p.i++
			op = strings.ToUpper(t.val)
		case t.is("IS"):
			p.i++
			if l, err = p.isTest(t, l); err != nil {
				return nil, err
			}
			continue
		default:
			return l, nil
		}
		r, err := p.additive()
		if err != nil {
			return nil, err
		}
		l = binary{pos: t.pos, op: op, l: l, r: r}
	}
}

// isTest reads what follows IS: [NOT] NULL, or a label of a variable (n IS Person), and the type and
// normalization tests, refused.
func (p *parser) isTest(is token, l expr) (expr, *Error) {
	not := p.accept("NOT")
	t := p.peek()
	switch {
	case p.accept("NULL"):
		return isNull{pos: is.pos, e: l, not: not}, nil
	case t.sym(":") && p.peekAt(1).sym(":"), t.is("TYPED"):
		return p.typeTest(is.pos)
	case t.is("NORMALIZED"), (t.is("NFC") || t.is("NFD") || t.is("NFKC") || t.is("NFKD")) && p.peekAt(1).is("NORMALIZED"):
		if !p.accept("NORMALIZED") {
			p.i += 2
		}
		return p.refuse(gapFunctions.at(is.pos, "IS NORMALIZED")), nil
	case t.kind == tIdent || t.sym("!") || t.sym("%") || t.sym("("):
		v, ok := l.(varRef)
		if !ok {
			break
		}
		labels, err := p.labels(t.pos, true)
		if err != nil {
			return nil, err
		}
		var e expr = hasLabel{pos: v.pos, v: v.name, labels: labels}
		if not {
			e = negate{pos: is.pos, e: e}
		}
		return e, nil
	}
	return nil, p.unexpected("NULL after IS")
}

func (p *parser) additive() (expr, *Error) {
	l, err := p.multiplicative()
	for err == nil {
		t := p.peek()
		if t.sym("|") && p.peekAt(1).sym("|") && p.peekAt(1).pos.Offset == t.pos.Offset+1 {
			p.i += 2
			if _, err = p.multiplicative(); err == nil {
				l = p.refuse(gapFunctions.at(t.pos, "|| (join text with +)"))
			}
			continue
		}
		if !t.sym("+") && !t.sym("-") {
			break
		}
		p.i++
		var r expr
		if r, err = p.multiplicative(); err == nil {
			l = binary{pos: t.pos, op: t.text, l: l, r: r}
		}
	}
	return l, err
}

func (p *parser) multiplicative() (expr, *Error) {
	l, err := p.unary()
	for err == nil && (p.peek().sym("*") || p.peek().sym("/") || p.peek().sym("%") || p.peek().sym("^")) {
		t := p.next()
		var r expr
		if r, err = p.unary(); err == nil {
			l = binary{pos: t.pos, op: t.text, l: l, r: r}
		}
	}
	return l, err
}

func (p *parser) unary() (expr, *Error) {
	if t := p.peek(); t.sym("-") {
		p.i++
		e, err := p.unary()
		return minus{pos: t.pos, e: e}, err
	}
	if t := p.peek(); t.sym("+") {
		p.i++
		return p.unary()
	}
	return p.postfix()
}

func (p *parser) postfix() (expr, *Error) {
	e, err := p.atom()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		switch {
		case t.sym(":") && p.peekAt(1).sym(":"):
			if e, err = p.typeTest(t.pos); err != nil {
				return nil, err
			}
		case t.sym("."):
			v, ok := e.(varRef)
			if !ok {
				var refusal *Error
				if pr, ok := e.(propRef); ok {
					refusal = gapLists.at(t.pos, "a property of %s.%s (a map value)", pr.v, pr.key)
				} else {
					refusal = gapLists.at(t.pos, "a property can only be read from a variable")
				}
				p.i++
				if _, err := p.name("a property name after ."); err != nil {
					return nil, err
				}
				e = p.refuse(refusal)
				continue
			}
			p.i++
			k, err := p.name("a property name after .")
			if err != nil {
				return nil, err
			}
			e = propRef{pos: v.pos, v: v.name, key: k}
		case t.sym(":"):
			v, ok := e.(varRef)
			if !ok {
				return e, nil
			}
			p.i++
			labels, err := p.labels(t.pos, true)
			if err != nil {
				return nil, err
			}
			e = hasLabel{pos: v.pos, v: v.name, labels: labels}
		case t.sym("["):
			idx, index, err := p.subscript()
			if err != nil {
				return nil, err
			}
			if c, ok := e.(call); ok && index && strings.EqualFold(c.name, "split") && len(c.args) == 2 {
				e = call{pos: c.pos, name: splitIndex, args: append(c.args, idx)}
				continue
			}
			e = p.refuse(gapLists.at(t.pos, "indexing or slicing a value"))
		case t.sym("{"):
			if _, ok := e.(varRef); !ok {
				return e, nil
			}
			if err := p.mapProjection(); err != nil {
				return nil, err
			}
			e = p.refuse(gapLists.at(t.pos, "a map projection"))
		default:
			return e, nil
		}
	}
}

// splitIndex is split(s, sep)[i], the part str.split answers at an index.
const splitIndex = "split[]"

func (p *parser) atom() (expr, *Error) {
	t := p.peek()
	switch t.kind {
	case tString:
		p.i++
		return strLit{pos: t.pos, s: t.val}, nil
	case tNumber:
		p.i++
		return numLit{pos: t.pos, text: t.val}, nil
	case tParam:
		p.i++
		return paramRef{pos: t.pos, name: t.val}, nil
	case tIdent:
		switch {
		case t.quoted:
		case t.is("true"), t.is("false"):
			p.i++
			return unsupported{pos: t.pos, err: errAt(t.pos, "jaala has no booleans: compare a property with the value the graph stores")}, nil
		case t.is("null"):
			p.i++
			return unsupported{pos: t.pos, err: errAt(t.pos, "null never equals anything: write IS NULL or IS NOT NULL")}, nil
		case t.is("CASE"):
			return p.caseExpr()
		case t.is("COLLECT") && p.peekAt(1).sym("{"):
			p.i++
			e := p.refuse(gapLists.at(t.pos, "a COLLECT { } subquery"))
			if !p.peekAt(1).sym("(") {
				return e, p.braced()
			}
			p.i++ // {, and patterns with a WHERE, as EXISTS { } takes them
			if _, err := p.patterns(); err != nil {
				return nil, err
			}
			if p.accept("WHERE") {
				if _, err := p.expr(); err != nil {
					return nil, err
				}
			}
			return e, p.expectSym("}", "} to end the subquery")
		case t.is("EXISTS") && p.peekAt(1).sym("{"), t.is("COUNT") && p.peekAt(1).sym("{"):
			return p.subquery()
		case t.is("shortestPath"), t.is("allShortestPaths"):
			if _, err := p.pattern(); err != nil {
				return nil, err
			}
			return p.refuse(gapPaths.at(t.pos, "%s as a value, a path", t.val)), nil
		}
		if p.callAhead() {
			return p.call()
		}
		p.i++
		return varRef{pos: t.pos, name: t.val}, nil
	case tSym:
		switch t.text {
		case "(":
			save, refused := p.i, p.refused
			if pat, err := p.pattern(); err == nil && (len(pat.rels) > 0 || pat.refused) {
				if ws := inlineWheres([]pattern{pat}); len(ws) > 0 {
					p.refuse(gapLater.at(ws[0].at(), "a WHERE inside a pattern in an expression"))
				}
				return patExpr{pos: t.pos, p: pat}, nil
			}
			p.i, p.refused = save+1, refused
			e, err := p.expr()
			if err != nil {
				return nil, err
			}
			return e, p.expectSym(")", ")")
		case "[":
			return p.list()
		case "{":
			return p.refuse(gapLists.at(t.pos, "a map")), p.mapLiteral()
		}
	}
	return nil, p.unexpected("an expression")
}

// callAhead says whether the name at p starts a call: name( or a namespaced name.name...(.
func (p *parser) callAhead() bool {
	n := 1
	for p.peekAt(n).sym(".") && p.peekAt(n+1).kind == tIdent {
		n += 2
	}
	return p.peekAt(n).sym("(")
}

// list reads a list literal, or a list or pattern comprehension, refused.
func (p *parser) list() (expr, *Error) {
	t := p.next() // [
	if p.peek().kind == tIdent && p.peekAt(1).is("IN") {
		p.i += 2
		if _, err := p.expr(); err != nil {
			return nil, err
		}
		return p.refuse(gapLists.at(t.pos, "a list comprehension")), p.comprehensionTail(false)
	}
	if p.peek().sym("(") || (p.peek().kind == tIdent && p.peekAt(1).sym("=")) {
		save, refused := p.i, p.refused
		if pat, err := p.pattern(); err == nil && (len(pat.rels) > 0 || pat.refused) && (p.peek().is("WHERE") || p.peek().sym("|")) {
			p.refused = refused
			return p.refuse(gapLists.at(t.pos, "a pattern comprehension")), p.comprehensionTail(true)
		}
		p.i, p.refused = save, refused
	}
	l := listLit{pos: t.pos}
	if p.acceptSym("]") {
		return l, nil
	}
	items, err := p.exprs()
	if err != nil {
		return nil, err
	}
	l.items = items
	return l, p.expectSym("]", "] to end the list")
}

// subquery reads EXISTS { [MATCH] pattern, ... [WHERE expr] } or COUNT { ... }, and one of several
// clauses, refused.
func (p *parser) subquery() (expr, *Error) {
	t := p.next()
	p.next() // {
	body := p.i
	sq := subquery{pos: t.pos, count: t.is("COUNT")}
	p.accept("MATCH")
	pats, err := p.patterns()
	if err != nil {
		return nil, err
	}
	sq.patterns = pats
	if p.accept("WHERE") {
		e, err := p.expr()
		if err != nil {
			return nil, err
		}
		sq.where = e
	}
	sq.where = andAll(append(inlineWheres(sq.patterns), sq.where)...)
	if c := p.peek(); c.kind == tIdent && !c.quoted && clauseWords[strings.ToUpper(c.val)] {
		p.refuse(gapLater.at(c.pos, "a %s { } subquery of several clauses", strings.ToUpper(sq.kind())))
		p.i = body
		p.braces++
		_, err := p.union()
		p.braces--
		if err != nil {
			return nil, err
		}
	}
	return sq, p.expectSym("}", "} to end the subquery")
}

func (p *parser) call() (expr, *Error) {
	t := p.next()
	name := t.val
	for p.acceptSym(".") {
		n, err := p.name("a function name")
		if err != nil {
			return nil, err
		}
		name += "." + n
	}
	c := call{pos: t.pos, name: name}
	p.next() // (
	if e, ok, err := p.listFunction(t, name); ok {
		return e, err
	}
	if p.acceptSym("*") {
		c.star = true
		return c, p.expectSym(")", ") after *")
	}
	c.distinct = p.accept("DISTINCT")
	if p.acceptSym(")") {
		return c, nil
	}
	args, err := p.exprs()
	if err != nil {
		return nil, err
	}
	c.args = args
	return c, p.expectSym(")", ") to end the call")
}
