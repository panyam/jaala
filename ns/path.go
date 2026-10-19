package ns

import "errors"

// A Path is a walk's route as a value (jaala#280): its nodes in order from where it starts, and, when
// the relation it walked names its edges (from, edge, to), the edge taken at each step. A path of n
// steps has n+1 nodes and, with edges, n edges.
//
// A path travels through a query as the text Text writes, so it joins, dedups and orders as any text
// does, an answer shows it as written, and stdlib's path.* functions read it with ParsePath, with no
// state held anywhere. A path over a two-argument relation is a JSON list of its nodes,
// ["a","b","c"]; one with edges is {"nodes":["a","b","c"],"edges":["e1","e2"]}, since a list
// alternating nodes and edges would read the same as a list of nodes.
type Path struct {
	Nodes []Value
	Edges []Value
	// HasEdges says the path names its edges, so a path of no steps still writes its (empty) edges.
	HasEdges bool
}

// Text is the path's value: its nodes as a list (ListText), or an object of its nodes and edges, each
// a list.
func (p Path) Text() string {
	if !p.HasEdges {
		return ListText(p.Nodes)
	}
	return `{"nodes":` + ListText(p.Nodes) + `,"edges":` + ListText(p.Edges) + `}`
}

// errNotPath is ParsePath's error for text that isn't a path.
var errNotPath = errors.New("not a path: want a JSON list of nodes, or an object of nodes and edges")

// ParsePath reads a path's value back, its values as ParseList reads a list's. It refuses text Text
// could not have written, and an object whose edges aren't one fewer than its nodes.
func ParsePath(s string) (Path, error) {
	raw, ok := decodeJSON(s)
	if !ok {
		return Path{}, errNotPath
	}
	switch t := raw.(type) {
	case []any:
		nodes, ok := jsonValues(t)
		if !ok || len(nodes) == 0 {
			return Path{}, errNotPath
		}
		return Path{Nodes: nodes}, nil
	case map[string]any:
		ns, ok1 := t["nodes"].([]any)
		es, ok2 := t["edges"].([]any)
		if !ok1 || !ok2 || len(t) != 2 {
			return Path{}, errNotPath
		}
		nodes, ok1 := jsonValues(ns)
		edges, ok2 := jsonValues(es)
		if !ok1 || !ok2 || len(nodes) == 0 || len(edges) != len(nodes)-1 {
			return Path{}, errNotPath
		}
		return Path{Nodes: nodes, Edges: edges, HasEdges: true}, nil
	}
	return Path{}, errNotPath
}

// Steps is how many steps the path takes.
func (p Path) Steps() int { return len(p.Nodes) - 1 }
