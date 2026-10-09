package ns

import (
	"fmt"
	"strings"
)

// An ArgType says what one argument of a relation denotes. The zero value says nothing: an
// argument the relation has not described.
//
// The engine never interprets a kind. "net" and "component" are opaque strings a host defines and
// reads back, typically to make an answer cell clickable. What the engine does is carry them: from
// a base relation or predicate that declares them, through the rules that read it, to the columns of
// an answer (see ColumnKinds), so a derived relation's arguments keep the kinds of the facts it was
// built from.
//
// An argument is described in one of three ways, plus an optional vocabulary:
//
//   - a fixed entity Kind: every value is a net;
//   - a kind taken from another argument per row (KindFrom): entity(name, kind) says in each row
//     what that row's name is;
//   - a Kind located through another argument (Owner): a pin names nothing on its own and is found
//     through the component in its owner argument;
//
// or, for a value that is not an entity, a scalar Type with its Unit.
type ArgType struct {
	// Kind is the entity kind every value of the argument names, or "" for none.
	Kind string
	// KindFrom is the label of the argument whose value, in each row, is this one's kind. When it is
	// set, Kind is "".
	KindFrom string
	// Owner is the label of the argument that locates this one, set beside Kind for an entity that is
	// only found through another, such as a pin through its component.
	Owner string
	// Type is a scalar type, TypeString or TypeNumber, for an argument that is not an entity. A query
	// constant (or bound value) is read as this type: text that parses as a number becomes one in a
	// number argument, and text that does not is refused; a number in a string argument, or in one
	// naming an entity, matches by its text.
	Type string
	// Unit is the base unit of a numeric argument ("V", "A"), for a reader; see Value.BaseUnit.
	Unit string
	// Domain closes the argument over a vocabulary. On a base relation or a derived one, a query
	// constant outside it is refused before evaluation. On a KindFrom argument it is also the set of
	// kinds a constant may name, so entity(?n, "net") types ?n as a net.
	Domain []string
}

// The scalar types an ArgType may name.
const (
	TypeString = "string"
	TypeNumber = "number"
)

// IsZero reports whether the type says nothing about its argument.
func (t ArgType) IsZero() bool {
	return t.Kind == "" && t.KindFrom == "" && t.Owner == "" && t.Type == "" && t.Unit == "" && len(t.Domain) == 0
}

// String renders the type in the syntax a rule head declares it with, without the leading ": ".
func (t ArgType) String() string {
	var s string
	switch {
	case t.KindFrom != "":
		s = "?" + t.KindFrom
	case t.Kind != "" && t.Owner != "":
		s = fmt.Sprintf("%s(?%s)", t.Kind, t.Owner)
	case t.Kind != "":
		s = t.Kind
	case t.Type == TypeNumber && t.Unit != "":
		s = fmt.Sprintf("number[%s]", t.Unit)
	case t.Type != "" && len(t.Domain) == 0:
		s = t.Type
	}
	if len(t.Domain) > 0 {
		q := make([]string, len(t.Domain))
		for i, d := range t.Domain {
			q[i] = fmt.Sprintf("%q", d)
		}
		dom := "{" + strings.Join(q, ", ") + "}"
		if s == "" {
			return dom
		}
		return s + " " + dom
	}
	return s
}

// An ArgSig is one argument of a signature: its name and type, and whether the type was inferred
// from rules rather than declared.
type ArgSig struct {
	Name string
	ArgType
	// Inferred is set on a derived relation's argument whose type no rule head declares, so a host
	// can show a reader which types the author stated and which the engine worked out. A numeric
	// constant in a number-typed head column counts as declared, as does a head aggregate whose
	// function decides its type (count a number, list a string, sum, min and max a number over a typed
	// number).
	Inferred bool
}

// String renders the argument as a rule head would declare it: "n: net", or "n" when nothing is
// known about it.
func (a ArgSig) String() string {
	if t := a.ArgType.String(); t != "" {
		return a.Name + ": " + t
	}
	return a.Name
}
