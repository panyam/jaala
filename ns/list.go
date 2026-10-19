package ns

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
)

// A list is a value holding values (jaala#283), carried as the text ListText writes, as a path is
// (jaala#280): it joins, dedups and orders as text, an answer shows it as written, and stdlib's list.*
// functions read it with ParseList, with no state held anywhere. The list aggregate writes one, and a
// Source may serve one in a column it declares TypeList. Two lists of the same values in the same
// order are the same text. Order between lists is their text's, not element by element.
//
// It is a JSON list: a text value is a string, a number without a unit a number in its canonical
// spelling (so 01 and 1 are one value), a number with a unit {"num": 3.3, "unit": "V"} so it reads
// back as that number, absent null, and a number JSON can't hold (NaN, an infinity) its text.

// ListText is vals as a list's value.
func ListText(vals []Value) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range vals {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONValue(&b, v)
	}
	b.WriteByte(']')
	return b.String()
}

// errNotList is ParseList's error for text that isn't a list.
var errNotList = errors.New("not a list: want a JSON list of values")

// ParseList reads a list's value back: a JSON string is text, a number a number, {"num", "unit"} a
// number in that unit, null absent. It refuses text ListText could not have written.
func ParseList(s string) ([]Value, error) {
	raw, ok := decodeJSON(s)
	if !ok {
		return nil, errNotList
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, errNotList
	}
	vals, ok := jsonValues(items)
	if !ok {
		return nil, errNotList
	}
	return vals, nil
}

func writeJSONValue(b *strings.Builder, v Value) {
	switch {
	case v.Absent:
		b.WriteString("null")
	case v.Num != nil && !math.IsInf(*v.Num, 0) && !math.IsNaN(*v.Num) && v.BaseUnit == "":
		b.WriteString(ftoa(*v.Num))
	case v.Num != nil && !math.IsInf(*v.Num, 0) && !math.IsNaN(*v.Num):
		b.WriteString(`{"num":` + ftoa(*v.Num) + `,"unit":`)
		writeJSONString(b, v.BaseUnit)
		b.WriteByte('}')
	default:
		writeJSONString(b, v.S)
	}
}

func writeJSONString(b *strings.Builder, s string) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	b.Write(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
}

// decodeJSON reads s as one JSON value, its numbers kept as written.
func decodeJSON(s string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil || dec.More() {
		return nil, false
	}
	return raw, true
}

// jsonValues reads decoded JSON items as values, refusing an item writeJSONValue doesn't write.
func jsonValues(items []any) ([]Value, bool) {
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
		case map[string]any:
			n, ok1 := t["num"].(json.Number)
			u, ok2 := t["unit"].(string)
			if !ok1 || !ok2 || len(t) != 2 || u == "" {
				return nil, false
			}
			f, err := n.Float64()
			if err != nil {
				return nil, false
			}
			out = append(out, NU(f, u))
		default:
			return nil, false
		}
	}
	return out, true
}
