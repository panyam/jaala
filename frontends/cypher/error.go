package cypher

import "fmt"

// A Pos is a place in the Cypher text: Offset in bytes, Line and Column from 1, Column in runes.
type Pos struct {
	Offset, Line, Column int
}

// An Error is a Cypher text Compile refused, with where the piece it is about starts. Gap is set when
// the text is Cypher a program can't express yet: the jaala issue that would let it (#279 to #289),
// and Capability says what that is in words a model can act on. A Gap of 0 is a mistake in the text
// or against the graph view, such as an unknown label.
type Error struct {
	Pos        Pos
	Msg        string
	Gap        int
	Capability string
}

func (e *Error) Error() string {
	if e.Gap != 0 {
		return fmt.Sprintf("cypher: not supported yet: %s (jaala#%d): %s", e.Capability, e.Gap, e.Msg)
	}
	return "cypher: " + e.Msg
}

func errAt(p Pos, format string, args ...any) *Error {
	return &Error{Pos: p, Msg: fmt.Sprintf(format, args...)}
}

// The gaps a refusal names, from epic #278 and the issues beside it.
var (
	gapEdgeVars  = gap{279, "binding a relationship to a variable"}
	gapPaths     = gap{280, "paths as values"}
	gapShortest  = gap{281, "shortest paths and zero-length or long bounded reach"}
	gapOptional  = gap{282, "OPTIONAL MATCH and null values"}
	gapLists     = gap{283, "lists and maps as values"}
	gapFunctions = gap{284, "this function"}
	gapWrites    = gap{287, "writing to the graph"}
	gapGraphs    = gap{289, "choosing a graph"}
	// Not gaps in jaala: clauses this package compiles next, under #352.
	gapStages = gap{352, "this clause"}
)

type gap struct {
	issue int
	what  string
}

func (g gap) at(p Pos, format string, args ...any) *Error {
	return &Error{Pos: p, Msg: fmt.Sprintf(format, args...), Gap: g.issue, Capability: g.what}
}
