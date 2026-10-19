package cypher

// statement is one Cypher query: its UNION parts, each a chain of stages.
type statement struct {
	parts []query
	// edgePaths are the path variables relationships() reads, which walk a relation naming its edges.
	edgePaths map[string]bool
}

// A query is one UNION part: its stages, each closed by a WITH but the last, closed by RETURN.
type query struct {
	pos    Pos
	stages []stage
}

type stage struct {
	clauses []readClause
	proj    returnClause
}

// A readClause is a MATCH or a CALL ... YIELD, one of them set.
type readClause struct {
	match *matchClause
	call  *callClause
}

type callClause struct {
	pos    Pos
	name   string
	args   []expr
	yields []yieldItem
	star   bool
	where  expr
}

type yieldItem struct {
	pos         Pos
	name, alias string
}

type matchClause struct {
	pos      Pos
	patterns []pattern
	where    expr
}

// A pattern is a chain of nodes joined by relationships: rels[i] joins nodes[i] and nodes[i+1].
type pattern struct {
	pos   Pos
	nodes []nodePat
	rels  []relPat
	// refused is set for a pattern holding what parse refused (a quantified path, shortestPath), so
	// it is read as a pattern though its nodes and rels don't say all of it.
	refused bool
	// path is the variable p = (...) binds the pattern's path to (#280), and selector what a path
	// selector or shortestPath chose: "", "shortest", or what the compiler refuses ("all shortest").
	path     string
	pathPos  Pos
	selector string
}

type nodePat struct {
	pos    Pos
	v      string // "" for an anonymous node
	labels []string
	props  []propEntry
	where  expr // a WHERE inside the node, which parse ANDs into its MATCH's
}

type relPat struct {
	pos      Pos
	v        string
	types    []string
	props    []propEntry
	dir      int // 1 left to right, -1 right to left, 0 either
	varLen   bool
	min, max int  // with varLen: max -1 is unbounded
	where    expr // a WHERE inside the relationship, as a node's
	// path is the path variable the relationship's walk binds, with its pattern's selector, and edges
	// whether relationships() reads it (#280).
	path     string
	selector string
	edges    bool
}

type propEntry struct {
	pos Pos
	key string
	val expr
}

// A returnClause is a RETURN, or with set a WITH, whose where filters what it hands on.
type returnClause struct {
	pos      Pos
	with     bool
	where    expr
	distinct bool
	star     bool
	items    []returnItem
	order    []orderItem
	skip     *int
	limit    *int
}

type returnItem struct {
	e     expr
	alias string
	text  string // the expression as written, a column's name without an alias
}

type orderItem struct {
	e    expr
	desc bool
}

type expr interface{ at() Pos }

type (
	strLit struct {
		pos Pos
		s   string
	}
	numLit struct {
		pos  Pos
		text string
	}
	paramRef struct {
		pos  Pos
		name string
	}
	varRef struct {
		pos  Pos
		name string
	}
	propRef struct {
		pos    Pos
		v, key string
	}
	// binary is a comparison (= <> < <= > >=), a string test (STARTS WITH, ENDS WITH, CONTAINS, =~),
	// IN, arithmetic (+ - * /), or a connective (AND, OR, XOR).
	binary struct {
		pos  Pos
		op   string
		l, r expr
	}
	// negate is NOT e, and minus is -e.
	negate struct {
		pos Pos
		e   expr
	}
	minus struct {
		pos Pos
		e   expr
	}
	isNull struct {
		pos Pos
		e   expr
		not bool
	}
	call struct {
		pos            Pos
		name           string
		distinct, star bool
		args           []expr
	}
	hasLabel struct {
		pos    Pos
		v      string
		labels []string
	}
	patExpr struct {
		pos Pos
		p   pattern
	}
	listLit struct {
		pos   Pos
		items []expr
	}
	// subquery is EXISTS { ... } or COUNT { ... }: patterns and an optional WHERE.
	subquery struct {
		pos      Pos
		count    bool
		patterns []pattern
		where    expr
	}
	// unsupported is an expression parsed so it can be refused by name: one parse refuses (a map,
	// CASE), or a literal Cypher has and jaala doesn't, refused when compiled.
	unsupported struct {
		pos Pos
		err *Error
	}
)

func (e strLit) at() Pos   { return e.pos }
func (e numLit) at() Pos   { return e.pos }
func (e paramRef) at() Pos { return e.pos }
func (e varRef) at() Pos   { return e.pos }
func (e propRef) at() Pos  { return e.pos }
func (e binary) at() Pos   { return e.pos }
func (e negate) at() Pos   { return e.pos }
func (e minus) at() Pos    { return e.pos }
func (e isNull) at() Pos   { return e.pos }
func (e call) at() Pos     { return e.pos }
func (e hasLabel) at() Pos { return e.pos }
func (e patExpr) at() Pos  { return e.pos }
func (e listLit) at() Pos  { return e.pos }
func (e subquery) at() Pos { return e.pos }

func (e subquery) kind() string {
	if e.count {
		return "COUNT"
	}
	return "EXISTS"
}
func (e unsupported) at() Pos { return e.pos }

// startOf is where e's text starts: its leftmost operand for an operator written between or after
// its operands (a > b, x IS NULL), whose own position is the operator's.
func startOf(e expr) Pos {
	switch e := e.(type) {
	case binary:
		return startOf(e.l)
	case isNull:
		return startOf(e.e)
	}
	return e.at()
}
