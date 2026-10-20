package cypher

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Keeping failed pattern attempts (notPattern, #449) changes no parse: every saved fuzz input and
// conformance query parses to the same statement, or fails with the same error, with the memo on and
// off.
func TestThePatternMemoChangesNoParse(t *testing.T) {
	files, err := filepath.Glob("testdata/fuzz/FuzzCompile/*")
	if err != nil || len(files) < 20 {
		t.Fatalf("control: %d saved inputs, %v", len(files), err)
	}
	var texts []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.HasPrefix(line, "string(") {
				continue
			}
			text, err := strconv.Unquote(strings.TrimSuffix(strings.TrimPrefix(line, "string("), ")"))
			if err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			texts = append(texts, text)
		}
	}
	golden, err := os.ReadFile("testdata/conformance.golden")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(golden), "\n") {
		if w, _, _ := strings.Cut(line, " "); slices.Contains([]string{"MATCH", "OPTIONAL", "WITH", "UNWIND", "CALL", "RETURN"}, w) {
			texts = append(texts, line)
		}
	}
	defer func() { memoizePatterns = true }()
	compared := 0
	for _, text := range texts {
		if strings.Count(text, "(") > 500 {
			continue // the deep inputs are what the memo is for, and quadratic without it
		}
		memoizePatterns = true
		on, onErr := parse(text)
		memoizePatterns = false
		off, offErr := parse(text)
		if !reflect.DeepEqual(on, off) || errText(onErr) != errText(offErr) {
			t.Errorf("%q: memo on %v (%v), off %v (%v)", text, on, onErr, off, offErr)
		}
		compared++
	}
	if compared < 100 {
		t.Errorf("control: %d inputs compared", compared)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
