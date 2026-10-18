package cypher

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/panyam/jaala/datalog"
)

// A Store names the relations of the generic graph store shape that Declaire and Agni both use (#366):
// every node in Node(id, kind), every relationship in Edge(from, kind, to), and properties as
// Attr(id, key, value). Graph turns it into a Graph with one label per node kind and one type per edge
// kind, so a host with this shape writes no Node or Edge by hand.
type Store struct {
	// Node, Edge and Attr are the relations' paths in the vocabulary. Attr is "" when the store keeps
	// no properties.
	Node, Edge, Attr string
	// Label names a node kind's label and Type an edge kind's type. Nil is DefaultLabel and
	// DefaultType, so a host overrides one without restating the other.
	Label, Type func(kind string) string
}

// Graph is the store's graph over the given kinds: a Node for each node kind, keyed by its id with the
// kind fixed, an Edge for each edge kind from its first argument to its third with the kind fixed, and
// Attr as the property relation. Two kinds naming one label or type make a Graph that Validate
// refuses.
func (s Store) Graph(nodeKinds, edgeKinds []string) Graph {
	label, typ := s.Label, s.Type
	if label == nil {
		label = DefaultLabel
	}
	if typ == nil {
		typ = DefaultType
	}
	var g Graph
	for _, k := range nodeKinds {
		g.Nodes = append(g.Nodes, Node{Label: label(k), Relation: s.Node, Key: 0, Fixed: map[int]string{1: k}})
	}
	for _, k := range edgeKinds {
		g.Edges = append(g.Edges, Edge{Type: typ(k), Relation: s.Edge, From: 0, To: 2, Fixed: map[int]string{1: k}})
	}
	if s.Attr != "" {
		g.Props = &PropRelation{Relation: s.Attr, Node: 0, Key: 1, Value: 2}
	}
	return g
}

// Kinds reads the node and edge kinds b's data holds, in the engine's answer order, for a host whose
// kinds come from its data rather than a list it keeps. It reads both relations whole, so a host asks
// once per data version.
func (s Store) Kinds(ctx context.Context, b *datalog.Base, opts ...datalog.Option) (nodeKinds, edgeKinds []string, err error) {
	if nodeKinds, err = distinct(ctx, b, fmt.Sprintf("%s(_, ?k) => ?k", s.Node), opts); err != nil {
		return nil, nil, err
	}
	if edgeKinds, err = distinct(ctx, b, fmt.Sprintf("%s(_, ?k, _) => ?k", s.Edge), opts); err != nil {
		return nil, nil, err
	}
	return nodeKinds, edgeKinds, nil
}

func distinct(ctx context.Context, b *datalog.Base, program string, opts []datalog.Option) ([]string, error) {
	q, err := datalog.Parse(program)
	if err != nil {
		return nil, err
	}
	rows, err := datalog.SemiNaive{}.Eval(ctx, q, b, opts...)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Bind["k"].S
	}
	return out, nil
}

// DefaultLabel writes a node kind as a label is usually written: each word capitalized and joined,
// so func is Func and http_route is HttpRoute. A word is a run of letters and digits; a kind written
// in camel case keeps its inner capitals (httpRoute is HttpRoute).
func DefaultLabel(kind string) string {
	var b strings.Builder
	for _, w := range words(kind) {
		r := []rune(w)
		b.WriteString(string(unicode.ToUpper(r[0])) + string(r[1:]))
	}
	return b.String()
}

// DefaultType writes an edge kind as a relationship type is usually written: upper case, words joined
// by underscores, so calls is CALLS and dispatches_to (or dispatchesTo) is DISPATCHES_TO.
func DefaultType(kind string) string {
	var parts []string
	for _, w := range words(kind) {
		parts = append(parts, splitCamel(w)...)
	}
	return strings.ToUpper(strings.Join(parts, "_"))
}

// words splits s at every character that is neither a letter nor a digit.
func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// splitCamel splits w before each upper-case letter that follows a lower-case one.
func splitCamel(w string) []string {
	var out []string
	start := 0
	r := []rune(w)
	for i := 1; i < len(r); i++ {
		if unicode.IsUpper(r[i]) && unicode.IsLower(r[i-1]) {
			out = append(out, string(r[start:i]))
			start = i
		}
	}
	return append(out, string(r[start:]))
}
