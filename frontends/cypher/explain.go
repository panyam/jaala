package cypher

import (
	"errors"
	"fmt"
	"strings"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/nl"
)

// Locate is where in the Cypher text an error is about (#353): a compile *Error's own position, or,
// for an error checking or parsing the program p compiled to (a *datalog.CheckError or
// *datalog.ParseError), the Cypher that the failing piece of the program was written for: the node
// pattern, relationship, WHERE condition, property or RETURN item. For an *nl.EmptyAnswer it is the
// Cypher that wrote the literal its report ends at, the one nearest the data that matched nothing
// (#386). It reports false for an error with no place, such as a spent budget, and for one in a piece
// of the program no Cypher wrote.
func (p Program) Locate(err error) (Pos, bool) {
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Pos, true
	}
	var empty *nl.EmptyAnswer
	if errors.As(err, &empty) {
		return p.locateEmpty(empty.Why)
	}
	at, ok := datalog.Locate(p.Text, err)
	if !ok {
		return Pos{}, false
	}
	return p.at(at.Offset)
}

// locateEmpty places the deepest literal of why's trail that the program's text holds as written: a
// rewrite may have renamed the ones above it.
func (p Program) locateEmpty(why *datalog.EmptyReport) (Pos, bool) {
	if why == nil {
		return Pos{}, false
	}
	for _, because := range why.Because {
		if pos, ok := p.locateEmpty(because); ok {
			return pos, true
		}
	}
	if i := strings.Index(p.Text, why.Literal); i >= 0 {
		return p.at(i)
	}
	return Pos{}, false
}

// at is the Cypher that wrote the program's text at offset: the narrowest span holding it.
func (p Program) at(offset int) (Pos, bool) {
	best := -1
	for i, s := range p.spans {
		if s.start <= offset && offset < s.end && (best < 0 || s.end-s.start < p.spans[best].end-p.spans[best].start) {
			best = i
		}
	}
	if best < 0 {
		return Pos{}, false
	}
	return p.spans[best].at, true
}

// Explain writes an error from compiling query, or from checking or running the program p it compiled
// to, the way a model or an agent reads it to fix the query: the message with the program's variables
// named as the Cypher names them (p.loc, not ?p_loc), the line of the query with a caret under the
// place (Locate), the fix when the engine has one, and, for Cypher a program can't express yet, that
// the query should be written without it. p may be the zero Program when Compile failed.
func Explain(query string, p Program, err error) string {
	var b strings.Builder
	b.WriteString(p.inCypher(err.Error()) + "\n")
	if pos, ok := p.Locate(err); ok {
		lines := strings.Split(query, "\n")
		if l := pos.Line; l >= 1 && l <= len(lines) {
			fmt.Fprintf(&b, "line %d, column %d:\n  %s\n  %s^\n", l, pos.Column, lines[l-1], strings.Repeat(" ", max(pos.Column-1, 0)))
		}
	}
	var check *datalog.CheckError
	if errors.As(err, &check) && check.Fix != "" {
		b.WriteString("fix: " + p.inCypher(check.Fix) + "\n")
	}
	var ce *Error
	if errors.As(err, &ce) && ce.Gap != 0 {
		b.WriteString("Write the query without it.\n")
	}
	return b.String()
}

// inCypher replaces each jaala variable in text that stands for something the Cypher named with
// that name.
func (p Program) inCypher(text string) string {
	if len(p.names) == 0 {
		return text
	}
	return varRe.ReplaceAllStringFunc(text, func(v string) string {
		if name, ok := p.names[v[1:]]; ok {
			return name
		}
		return v
	})
}
