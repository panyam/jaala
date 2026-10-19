package cypher

import (
	"fmt"
	"sort"
	"strings"

	"github.com/panyam/jaala/ns"
)

// A Graph is how a vocabulary's relations read as a property graph: which relations hold the nodes
// of a label, which hold the relationships of a type, and where each property lives. The host
// writes it, since nothing in a relation's schema says which of its arguments is an endpoint.
//
// A node is its key value: `(p:Package)` binds p to the key argument of Package's relation, and
// `RETURN p` answers that value. Two shapes are both covered. A vocabulary of plain relations gives
// each label a relation (Package is pkg(name, license), keyed by name, with license as a column). A
// generic store gives every label one relation and tells them apart by a constant (Function is
// node(id, "Function"), through Fixed), and holds properties in a key-value relation (Props).
type Graph struct {
	Nodes []Node
	Edges []Edge
	// Props is the relation holding a node's properties as key-value facts, attr(node, key, value),
	// read for a property no Node lists. Nil when every property is a column.
	Props *PropRelation
	// Derived maps a property to the relation, or predicate, that holds it for any node, one the host
	// computes rather than stores (Declaire's name.of answers a function's short name from its id).
	// A label's own column wins over it, and it wins over Props.
	Derived map[string]PropSource
}

// A Node is the relation holding the nodes of one label.
type Node struct {
	Label    string
	Relation string
	Key      int            // the argument holding the node's key
	Fixed    map[int]string // arguments a constant pins, such as the label's name in a shared relation
	// Props maps a property to the argument holding it. A property at Key reads the key itself, so
	// `{name: 'app'}` on a node keyed by name is the node "app".
	Props map[string]int
}

// An Edge is the relation holding the relationships of one type, From to To.
type Edge struct {
	Type     string
	Relation string
	From, To int
	Fixed    map[int]string
	Props    map[string]int
}

// A PropRelation holds node properties as facts: the node's key at Node, the property's name at Key
// and its value at Value.
type PropRelation struct {
	Relation         string
	Node, Key, Value int
}

// A PropSource holds one property: the node's key at Node and the property's value at Value, with
// any other argument pinned by Fixed or left unread. Every read is an atom over it, so its modes say
// which the host answers: `{name: 'Foo'}` binds the value, `RETURN f.name` the node. A node it holds
// no fact for has no row, as with a Props read.
type PropSource struct {
	Relation    string
	Node, Value int
	Fixed       map[int]string
}

// Validate checks g against v: every relation is a member of v at an arity holding every argument g
// names, and labels and types are named once each.
func (g Graph) Validate(v *ns.Vocabulary) error {
	labels, types := map[string]bool{}, map[string]bool{}
	for _, n := range g.Nodes {
		if n.Label == "" || labels[n.Label] {
			return fmt.Errorf("cypher: graph: label %q is empty or named twice", n.Label)
		}
		labels[n.Label] = true
		if err := checkArgs(v, "label "+n.Label, n.Relation, n.Fixed, n.Props, n.Key); err != nil {
			return err
		}
	}
	for _, e := range g.Edges {
		if e.Type == "" || types[e.Type] {
			return fmt.Errorf("cypher: graph: relationship type %q is empty or named twice", e.Type)
		}
		types[e.Type] = true
		if e.From == e.To {
			return fmt.Errorf("cypher: graph: type %s reads From and To from one argument", e.Type)
		}
		if err := checkArgs(v, "type "+e.Type, e.Relation, e.Fixed, e.Props, e.From, e.To); err != nil {
			return err
		}
	}
	if p := g.Props; p != nil {
		if p.Node == p.Key || p.Node == p.Value || p.Key == p.Value {
			return fmt.Errorf("cypher: graph: the property relation %s names one argument twice", p.Relation)
		}
		if err := checkArgs(v, "properties", p.Relation, nil, nil, p.Node, p.Key, p.Value); err != nil {
			return err
		}
	}
	for _, k := range sortedKeys(g.Derived) {
		p := g.Derived[k]
		_, fixesNode := p.Fixed[p.Node]
		_, fixesValue := p.Fixed[p.Value]
		if p.Node == p.Value || fixesNode || fixesValue {
			return fmt.Errorf("cypher: graph: property %s's relation %s names one argument twice", k, p.Relation)
		}
		if err := checkArgs(v, "property "+k, p.Relation, p.Fixed, nil, p.Node, p.Value); err != nil {
			return err
		}
	}
	return nil
}

func checkArgs(v *ns.Vocabulary, what, rel string, fixed map[int]string, props map[string]int, args ...int) error {
	n, ok := arity(v, rel)
	if !ok {
		return fmt.Errorf("cypher: graph: %s reads %q, which the vocabulary doesn't hold", what, rel)
	}
	in := func(i int) bool { return i >= 0 && i < n }
	for _, i := range args {
		if !in(i) {
			return fmt.Errorf("cypher: graph: %s reads argument %d of %s, which takes %d", what, i, rel, n)
		}
	}
	for i := range fixed {
		if !in(i) {
			return fmt.Errorf("cypher: graph: %s fixes argument %d of %s, which takes %d", what, i, rel, n)
		}
	}
	for _, k := range sortedKeys(props) {
		if i := props[k]; !in(i) {
			return fmt.Errorf("cypher: graph: %s reads property %s at argument %d of %s, which takes %d", what, k, i, rel, n)
		}
	}
	return nil
}

// arity is the number of arguments rel takes in v: a base relation's schema, a derived relation's
// signature or a predicate's.
func arity(v *ns.Vocabulary, rel string) (int, bool) {
	if s, ok := v.Schema(rel); ok {
		return s.Arity, true
	}
	if sig, ok := v.Signature(rel); ok {
		return len(sig), true
	}
	if b, ok := v.Predicate(rel); ok {
		return b.Arity, true
	}
	return 0, false
}

// argTypes is what rel's arguments denote in v, from a base relation's schema or a predicate's Types.
func argTypes(v *ns.Vocabulary, rel string) []ns.ArgType {
	if s, ok := v.Schema(rel); ok {
		return s.Types
	}
	if b, ok := v.Predicate(rel); ok {
		return b.Types
	}
	return nil
}

func (g Graph) node(label string) (Node, bool) {
	for _, n := range g.Nodes {
		if n.Label == label {
			return n, true
		}
	}
	return Node{}, false
}

func (g Graph) edge(ty string) (Edge, bool) {
	for _, e := range g.Edges {
		if e.Type == ty {
			return e, true
		}
	}
	return Edge{}, false
}

func (g Graph) labelNames() []string {
	var out []string
	for _, n := range g.Nodes {
		out = append(out, n.Label)
	}
	return out
}

func (g Graph) typeNames() []string {
	var out []string
	for _, e := range g.Edges {
		out = append(out, e.Type)
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Describe says what a Cypher query over g can name, in a line: each label with its properties, each
// relationship type with its own, the properties the host computes for any node (Derived), and, with
// a Props relation, that a node's other properties are any key its facts name. The MCP server's
// cypher tool and the front end's card describe the graph with it.
func (g Graph) Describe() string {
	var labels, types []string
	for _, n := range g.Nodes {
		labels = append(labels, n.Label+propsText(n.Props))
	}
	for _, e := range g.Edges {
		types = append(types, e.Type+propsText(e.Props))
	}
	text := fmt.Sprintf("Node labels: %s. Relationship types: %s.", strings.Join(labels, ", "), strings.Join(types, ", "))
	if len(g.Derived) > 0 {
		text += " Any node may have" + propsText(g.Derived) + ", which the host computes."
	}
	if g.Props != nil {
		text += " A node's other properties are any key its " + g.Props.Relation + " facts name."
	}
	return text
}

func propsText[V any](props map[string]V) string {
	if len(props) == 0 {
		return ""
	}
	names := make([]string, 0, len(props))
	for k := range props {
		names = append(names, k)
	}
	sort.Strings(names)
	return " {" + strings.Join(names, ", ") + "}"
}
