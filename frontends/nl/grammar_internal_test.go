package nl

import (
	"testing"

	"github.com/panyam/jaala/datalog"
)

// Every program the grammar can write parses, which is what decoding under it promises a host.
func TestEveryProgramTheGrammarWritesParses(t *testing.T) {
	var s *sampler
	failed, rules := 0, programRules()
	for i := 0; i < 21000; i++ {
		if i%300 == 0 { // 70 seeds over every depth: one seed alone missed -(9) as a column
			s = newSampler(rules, int64(i), 3+i/300%7)
		}
		text := s.sample("root")
		if _, err := datalog.Parse(text); err != nil {
			failed++
			t.Errorf("%q: %v", text, err)
			if failed >= 15 {
				t.Fatal("stopping after 15")
			}
		}
	}
}
