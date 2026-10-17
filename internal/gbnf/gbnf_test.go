package gbnf

import "testing"

// An empty rule another rule reads twice in a row is the case Earley's recognizer misses without the
// nullable step (Aycock and Horspool): the second read is predicted after the first completed.
func TestTheRecognizerReadsEmptyRules(t *testing.T) {
	g, err := Parse("root ::= a a \"x\"\na ::= \"\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if !g.Accepts("x") || g.Accepts("") || g.Accepts("xx") {
		t.Fatal(`root ::= a a "x" with a empty doesn't read exactly "x"`)
	}
}

func TestAPrefixIsWhatSomeAcceptedTextStartsWith(t *testing.T) {
	g, err := Parse("root ::= \"ab\" [0-9]+\n")
	if err != nil {
		t.Fatal(err)
	}
	for text, want := range map[string]bool{"": true, "a": true, "ab": true, "ab12": true, "b": false, "abx": false, "ab1x": false} {
		if g.AcceptsPrefix(text) != want {
			t.Errorf("AcceptsPrefix(%q) = %v", text, !want)
		}
	}
	if g.Accepts("ab") || !g.Accepts("ab1") {
		t.Error("control: Accepts reads a prefix as a whole text")
	}
}

func TestABoundedRepetitionReadsMToNItems(t *testing.T) {
	g, err := Parse("root ::= \"a\"{2,4} \"b\"\n")
	if err != nil {
		t.Fatal(err)
	}
	for text, want := range map[string]bool{"ab": false, "aab": true, "aaab": true, "aaaab": true, "aaaaab": false, "b": false} {
		if g.Accepts(text) != want {
			t.Errorf("Accepts(%q) = %v", text, !want)
		}
	}
	for _, bad := range []string{"root ::= \"a\"{2}\n", "root ::= \"a\"{3,2}\n", "root ::= \"a\"{2,\n"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) took a repetition it doesn't read", bad)
		}
	}
}
