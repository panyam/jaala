package cypher

import (
	"strconv"
	"strings"
)

// What parse reads only to refuse it (#382): each function here reads a construct's whole syntax and
// records a refusal naming its gap, so the rest of the text is still checked. Backfilling a gap moves
// the construct's function to parse.go, building a node where it records the refusal.

// refusedClauses are clause keywords outside the subset, by the gap or ticket that would take them.
var refusedClauses = map[string]gap{
	"UNWIND": gapLists, "FOREACH": gapLists,
	"CREATE": gapWrites, "MERGE": gapWrites, "SET": gapWrites, "DELETE": gapWrites, "DETACH": gapWrites,
	"NODETACH": gapWrites, "REMOVE": gapWrites,
	"USE": gapGraphs,
}

// refusedClause reads a clause outside the subset at p, recording its refusal. handled says there
// was one, and ends that a query may end after it: a write, or FINISH.
func (p *parser) refusedClause() (handled, ends bool, err *Error) {
	t := p.peek()
	if t.kind != tIdent || t.quoted {
		return false, false, nil
	}
	word := strings.ToUpper(t.val)
	switch {
	case t.is("UNWIND"):
		p.refuse(gapLists.at(t.pos, "UNWIND"))
		p.i++
		if _, err := p.expr(); err != nil {
			return true, false, err
		}
		if err := p.expect("AS", "AS after UNWIND's list"); err != nil {
			return true, false, err
		}
		_, err := p.name("a name after AS")
		return true, false, err
	case t.is("USE"):
		p.refuse(gapGraphs.at(t.pos, "USE"))
		p.i++
		_, err := p.postfix()
		return true, false, err
	case t.is("LOAD") && p.peekAt(1).is("CSV"):
		p.refuse(gapGraphs.at(t.pos, "LOAD CSV"))
		return true, false, p.loadCSV()
	case t.is("FINISH"):
		p.refuse(errAt(t.pos, "FINISH answers nothing: end the query with RETURN"))
		p.i++
		return true, true, nil
	case refusedClauses[word] != gap{}:
		p.refuse(refusedClauses[word].at(t.pos, "%s", word))
		return true, true, p.updating()
	}
	return false, false, nil
}

// updating reads a writing clause: CREATE, MERGE (with its ON CREATE and ON MATCH), SET, REMOVE,
// [DETACH | NODETACH] DELETE or FOREACH.
func (p *parser) updating() *Error {
	t := p.next()
	switch {
	case t.is("CREATE"):
		_, err := p.patterns()
		return err
	case t.is("MERGE"):
		if _, err := p.pattern(); err != nil {
			return err
		}
		for p.peek().is("ON") && (p.peekAt(1).is("CREATE") || p.peekAt(1).is("MATCH")) {
			p.i += 2
			if err := p.expect("SET", "SET after ON CREATE or ON MATCH"); err != nil {
				return err
			}
			if err := p.setItems(); err != nil {
				return err
			}
		}
		return nil
	case t.is("SET"):
		return p.setItems()
	case t.is("REMOVE"):
		for {
			if _, err := p.postfix(); err != nil {
				return err
			}
			if !p.acceptSym(",") {
				return nil
			}
		}
	case t.is("DETACH"), t.is("NODETACH"):
		if err := p.expect("DELETE", "DELETE after "+strings.ToUpper(t.val)); err != nil {
			return err
		}
		fallthrough
	case t.is("DELETE"):
		_, err := p.exprs()
		return err
	case t.is("FOREACH"):
		if err := p.expectSym("(", "( after FOREACH"); err != nil {
			return err
		}
		if _, err := p.name("a name after FOREACH ("); err != nil {
			return err
		}
		if err := p.expect("IN", "IN after FOREACH's name"); err != nil {
			return err
		}
		if _, err := p.expr(); err != nil {
			return err
		}
		if err := p.expectSym("|", "| after FOREACH's list"); err != nil {
			return err
		}
		for !p.acceptSym(")") {
			if w := p.peek(); w.kind != tIdent || !(w.is("FOREACH") || refusedClauses[strings.ToUpper(w.val)] == gapWrites) {
				return p.unexpected("a write, or ) to end FOREACH")
			}
			if err := p.updating(); err != nil {
				return err
			}
		}
		return nil
	}
	return p.unexpected("a write")
}

// setItems reads SET's items: a property, a variable (= or +=) or a dynamic property set to a value,
// or labels added (n:Label, n IS Label).
func (p *parser) setItems() *Error {
	for {
		lhs, err := p.postfix()
		if err != nil {
			return err
		}
		if _, ok := lhs.(hasLabel); !ok {
			if p.accept("IS") {
				if _, _, err := p.labelExpr(true); err != nil {
					return err
				}
			} else {
				p.acceptSym("+") // +=
				if err := p.expectSym("=", "= in SET"); err != nil {
					return err
				}
				if _, err := p.expr(); err != nil {
					return err
				}
			}
		}
		if !p.acceptSym(",") {
			return nil
		}
	}
}

// loadCSV reads LOAD CSV [WITH HEADERS] FROM url AS name [FIELDTERMINATOR text].
func (p *parser) loadCSV() *Error {
	p.i += 2
	if p.accept("WITH") {
		if err := p.expect("HEADERS", "HEADERS after WITH"); err != nil {
			return err
		}
	}
	if err := p.expect("FROM", "FROM after LOAD CSV"); err != nil {
		return err
	}
	if _, err := p.expr(); err != nil {
		return err
	}
	if err := p.expect("AS", "AS after LOAD CSV's source"); err != nil {
		return err
	}
	if _, err := p.name("a name after AS"); err != nil {
		return err
	}
	if p.accept("FIELDTERMINATOR") {
		if t := p.next(); t.kind != tString {
			p.i--
			return p.unexpected("a string after FIELDTERMINATOR")
		}
	}
	return nil
}

// callSubquery reads a CALL subquery after CALL: [( names | * )] { query } [IN TRANSACTIONS ...].
func (p *parser) callSubquery() *Error {
	if p.acceptSym("(") && !p.acceptSym("*") && !p.peek().sym(")") {
		for {
			if _, err := p.name("a variable the subquery imports"); err != nil {
				return err
			}
			if !p.acceptSym(",") {
				break
			}
		}
	}
	p.acceptSym(")")
	if err := p.braced(); err != nil {
		return err
	}
	return p.inTransactions()
}

// braced reads { query { UNION query } }, a subquery's body, where a query may end without RETURN.
func (p *parser) braced() *Error {
	if err := p.expectSym("{", "{ to start the subquery"); err != nil {
		return err
	}
	p.braces++
	_, err := p.union()
	p.braces--
	if err != nil {
		return err
	}
	return p.expectSym("}", "} to end the subquery")
}

// inTransactions reads what may follow a CALL { }: IN [n CONCURRENT] TRANSACTIONS [OF n ROWS]
// [ON ERROR ...] [REPORT STATUS AS name], refused as a write.
func (p *parser) inTransactions() *Error {
	t := p.peek()
	if !t.is("IN") {
		return nil
	}
	p.i++
	p.refuse(gapWrites.at(t.pos, "IN TRANSACTIONS"))
	if !p.peek().is("TRANSACTIONS") && !p.peek().is("CONCURRENT") {
		if _, err := p.additive(); err != nil {
			return err
		}
	}
	p.accept("CONCURRENT")
	if err := p.expect("TRANSACTIONS", "TRANSACTIONS after IN"); err != nil {
		return err
	}
	if p.accept("OF") {
		if _, err := p.additive(); err != nil {
			return err
		}
		if !p.accept("ROWS") && !p.accept("ROW") {
			return p.unexpected("ROWS after OF")
		}
	}
	if p.accept("ON") {
		if err := p.expect("ERROR", "ERROR after ON"); err != nil {
			return err
		}
		if p.accept("RETRY") {
			if p.accept("FOR") {
				if _, err := p.additive(); err != nil {
					return err
				}
				if !p.accept("SECONDS") && !p.accept("SECOND") {
					return p.unexpected("SECONDS")
				}
			}
			if !p.accept("THEN") {
				return p.reportStatus()
			}
		}
		if !p.accept("CONTINUE") && !p.accept("BREAK") && !p.accept("FAIL") {
			return p.unexpected("CONTINUE, BREAK or FAIL after ON ERROR")
		}
	}
	return p.reportStatus()
}

func (p *parser) reportStatus() *Error {
	if !p.accept("REPORT") {
		return nil
	}
	if err := p.expect("STATUS", "STATUS after REPORT"); err != nil {
		return err
	}
	if err := p.expect("AS", "AS after REPORT STATUS"); err != nil {
		return err
	}
	_, err := p.name("a name after AS")
	return err
}

// adminWords begin a command that isn't a query, and schemaWords follow CREATE in one.
var (
	adminWords = map[string]bool{"SHOW": true, "GRANT": true, "DENY": true, "REVOKE": true, "DROP": true,
		"ALTER": true, "START": true, "STOP": true, "TERMINATE": true, "ENABLE": true, "RENAME": true,
		"DEALLOCATE": true, "REALLOCATE": true, "DRYRUN": true}
	schemaWords = map[string]bool{"INDEX": true, "CONSTRAINT": true, "DATABASE": true, "COMPOSITE": true,
		"ALIAS": true, "USER": true, "ROLE": true, "OR": true, "RANGE": true, "TEXT": true, "POINT": true,
		"LOOKUP": true, "FULLTEXT": true, "VECTOR": true, "BTREE": true, "SERVER": true}
)

// prefix reads what may come before a query: CYPHER and its options, which change nothing here,
// PROFILE, which answers as the query does, and EXPLAIN, which answers nothing and is refused. A
// command that isn't a query is refused at its first word, with nothing after it read.
func (p *parser) prefix() *Error {
	for {
		t := p.peek()
		if t.kind != tIdent || t.quoted {
			return nil
		}
		switch next := p.peekAt(1); {
		case adminWords[strings.ToUpper(t.val)], t.is("CREATE") && next.kind == tIdent && schemaWords[strings.ToUpper(next.val)]:
			return errAt(t.pos, "%s is a command for the database, not a query: only a query compiles", strings.ToUpper(t.val))
		case t.is("EXPLAIN"):
			p.refuse(errAt(t.pos, "EXPLAIN plans the query without answering it: leave EXPLAIN out"))
			p.i++
		case t.is("PROFILE"):
			p.i++
		case t.is("CYPHER"):
			p.i++
			if p.peek().kind == tNumber {
				p.i++
			}
			for p.peek().kind == tIdent && p.peekAt(1).sym("=") {
				p.i += 3
			}
		default:
			return nil
		}
	}
}

// pathSelector reads a selector before a pattern (SHORTEST k, ALL SHORTEST, ANY, ANY SHORTEST, ALL
// [PATHS]). Each chooses among a pair of nodes' paths, and a match answers the nodes, which every
// selector keeps, so it compiles as the pattern alone.
func (p *parser) pathSelector() (string, *Error) {
	t := p.peek()
	if !(t.is("SHORTEST") || t.is("ALL") || t.is("ANY")) || p.peekAt(1).sym("=") {
		return "", nil
	}
	var words []string
	for {
		w := p.peek()
		switch {
		case w.is("SHORTEST"), w.is("ANY"), w.is("GROUP"), w.is("GROUPS"), w.kind == tNumber, w.is("ALL"):
			words = append(words, strings.ToUpper(w.text))
		case w.is("PATH"), w.is("PATHS"):
		default:
			if !w.sym("(") && !(w.kind == tIdent && p.peekAt(1).sym("=")) {
				return "", p.unexpected("( to start the pattern")
			}
			return selectorOf(words), nil
		}
		p.i++
	}
}

// selectorOf is what a path selector's words choose for a path variable: "shortest" for one shortest
// path per pair (SHORTEST 1, ANY SHORTEST), "" for every path (ALL), and otherwise the words, which the
// compiler refuses (ALL SHORTEST, SHORTEST 2, ANY, a GROUP).
func selectorOf(words []string) string {
	switch strings.Join(words, " ") {
	case "ALL":
		return ""
	case "SHORTEST", "SHORTEST 1", "ANY SHORTEST":
		return "shortest"
	}
	return strings.ToLower(strings.Join(words, " "))
}

// quantifiedPath reads a parenthesized path, ((a)-->(b) [WHERE expr]) and its quantifier, refused.
func (p *parser) quantifiedPath() *Error {
	t := p.next() // (
	p.refuse(gapPaths.at(t.pos, "a quantified path pattern"))
	if _, err := p.pattern(); err != nil {
		return err
	}
	if p.accept("WHERE") {
		if _, err := p.expr(); err != nil {
			return err
		}
	}
	if err := p.expectSym(")", ") to end the path pattern"); err != nil {
		return err
	}
	p.quantifier(true, nil)
	return nil
}

// quantifier reads a quantifier after a parenthesized path or a relationship (+, *, {n}, {n,}, {,m}
// or {n,m}), saying whether there was one. After a relationship, + and * count only before the next
// node, and r, when set, is given the length the quantifier says, as *min..max would; one already of a
// variable length gets min -2, which the caller refuses.
func (p *parser) quantifier(path bool, r *relPat) bool {
	t := p.peek()
	lo, hi := 0, -1
	switch {
	case (t.sym("+") || t.sym("*")) && (path || p.peekAt(1).sym("(")):
		p.i++
		if t.sym("+") {
			lo = 1
		}
	case t.sym("{"):
		n := 1
		var nums []string
		comma := false
		for p.peekAt(n).kind == tNumber || p.peekAt(n).sym(",") {
			if p.peekAt(n).sym(",") {
				comma = true
			} else if !comma {
				nums = append(nums, p.peekAt(n).val)
			} else {
				nums = append(nums, ","+p.peekAt(n).val)
			}
			n++
		}
		if n == 1 || !p.peekAt(n).sym("}") {
			return false
		}
		p.i += n + 1
		for _, s := range nums {
			v, err := strconv.Atoi(strings.TrimPrefix(s, ","))
			if err != nil {
				continue
			}
			if strings.HasPrefix(s, ",") {
				hi = v
			} else {
				lo = v
			}
		}
		if !comma {
			hi = lo
		}
	default:
		return false
	}
	if r != nil {
		if r.varLen {
			r.min = -2
			return true
		}
		r.varLen, r.min, r.max = true, lo, hi
	}
	return true
}

// typeTest reads a type predicate, x :: TYPE or x IS [NOT] TYPED TYPE (IS and NOT read already),
// refused.
func (p *parser) typeTest(pos Pos) (expr, *Error) {
	if !p.accept("TYPED") {
		p.i += 2 // ::
	}
	return p.refuse(gapFunctions.at(pos, "a type predicate (IS ::)")), p.typeName()
}

// typeWords continue a type's name of several words: SIGNED INTEGER, ZONED DATETIME, LOCAL TIME,
// TIME WITHOUT TIMEZONE, PROPERTY VALUE, ANY NODE.
var typeWords = map[string]bool{"INTEGER": true, "DATETIME": true, "TIME": true, "TIMEZONE": true,
	"WITHOUT": true, "VALUE": true, "PROPERTY": true, "NODE": true, "VERTEX": true, "RELATIONSHIP": true,
	"EDGE": true, "MAP": true}

// typeName reads a type: a name of one or more words, LIST<T> or ANY<T | U>, NOT NULL or !, and
// types joined by |.
func (p *parser) typeName() *Error {
	for {
		if _, err := p.name("a type after ::"); err != nil {
			return err
		}
		for {
			t := p.peek()
			if t.is("WITH") && p.peekAt(1).is("TIMEZONE") {
				p.i += 2
				continue
			}
			if t.kind != tIdent || t.quoted || !typeWords[strings.ToUpper(t.val)] {
				break
			}
			p.i++
		}
		if p.acceptSym("<") {
			if err := p.typeName(); err != nil {
				return err
			}
			if err := p.expectSym(">", "> to end the type"); err != nil {
				return err
			}
		}
		if p.peek().is("NOT") && p.peekAt(1).is("NULL") {
			p.i += 2
		} else {
			p.acceptSym("!")
		}
		if !p.peek().sym("|") || p.peekAt(1).sym("|") {
			return nil
		}
		p.i++
	}
}

// caseExpr reads CASE [expr] WHEN ... THEN expr ... [ELSE expr] END, refused. A simple CASE's WHEN
// may list values, or start with a comparison or IS (WHEN > 5, WHEN IS NULL).
func (p *parser) caseExpr() (expr, *Error) {
	t := p.next()
	e := p.refuse(gapFunctions.at(t.pos, "CASE"))
	if !p.peek().is("WHEN") {
		if _, err := p.expr(); err != nil {
			return nil, err
		}
	}
	if !p.peek().is("WHEN") {
		return nil, p.unexpected("WHEN in CASE")
	}
	for p.accept("WHEN") {
		for {
			w := p.peek()
			var err *Error
			switch {
			case w.kind == tSym && (w.text == "=" || w.text == "<>" || w.text == "!=" || w.text == "<" || w.text == ">" || w.text == "<=" || w.text == ">="):
				p.i++
				_, err = p.additive()
			case w.is("IS"):
				p.i++
				_, err = p.isTest(w, varRef{pos: w.pos})
			default:
				_, err = p.expr()
			}
			if err != nil {
				return nil, err
			}
			if !p.acceptSym(",") {
				break
			}
		}
		if err := p.expect("THEN", "THEN after WHEN"); err != nil {
			return nil, err
		}
		if _, err := p.expr(); err != nil {
			return nil, err
		}
	}
	if p.accept("ELSE") {
		if _, err := p.expr(); err != nil {
			return nil, err
		}
	}
	return e, p.expect("END", "END to end CASE")
}

// mapLiteral reads { key: expr, ... }, which its caller refuses.
func (p *parser) mapLiteral() *Error {
	p.next() // {
	if p.acceptSym("}") {
		return nil
	}
	for {
		if _, err := p.name("a key in the map"); err != nil {
			return err
		}
		if err := p.expectSym(":", ": after the key"); err != nil {
			return err
		}
		if _, err := p.expr(); err != nil {
			return err
		}
		if !p.acceptSym(",") {
			break
		}
	}
	return p.expectSym("}", "} to end the map")
}

// mapProjection reads n { .key, .*, key: expr, name, ... }, which its caller refuses.
func (p *parser) mapProjection() *Error {
	p.next() // {
	if p.acceptSym("}") {
		return nil
	}
	for {
		if p.acceptSym(".") {
			if !p.acceptSym("*") {
				if _, err := p.name("a property after ."); err != nil {
					return err
				}
			}
		} else {
			if _, err := p.name("a key in the map projection"); err != nil {
				return err
			}
			if p.acceptSym(":") {
				if _, err := p.expr(); err != nil {
					return err
				}
			}
		}
		if !p.acceptSym(",") {
			break
		}
	}
	return p.expectSym("}", "} to end the map projection")
}

// subscript reads [i], [i..j], [i..] or [..j] after a value, saying whether it is an index, and
// returning the index when it is.
func (p *parser) subscript() (expr, bool, *Error) {
	p.next() // [
	var idx expr
	if !p.peek().sym("..") {
		e, err := p.expr()
		if err != nil {
			return nil, false, err
		}
		idx = e
	}
	if p.acceptSym("..") {
		if !p.peek().sym("]") {
			if _, err := p.expr(); err != nil {
				return nil, false, err
			}
		}
		return nil, false, p.expectSym("]", "] to end the slice")
	}
	return idx, true, p.expectSym("]", "] after the index")
}

// comprehensionTail reads a comprehension after its list or pattern: [WHERE expr] [| expr] ], the
// | required in a pattern comprehension (projects).
func (p *parser) comprehensionTail(projects bool) *Error {
	if p.accept("WHERE") {
		if _, err := p.expr(); err != nil {
			return err
		}
	}
	if p.acceptSym("|") {
		if _, err := p.expr(); err != nil {
			return err
		}
	} else if projects {
		return p.unexpected("| in the pattern comprehension")
	}
	return p.expectSym("]", "] to end the comprehension")
}

// listFunction reads, after a call's (, the functions whose arguments aren't expressions, refused:
// all, any, none and single (x IN list WHERE predicate), and reduce(acc = init, x IN list | expr).
// ok says name was one.
func (p *parser) listFunction(t token, name string) (e expr, ok bool, err *Error) {
	switch lower := strings.ToLower(name); {
	case (lower == "all" || lower == "any" || lower == "none" || lower == "single") && p.peek().kind == tIdent && p.peekAt(1).is("IN"):
		e = p.refuse(gapLists.at(t.pos, "%s(x IN ...), a predicate over a list", lower))
		p.i += 2
		if _, err := p.expr(); err != nil {
			return nil, true, err
		}
		if p.accept("WHERE") {
			if _, err := p.expr(); err != nil {
				return nil, true, err
			}
		}
	case lower == "reduce" && p.peek().kind == tIdent && p.peekAt(1).sym("="):
		e = p.refuse(gapLists.at(t.pos, "reduce"))
		p.i += 2
		if _, err := p.expr(); err != nil {
			return nil, true, err
		}
		if err := p.expectSym(",", ", after reduce's start"); err != nil {
			return nil, true, err
		}
		if _, err := p.name("a name after ,"); err != nil {
			return nil, true, err
		}
		if err := p.expect("IN", "IN after reduce's name"); err != nil {
			return nil, true, err
		}
		if _, err := p.expr(); err != nil {
			return nil, true, err
		}
		if err := p.expectSym("|", "| after reduce's list"); err != nil {
			return nil, true, err
		}
		if _, err := p.expr(); err != nil {
			return nil, true, err
		}
	default:
		return nil, false, nil
	}
	return e, true, p.expectSym(")", ") to end the call")
}
