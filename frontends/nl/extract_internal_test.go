package nl

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/panyam/jaala/internal/gbnf"
)

// Every reply the extraction grammar writes is facts Extract reads over the vocabulary, given a text
// holding the words it quotes: a base relation at its arity, a closed domain's argument one of its
// values. control: every base relation is written, a removal with and without words too, and nothing
// derived or predicated.
func TestEveryReplyTheExtractionGrammarWritesIsRead(t *testing.T) {
	v := grammarVocabulary(t)
	rules, err := extractRules(v)
	if err != nil {
		t.Fatal(err)
	}
	text, err := ExtractGrammar(v)
	if err != nil {
		t.Fatal(err)
	}
	mustGBNF(t, text)
	quoted := regexp.MustCompile(` from "((?:[^"\\]|\\.)*)"$`)
	seen := map[string]int{}
	var s *gbnf.Sampler
	failed := 0
	for i := 0; i < 3000; i++ {
		if i%500 == 0 {
			s = gbnf.NewSampler(table(rules), int64(i), 3+i/500%5)
		}
		draft := s.Sample("root")
		var words []string
		for _, line := range strings.Split(draft, "\n") {
			if m := quoted.FindStringSubmatch(line); m != nil {
				words = append(words, unescape(m[1]))
			}
			switch {
			case strings.HasPrefix(line, "- ") && quoted.MatchString(line):
				seen["remove from"]++
			case strings.HasPrefix(line, "- "):
				seen["remove"]++
			}
			if open := strings.IndexByte(line, '('); open > 2 {
				seen[line[2:open]]++
			}
		}
		if _, err := readDraft(context.Background(), strings.Join(words, " | "), draft, v, nil, nil, nil); err != nil && strings.Contains(draft, `from ""`) {
			continue // the grammar can quote no words, which no text states; the line is refused, as it should be
		} else if err != nil {
			failed++
			if failed <= 5 {
				t.Errorf("sample %d: %v\n%s", i, err, draft)
			}
		}
	}
	for _, rel := range []string{"edge", "kind", "remove", "remove from"} {
		if seen[rel] == 0 {
			t.Errorf("control: no sample wrote %s (%v)", rel, seen)
		}
	}
	for _, rel := range []string{"graph.reach", "name.find", "str.contains"} {
		if seen[rel] > 0 {
			t.Errorf("a sample wrote %s, which isn't a base relation", rel)
		}
	}
}
