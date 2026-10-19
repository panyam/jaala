package ns

import (
	"math"
	"reflect"
	"testing"
)

// A path's text reads back as the path, a number as a number and absent as absent, whatever its text
// nodes hold, and a number's spelling doesn't make two texts of one path.
func TestAPathReadsBackAsWritten(t *testing.T) {
	for _, c := range []struct {
		p    Path
		text string
	}{
		{Path{Nodes: []Value{S("a"), S("b")}}, `["a","b"]`},
		{Path{Nodes: []Value{S(`q"<&>\`), S("é\n")}}, `["q\"<&>\\","é\n"]`},
		{Path{Nodes: []Value{N(1), {S: "01", Num: ptr(1)}, N(-2.5), Absent()}}, `[1,1,-2.5,null]`},
		{Path{Nodes: []Value{S("a"), S("b")}, Edges: []Value{S("e1")}, HasEdges: true}, `{"nodes":["a","b"],"edges":["e1"]}`},
		{Path{Nodes: []Value{S("a")}, Edges: []Value{}, HasEdges: true}, `{"nodes":["a"],"edges":[]}`},
		{Path{Nodes: []Value{{S: "3V", Num: ptr(3), BaseUnit: "V"}, N(math.Inf(1))}}, `[{"num":3,"unit":"V"},"+Inf"]`},
	} {
		if got := c.p.Text(); got != c.text {
			t.Errorf("%v: text %s, want %s", c.p, got, c.text)
			continue
		}
		back, err := ParsePath(c.text)
		if err != nil {
			t.Errorf("%s: %v", c.text, err)
			continue
		}
		if back.Text() != c.text || back.HasEdges != c.p.HasEdges || len(back.Nodes) != len(c.p.Nodes) {
			t.Errorf("%s reads back as %+v", c.text, back)
		}
	}
	back, _ := ParsePath(`[1,"1",null]`)
	if want := []Value{N(1), S("1"), Absent()}; !reflect.DeepEqual(back.Nodes, want) {
		t.Errorf("kinds: %+v, want %+v", back.Nodes, want)
	}
}

func TestParsePathRefusesWhatTextCouldNotWrite(t *testing.T) {
	for _, s := range []string{``, `[]`, `"a"`, `["a"] x`, `[["a"]]`, `[true]`, `{"nodes":["a","b"],"edges":[]}`, `{"nodes":["a"],"edges":[],"x":1}`, `{"nodes":[],"edges":[]}`, `[1e999]`} {
		if p, err := ParsePath(s); err == nil {
			t.Errorf("%s read as %+v", s, p)
		}
	}
}

func ptr(f float64) *float64 { return &f }
