package cypher

import (
	"strconv"
	"strings"
)

// The subset parse reads (EBNF, keywords in any case):
//
//	statement = query { "UNION" [ "ALL" ] query } [ ";" ] ;
//	query     = { reading } { "WITH" items [ "WHERE" expr ] { reading } } return ;   (* reading first *)
//	reading   = match | call ;
//	match     = "MATCH" pattern { "," pattern } [ "WHERE" expr ] ;
//	call      = "CALL" name "(" [ expr { "," expr } ] ")" [ "YIELD" ( "*" | name [ "AS" name ] { "," ... } ) [ "WHERE" expr ] ] ;
//	pattern   = node { rel node } ;
//	node      = "(" [ name ] { ":" name } [ props ] ")" ;
//	rel       = ( "-" | "<-" ) [ "[" [ name ] [ ":" name { "|" [ ":" ] name } ] [ range ] [ props ] "]" ] ( "-" | "->" ) ;
//	range     = "*" [ int ] [ ".." [ int ] ] ;
//	props     = "{" name ":" expr { "," name ":" expr } "}" ;
//	return    = "RETURN" [ "DISTINCT" ] ( "*" | item { "," item } )
//	            [ "ORDER" "BY" expr [ "ASC" | "DESC" ] { "," ... } ] [ "SKIP" int ] [ "LIMIT" int ] ;
//	item      = expr [ "AS" name ] ;
//
// Expressions follow openCypher's precedence: OR, XOR, AND, NOT, comparison (with the string tests,
// IN and IS NULL), + and -, * / and %, unary minus, then a property, a label test, a call (split's
// [i] too), EXISTS { } or COUNT { } (an optional MATCH, patterns and a WHERE), a literal, a
// parameter, a parenthesized expression or a pattern. It also reads what it refuses, so a refusal
// names the clause or the gap rather than failing to parse: OPTIONAL MATCH, UNWIND, CALL { }, the
// writing clauses, a path variable, shortestPath, lists, maps, COLLECT { } and CASE.
type parser struct {
	toks []token
	i    int
	text string
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
	return st, nil
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

// refusedClauses are clause keywords outside the subset, by the gap or ticket that would take them.
var refusedClauses = map[string]gap{
	"UNWIND": gapLists, "FOREACH": gapLists,
	"CREATE": gapWrites, "MERGE": gapWrites, "SET": gapWrites, "DELETE": gapWrites, "DETACH": gapWrites, "REMOVE": gapWrites,
	"USE": gapGraphs,
}

func (p *parser) refuseClause() *Error {
	t := p.peek()
	if t.is("OPTIONAL") {
		return gapOptional.at(t.pos, "write the match without OPTIONAL, or as a second query")
	}
	for kw, g := range refusedClauses {
		if t.is(kw) {
			return g.at(t.pos, "%s", strings.ToUpper(t.val))
		}
	}
	return nil
}

func (p *parser) statement() (statement, *Error) {
	var st statement
	for {
		q, err := p.query()
		if err != nil {
			return st, err
		}
		st.parts = append(st.parts, q)
		if !p.accept("UNION") {
			break
		}
		p.accept("ALL")
	}
	p.acceptSym(";")
	if t := p.peek(); t.kind != tEOF {
		return st, errAt(t.pos, "unexpected %q after RETURN", t.text)
	}
	return st, nil
}

// query reads one UNION part: stages of MATCH and CALL clauses, each closed by a WITH, the last by
// RETURN.
func (p *parser) query() (query, *Error) {
	q := query{pos: p.peek().pos}
	var cur stage
	for {
		if err := p.refuseClause(); err != nil {
			return q, err
		}
		t := p.peek()
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
			if p.peek().sym("{") {
				return q, gapLater.at(t.pos, "a CALL { } subquery")
			}
			cc, err := p.callClause(t.pos)
			if err != nil {
				return q, err
			}
			cur.clauses = append(cur.clauses, readClause{call: &cc})
		case t.is("WITH"), t.is("RETURN"):
			if len(q.stages) == 0 && len(cur.clauses) == 0 {
				return q, p.unexpected("MATCH")
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
	for {
		pat, err := p.pattern(true)
		if err != nil {
			return m, err
		}
		m.patterns = append(m.patterns, pat)
		if !p.acceptSym(",") {
			break
		}
	}
	if p.accept("WHERE") {
		e, err := p.expr()
		if err != nil {
			return m, err
		}
		m.where = e
	}
	return m, nil
}

// callClause reads a procedure call after CALL: name(args) [YIELD name [AS alias], ... | *] [WHERE].
func (p *parser) callClause(pos Pos) (callClause, *Error) {
	cc := callClause{pos: pos}
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
	if err := p.expectSym("(", "( after the procedure's name"); err != nil {
		return cc, err
	}
	if !p.acceptSym(")") {
		for {
			e, err := p.expr()
			if err != nil {
				return cc, err
			}
			cc.args = append(cc.args, e)
			if !p.acceptSym(",") {
				break
			}
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
	} else {
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
	if p.accept("SKIP") {
		n, err := p.count("SKIP")
		if err != nil {
			return r, err
		}
		r.skip = &n
	}
	if p.accept("LIMIT") {
		n, err := p.count("LIMIT")
		if err != nil {
			return r, err
		}
		r.limit = &n
	}
	return r, nil
}

func (p *parser) count(what string) (int, *Error) {
	t := p.peek()
	if t.kind == tParam {
		return 0, errAt(t.pos, "%s takes a number written in the query, not a parameter", what)
	}
	if t.kind != tNumber {
		return 0, p.unexpected("a number after " + what)
	}
	n, err := strconv.Atoi(t.text)
	if err != nil || n < 0 {
		return 0, errAt(t.pos, "%s takes a whole number, not %s", what, t.text)
	}
	p.i++
	return n, nil
}

// pattern reads a chain of nodes and relationships. top is true in a MATCH, where a path variable or
// shortestPath can appear, so it is refused by name.
func (p *parser) pattern(top bool) (pattern, *Error) {
	t := p.peek()
	if top && t.kind == tIdent && p.peekAt(1).sym("=") {
		return pattern{}, gapPaths.at(t.pos, "a path variable (%s = ...)", t.val)
	}
	if t.is("shortestPath") || t.is("allShortestPaths") {
		return pattern{}, gapShortest.at(t.pos, "%s", t.val)
	}
	pat := pattern{pos: t.pos}
	n, err := p.node()
	if err != nil {
		return pat, err
	}
	pat.nodes = append(pat.nodes, n)
	for p.peek().sym("-") || (p.peek().sym("<") && p.peekAt(1).sym("-")) {
		r, err := p.rel()
		if err != nil {
			return pat, err
		}
		n, err := p.node()
		if err != nil {
			return pat, err
		}
		pat.rels = append(pat.rels, r)
		pat.nodes = append(pat.nodes, n)
	}
	return pat, nil
}

func (p *parser) node() (nodePat, *Error) {
	n := nodePat{pos: p.peek().pos}
	if err := p.expectSym("(", "( to start a node"); err != nil {
		return n, err
	}
	if p.peek().kind == tIdent {
		n.v = p.next().val
	}
	for p.acceptSym(":") {
		l, err := p.name("a label after :")
		if err != nil {
			return n, err
		}
		n.labels = append(n.labels, l)
	}
	if p.peek().sym("{") {
		props, err := p.props()
		if err != nil {
			return n, err
		}
		n.props = props
	}
	if p.peek().kind == tParam {
		return n, gapLists.at(p.peek().pos, "a map parameter as a node's properties")
	}
	return n, p.expectSym(")", ") to end the node")
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
		if t := p.peek(); t.kind == tIdent {
			r.v = p.next().val
		}
		if p.acceptSym(":") {
			for {
				ty, err := p.name("a relationship type")
				if err != nil {
					return r, err
				}
				r.types = append(r.types, ty)
				if !p.acceptSym("|") {
					break
				}
				p.acceptSym(":")
			}
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
			not := p.accept("NOT")
			if !p.accept("NULL") {
				return nil, p.unexpected("NULL after IS")
			}
			l = isNull{pos: t.pos, e: l, not: not}
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

func (p *parser) additive() (expr, *Error) {
	l, err := p.multiplicative()
	for err == nil && (p.peek().sym("+") || p.peek().sym("-")) {
		t := p.next()
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
		case t.sym("."):
			v, ok := e.(varRef)
			if !ok {
				if pr, ok := e.(propRef); ok {
					return nil, gapLists.at(t.pos, "a property of %s.%s (a map value)", pr.v, pr.key)
				}
				return nil, errAt(t.pos, "a property can only be read from a variable")
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
			h := hasLabel{pos: v.pos, v: v.name}
			for p.acceptSym(":") {
				l, err := p.name("a label after :")
				if err != nil {
					return nil, err
				}
				h.labels = append(h.labels, l)
			}
			e = h
		case t.sym("["):
			if c, ok := e.(call); ok && strings.EqualFold(c.name, "split") && len(c.args) == 2 {
				p.i++
				idx, err := p.expr()
				if err != nil {
					return nil, err
				}
				if err := p.expectSym("]", "] after the index"); err != nil {
					return nil, err
				}
				e = call{pos: c.pos, name: splitIndex, args: append(c.args, idx)}
				continue
			}
			return nil, gapLists.at(t.pos, "indexing or slicing a value")
		case t.sym("{"):
			if _, ok := e.(varRef); ok {
				return nil, gapLists.at(t.pos, "a map projection")
			}
			return e, nil
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
		return numLit{pos: t.pos, text: t.text}, nil
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
			return nil, gapFunctions.at(t.pos, "CASE")
		case t.is("COLLECT") && p.peekAt(1).sym("{"):
			return nil, gapLists.at(t.pos, "a COLLECT { } subquery")
		case t.is("EXISTS") && p.peekAt(1).sym("{"), t.is("COUNT") && p.peekAt(1).sym("{"):
			return p.subquery()
		case t.is("shortestPath"), t.is("allShortestPaths"):
			return nil, gapShortest.at(t.pos, "%s", t.val)
		}
		if p.peekAt(1).sym("(") || (p.peekAt(1).sym(".") && p.peekAt(2).kind == tIdent && p.peekAt(3).sym("(")) {
			return p.call()
		}
		p.i++
		return varRef{pos: t.pos, name: t.val}, nil
	case tSym:
		switch t.text {
		case "(":
			save := p.i
			if pat, err := p.pattern(false); err == nil && len(pat.rels) > 0 {
				return patExpr{pos: t.pos, p: pat}, nil
			}
			p.i = save + 1
			e, err := p.expr()
			if err != nil {
				return nil, err
			}
			return e, p.expectSym(")", ")")
		case "[":
			p.i++
			l := listLit{pos: t.pos}
			if p.acceptSym("]") {
				return l, nil
			}
			for {
				e, err := p.expr()
				if err != nil {
					return nil, err
				}
				l.items = append(l.items, e)
				if !p.acceptSym(",") {
					break
				}
			}
			return l, p.expectSym("]", "] to end the list")
		case "{":
			return nil, gapLists.at(t.pos, "a map")
		}
	}
	return nil, p.unexpected("an expression")
}

// subquery reads EXISTS { [MATCH] pattern, ... [WHERE expr] } or COUNT { ... }.
func (p *parser) subquery() (expr, *Error) {
	t := p.next()
	p.next() // {
	sq := subquery{pos: t.pos, count: t.is("COUNT")}
	p.accept("MATCH")
	for {
		pat, err := p.pattern(false)
		if err != nil {
			return nil, err
		}
		sq.patterns = append(sq.patterns, pat)
		if !p.acceptSym(",") {
			break
		}
	}
	if p.accept("WHERE") {
		e, err := p.expr()
		if err != nil {
			return nil, err
		}
		sq.where = e
	}
	if t := p.peek(); t.is("RETURN") || t.is("WITH") || t.is("MATCH") {
		return nil, gapLater.at(t.pos, "a %s { } subquery of several clauses", strings.ToUpper(sq.kind()))
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
	if p.acceptSym("*") {
		c.star = true
		return c, p.expectSym(")", ") after *")
	}
	c.distinct = p.accept("DISTINCT")
	if p.acceptSym(")") {
		return c, nil
	}
	for {
		e, err := p.expr()
		if err != nil {
			return nil, err
		}
		c.args = append(c.args, e)
		if !p.acceptSym(",") {
			break
		}
	}
	return c, p.expectSym(")", ") to end the call")
}
