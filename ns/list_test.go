package ns

import (
	"math"
	"reflect"
	"testing"
)

// A list's text reads back as its values: text as text, a number as a number, one with a unit in that
// unit, absent as absent, and one value's spellings as one text.
func TestAListReadsBackAsItsValues(t *testing.T) {
	vals := []Value{S("a"), S(""), S(`q"[1]`), N(2), {S: "02", Num: ptr(2)}, NU(3.3, "V"), Absent(), N(math.Inf(-1))}
	text := ListText(vals)
	if want := `["a","","q\"[1]",2,2,{"num":3.3,"unit":"V"},null,"-Inf"]`; text != want {
		t.Fatalf("text %s, want %s", text, want)
	}
	back, err := ParseList(text)
	if err != nil {
		t.Fatal(err)
	}
	want := []Value{S("a"), S(""), S(`q"[1]`), N(2), N(2), NU(3.3, "V"), Absent(), S("-Inf")}
	if !reflect.DeepEqual(back, want) {
		t.Errorf("reads back as %+v, want %+v", back, want)
	}
	if got := ListText(nil); got != "[]" {
		t.Errorf("no values: %s", got)
	}
}

func TestParseListRefusesWhatListTextCouldNotWrite(t *testing.T) {
	for _, s := range []string{``, `a`, `{"num":1,"unit":"V"}`, `[1] [2]`, `[[1]]`, `[true]`, `[{"num":1}]`, `[{"num":1,"unit":""}]`, `[{"num":1,"unit":"V","x":2}]`, `[1e999]`} {
		if v, err := ParseList(s); err == nil {
			t.Errorf("%s read as %+v", s, v)
		}
	}
}
