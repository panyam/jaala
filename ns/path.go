package ns

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
)

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

// Text is the path's value: a JSON list of its nodes, or an object of its nodes and edges. A text node
// is a JSON string, a number without a unit a JSON number in its canonical spelling (so 01 and 1 are
// one node), absent null, and anything else, a number with a unit included, its text as a string.
func (p Path) Text() string {
	var b strings.Builder
	list := func(vals []Value) {
		b.WriteByte('[')
		for i, v := range vals {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONValue(&b, v)
		}
		b.WriteByte(']')
	}
	if !p.HasEdges {
		list(p.Nodes)
		return b.String()
	}
	b.WriteString(`{"nodes":`)
	list(p.Nodes)
	b.WriteString(`,"edges":`)
	list(p.Edges)
	b.WriteByte('}')
	return b.String()
}

func writeJSONValue(b *strings.Builder, v Value) {
	switch {
	case v.Absent:
		b.WriteString("null")
	case v.Num != nil && v.BaseUnit == "" && !math.IsInf(*v.Num, 0) && !math.IsNaN(*v.Num):
		b.WriteString(ftoa(*v.Num))
	default:
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v.S) // a string always encodes
		b.Write(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	}
}

// errNotPath is ParsePath's error for text that isn't a path.
var errNotPath = errors.New("not a path: want a JSON list of nodes, or an object of nodes and edges")

// ParsePath reads a path's value back: a JSON string is a text node, a number a number, null absent.
// It refuses text Text could not have written, and an object whose edges aren't one fewer than its
// nodes.
func ParsePath(s string) (Path, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil || dec.More() {
		return Path{}, errNotPath
	}
	values := func(x any) ([]Value, bool) {
		items, ok := x.([]any)
		if !ok {
			return nil, false
		}
		out := make([]Value, 0, len(items))
		for _, it := range items {
			switch t := it.(type) {
			case nil:
				out = append(out, Absent())
			case string:
				out = append(out, S(t))
			case json.Number:
				f, err := t.Float64()
				if err != nil {
					return nil, false
				}
				out = append(out, N(f))
			default:
				return nil, false
			}
		}
		return out, true
	}
	switch t := raw.(type) {
	case []any:
		nodes, ok := values(t)
		if !ok || len(nodes) == 0 {
			return Path{}, errNotPath
		}
		return Path{Nodes: nodes}, nil
	case map[string]any:
		nodes, ok1 := values(t["nodes"])
		edges, ok2 := values(t["edges"])
		if !ok1 || !ok2 || len(t) != 2 || len(nodes) == 0 || len(edges) != len(nodes)-1 {
			return Path{}, errNotPath
		}
		return Path{Nodes: nodes, Edges: edges, HasEdges: true}, nil
	}
	return Path{}, errNotPath
}

// Steps is how many steps the path takes.
func (p Path) Steps() int { return len(p.Nodes) - 1 }
