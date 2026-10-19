package ns

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// changesFormat is the version of the JSON a change set is written as.
const changesFormat = 1

// A Session is a change set with the version of the facts it was made over (#417), as a host sends it
// between processes: a browser keeping a session beside its snapshot, a server reading it through an
// Overlay of its own store, or a commit. Base is the host's name for that version (its Versioned
// string, a revision id), carried and never read by jaala, so a host can refuse a session made over a
// version that has since moved, or rebase it. Empty, the session says nothing about its base.
type Session struct {
	Base    string  `json:"base,omitempty"`
	Changes Changes `json:"changes"`
}

type wireChanges struct {
	Format json.Number `json:"format"`
	Ops    []wireOp    `json:"ops"`
}

type wireOp struct {
	Rel   string            `json:"rel"`
	Op    string            `json:"op"`
	Vals  []json.RawMessage `json:"vals"`
	ID    string            `json:"id,omitempty"`
	Cites []string          `json:"cites,omitempty"`
}

// MarshalJSON writes c as {"format": 1, "ops": [...]}, each op {"rel", "op" ("add" or "remove"),
// "vals", "id", "cites"}, relation by relation in the order c first named them and, within one, each
// fact's last operation in the order c first named the fact, which is what c holds: decoding replays
// the ops into a change set equal to c in every respect, Relations' order and an Overlay's Version
// included, so two processes holding the same session key their caches alike. Empty and nil
// citations both write as none.
//
// A value is written so it reads back as itself: absent is null, text a string, and a number whose
// text is its canonical spelling, with no unit, a number. Any other number is {"num": n} with "unit"
// when it has one and "text" when its text isn't the canonical spelling (data's own, such as "1.50"
// or "3.3V", which an answer keeps, #148), n being "NaN", "+Inf" or "-Inf" when JSON can't hold it;
// text with a unit is {"text", "unit"}. That reads every value a list writes (ListText) the same way.
func (c Changes) MarshalJSON() ([]byte, error) {
	w := wireChanges{Format: json.Number(strconv.Itoa(changesFormat)), Ops: []wireOp{}}
	for _, rel := range c.rels {
		r := c.byRel[rel]
		for _, k := range r.keys {
			ch := r.facts[k]
			op := wireOp{Rel: rel, Op: "remove", Vals: make([]json.RawMessage, len(ch.t.Vals)), ID: ch.t.ID, Cites: ch.t.Cites}
			if ch.add {
				op.Op = "add"
			}
			for i, v := range ch.t.Vals {
				var b strings.Builder
				writeWireValue(&b, v)
				op.Vals[i] = json.RawMessage(b.String())
			}
			w.Ops = append(w.Ops, op)
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(w); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func writeWireValue(b *strings.Builder, v Value) {
	switch {
	case v.Absent:
		b.WriteString("null")
	case v.Num == nil && v.BaseUnit == "":
		writeJSONString(b, v.S)
	case v.Num == nil:
		b.WriteString(`{"text":`)
		writeJSONString(b, v.S)
		b.WriteString(`,"unit":`)
		writeJSONString(b, v.BaseUnit)
		b.WriteByte('}')
	default:
		n := *v.Num
		finite := !math.IsNaN(n) && !math.IsInf(n, 0)
		if finite && v.BaseUnit == "" && v.S == ftoa(n) {
			b.WriteString(ftoa(n))
			return
		}
		b.WriteString(`{"num":`)
		if finite {
			b.WriteString(ftoa(n))
		} else {
			b.WriteString(`"` + ftoa(n) + `"`)
		}
		if v.BaseUnit != "" {
			b.WriteString(`,"unit":`)
			writeJSONString(b, v.BaseUnit)
		}
		if v.S != ftoa(n) {
			b.WriteString(`,"text":`)
			writeJSONString(b, v.S)
		}
		b.WriteByte('}')
	}
}

// UnmarshalJSON reads a change set MarshalJSON wrote, replacing c. It refuses a format it doesn't
// read, an op other than "add" or "remove", an op naming no relation, a field the format doesn't
// write, and a value MarshalJSON couldn't have written, so a session from a newer writer fails rather
// than reading as something else.
func (c *Changes) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	var w wireChanges
	if err := dec.Decode(&w); err != nil {
		return fmt.Errorf("query: change set: not a change set's JSON: %v", err)
	}
	if w.Format.String() != strconv.Itoa(changesFormat) {
		return fmt.Errorf("query: change set: format %q isn't one this version reads (it reads %d)", w.Format.String(), changesFormat)
	}
	var out Changes
	for i, op := range w.Ops {
		if op.Rel == "" {
			return fmt.Errorf("query: change set: op %d names no relation", i)
		}
		if op.Op != "add" && op.Op != "remove" {
			return fmt.Errorf("query: change set: op %d is %q, not \"add\" or \"remove\"", i, op.Op)
		}
		t := Tuple{Vals: make([]Value, len(op.Vals)), ID: op.ID, Cites: op.Cites}
		for j, raw := range op.Vals {
			v, ok := readWireValue(raw)
			if !ok {
				return fmt.Errorf("query: change set: op %d's value %d isn't one a change set writes: %s", i, j, raw)
			}
			t.Vals[j] = v
		}
		out.put(op.Rel, t, op.Op == "add")
	}
	*c = out
	return nil
}

func readWireValue(raw json.RawMessage) (Value, bool) {
	v, ok := decodeJSON(string(raw))
	if !ok {
		return Value{}, false
	}
	obj, isObj := v.(map[string]any)
	if !isObj {
		vals, ok := jsonValues([]any{v})
		if !ok {
			return Value{}, false
		}
		return vals[0], true
	}
	text, hasText := obj["text"].(string)
	unit, hasUnit := obj["unit"].(string)
	num, hasNum := obj["num"]
	if _, ok := obj["text"]; ok && !hasText {
		return Value{}, false
	}
	if _, ok := obj["unit"]; ok && !hasUnit {
		return Value{}, false
	}
	known := 0
	for _, has := range []bool{hasText, hasUnit, hasNum} {
		if has {
			known++
		}
	}
	if known != len(obj) || hasUnit && unit == "" {
		return Value{}, false
	}
	if !hasNum {
		if !hasText {
			return Value{}, false
		}
		return Value{S: text, BaseUnit: unit}, true
	}
	var f float64
	switch n := num.(type) {
	case json.Number:
		var err error
		if f, err = n.Float64(); err != nil {
			return Value{}, false
		}
	case string:
		switch n {
		case "NaN":
			f = math.NaN()
		case "+Inf":
			f = math.Inf(1)
		case "-Inf":
			f = math.Inf(-1)
		default:
			return Value{}, false
		}
	default:
		return Value{}, false
	}
	if !hasText {
		text = ftoa(f)
	}
	return Value{S: text, Num: &f, BaseUnit: unit}, true
}
