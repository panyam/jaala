package bolt

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/panyam/jaala/ns"
)

// A value is kept as a property and up to two companions: the number or the text at p, the number's
// unit at _unit.p, and its spelling at _text.p when the spelling isn't the number's own (`01`, `3.3V`),
// since an answer keeps data's spelling where two equal numbers meet (#148). An absent value is no
// property at all. A text argument (ns.TypeString, or an entity) keeps the text even for a number,
// which is how the engine reads it there.
func put(m map[string]any, p string, v ns.Value, text bool) {
	switch {
	case v.Absent:
	case v.Num != nil && !text:
		m[p] = *v.Num
		if v.BaseUnit != "" {
			m[unitProp(p)] = v.BaseUnit
		}
		if v.S != ftoa(*v.Num) {
			m[textProp(p)] = v.S
		}
	default:
		m[p] = v.S
	}
}

// get reads back what put wrote, from a node's or relationship's properties.
func get(props map[string]any, p string) (ns.Value, error) {
	raw, ok := props[p]
	if !ok || raw == nil {
		return ns.Absent(), nil
	}
	switch x := raw.(type) {
	case string:
		return ns.S(x), nil
	case float64:
		return number(x, props, p), nil
	case int64:
		return number(float64(x), props, p), nil
	case bool:
		return ns.S(strconv.FormatBool(x)), nil
	}
	return ns.Value{}, fmt.Errorf("bolt: property %s holds a %T, which jaala has no value for", p, raw)
}

func number(f float64, props map[string]any, p string) ns.Value {
	unit, _ := props[unitProp(p)].(string)
	v := ns.NU(f, unit)
	if s, ok := props[textProp(p)].(string); ok {
		v.S = s
	}
	return v
}

func unitProp(p string) string { return "_unit." + p }
func textProp(p string) string { return "_text." + p }

// valueKey names a value so that two values the engine calls equal share it (a number by value and
// unit, -0 as 0, text by its text), which makes a relationship's _key name the fact by its values.
func valueKey(v ns.Value) string {
	switch {
	case v.Absent:
		return "a"
	case v.Num != nil:
		f := *v.Num
		if f == 0 {
			f = 0
		}
		if math.IsNaN(f) {
			return "n:NaN|" + v.BaseUnit
		}
		return "n:" + ftoa(f) + "|" + v.BaseUnit
	}
	return "s:" + v.S
}

// factKey names a fact by its values.
func factKey(vals []ns.Value) string {
	var b strings.Builder
	for _, v := range vals {
		k := valueKey(v)
		fmt.Fprintf(&b, "%d:%s", len(k), k)
	}
	return b.String()
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// quote writes a name Cypher reads as an identifier, whatever it holds.
func quote(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }
