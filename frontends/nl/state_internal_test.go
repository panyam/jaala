package nl

import "testing"

// repair rewrites a quoted value only where one other spelling is known: case and punctuation apart,
// in either quote, never between two candidates, never a value already right, and never into a value
// holding the quote it sits in.
func TestRepairRewritesOnlyAnUnambiguousSpelling(t *testing.T) {
	m := &memory{}
	m.see([]string{"MIT", "Apache-2.0", "Rust", "rust", `it's`})
	for _, c := range []struct {
		text, domain, want, changed string
	}{
		{`pkg(?p, "mit") => ?p`, "", `pkg(?p, "MIT") => ?p`, `"mit" to "MIT"`},
		{`MATCH (p {license: 'apache 2.0'}) RETURN p`, "", `MATCH (p {license: 'Apache-2.0'}) RETURN p`, `'apache 2.0' to 'Apache-2.0'`},
		{`pkg(?p, "MIT") => ?p`, "", `pkg(?p, "MIT") => ?p`, ""},
		{`lang(?p, "RUST") => ?p`, "", `lang(?p, "RUST") => ?p`, ""},
		{`say(?p, 'its') => ?p`, "", `say(?p, 'its') => ?p`, ""},
		{`node(?n, "Func") => ?n`, "func", `node(?n, "func") => ?n`, `"Func" to "func"`},
		{`node(?n, "Gopher") => ?n`, "func", `node(?n, "Gopher") => ?n`, ""},
	} {
		var domain []string
		if c.domain != "" {
			domain = []string{c.domain}
		}
		got, changed, ok := m.repair(c.text, domain)
		if got != c.want || changed != c.changed || ok != (c.changed != "") {
			t.Errorf("%s: %q %q %v, want %q %q", c.text, got, changed, ok, c.want, c.changed)
		}
	}
}
