package cypher

// statement is one Cypher query: its MATCH clauses and its RETURN.
type statement struct {
	matches []matchClause
	ret     returnClause
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
}

type nodePat struct {
	pos    Pos
	v      string // "" for an anonymous node
	labels []string
	props  []propEntry
}

type relPat struct {
	pos      Pos
	v        string
	types    []string
	props    []propEntry
	dir      int // 1 left to right, -1 right to left, 0 either
	varLen   bool
	min, max int // with varLen: max -1 is unbounded
}

type propEntry struct {
	pos Pos
	key string
	val expr
}

type returnClause struct {
	pos      Pos
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
	// unsupported is an expression parsed so it can be refused by name: a map, CASE, a literal Cypher
	// has and jaala doesn't.
	unsupported struct {
		pos Pos
		err *Error
	}
)

func (e strLit) at() Pos      { return e.pos }
func (e numLit) at() Pos      { return e.pos }
func (e paramRef) at() Pos    { return e.pos }
func (e varRef) at() Pos      { return e.pos }
func (e propRef) at() Pos     { return e.pos }
func (e binary) at() Pos      { return e.pos }
func (e negate) at() Pos      { return e.pos }
func (e minus) at() Pos       { return e.pos }
func (e isNull) at() Pos      { return e.pos }
func (e call) at() Pos        { return e.pos }
func (e hasLabel) at() Pos    { return e.pos }
func (e patExpr) at() Pos     { return e.pos }
func (e listLit) at() Pos     { return e.pos }
func (e unsupported) at() Pos { return e.pos }
