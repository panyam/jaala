package ns

import "testing"

// electrical is a small table: base A, V, m, s; ohm and Ω over them; W; the prefixes k and m.
func electrical(t *testing.T) *Vocabulary {
	t.Helper()
	v := MustVocabulary(nil)
	for _, u := range [][2]string{{"k", "1e3"}, {"m", "1e-3"}, {"A", ""}, {"V", ""}, {"m", ""}, {"s", ""}, {"ohm", "V/A"}, {"Ω", "V/A"}, {"W", "V*A"}} {
		if err := v.AddUnit(u[0], u[1]); err != nil {
			t.Fatal(err)
		}
	}
	return v
}

func TestUnitsNormalizeToNamedBaseUnits(t *testing.T) {
	v := electrical(t)
	for _, c := range []struct {
		in   string
		f    float64
		want string
	}{
		{"kohm", 1000, "ohm"}, {"mV", 0.001, "V"}, {"A*ohm", 1, "V"}, {"V/A", 1, "ohm"}, {"Ω", 1, "ohm"},
		{"m/s", 1, "m/s"}, {"mm", 0.001, "m"}, {"min", 1, "min"}, {"V*V/ohm", 1, "W"}, {"ohm/s", 1, "ohm/s"},
		{"kV*mA", 1, "W"}, {"m/(s*s)", 1, "m/s^2"}, {"1/s", 1, "1/s"},
	} {
		f, got, err := v.Units().Normalize(1, c.in)
		if err != nil || got != c.want || f != c.f {
			t.Errorf("Normalize(1, %q) = %g %q %v, want %g %q", c.in, f, got, err, c.f, c.want)
		}
	}
	// Every spelling reads back as the unit it names.
	for _, s := range []string{"ohm/s", "m/s^2", "A^2*ohm", "1/s"} {
		if !v.Units().SameUnit(s, s) {
			t.Errorf("%s isn't the same unit as itself", s)
		}
	}
	if !v.Units().SameUnit("ohm", "V/A") || v.Units().SameUnit("ohm", "V") {
		t.Error("SameUnit should read names through the table")
	}
}

func TestAHostNamesItsOwnUnits(t *testing.T) {
	v := electrical(t)
	if err := v.AddUnit("speed", "m/s"); err != nil {
		t.Fatal(err)
	}
	if _, got, _ := v.Units().Normalize(1, "m/s"); got != "speed" {
		t.Errorf("m/s is written %q, want the host's speed", got)
	}
	for _, c := range []struct{ name, def, want string }{
		{"V", "", `unit "V" is defined twice`},
		{"k", "1e6", `prefix "k" is defined twice`},
		{"x2", "", `isn't a name`},
		{"bad", "V^x", `a power is a whole number`},
		{"bad", "1.2.3", `isn't a positive decimal number`},
		{"bad", "0*V", `isn't a positive decimal number`},
		{"bad", "999999999999*999999999999*V", `too large to keep exactly`},
	} {
		if err := v.AddUnit(c.name, c.def); err == nil || !contains(err.Error(), c.want) {
			t.Errorf("AddUnit(%q, %q) = %v, want it to say %q", c.name, c.def, err, c.want)
		}
	}
}

// A unit's scale is any exact factor beside its power of ten (jaala#303): durations over seconds, a
// binary prefix, a decimal factor, and a prefix that isn't a power of ten at all.
func TestAUnitScaleIsAnyExactFactor(t *testing.T) {
	v := MustVocabulary(nil)
	for _, u := range [][2]string{
		{"k", "1e3"}, {"m", "1e-3"}, {"m", ""}, {"s", ""}, {"B", ""}, {"egg", ""},
		{"min", "60*s"}, {"h", "60*min"}, {"Ki", "1024"}, {"inch", "2.54e-2*m"}, {"dozen", "12"},
	} {
		if err := v.AddUnit(u[0], u[1]); err != nil {
			t.Fatalf("AddUnit(%q, %q): %v", u[0], u[1], err)
		}
	}
	u := v.Units()
	for _, c := range []struct {
		f    float64
		in   string
		want float64
		unit string
	}{
		{1.5, "min", 90, "s"}, {2, "h", 7200, "s"}, {2, "KiB", 2048, "B"}, {1, "inch", 0.0254, "m"},
		{36, "km/h", 10, "m/s"}, {90, "s/min", 1.5, ""}, {2, "dozenegg", 24, "egg"}, {3300, "ms", 3.3, "s"},
	} {
		f, got, err := u.Normalize(c.f, c.in)
		if err != nil || f != c.want || got != c.unit {
			t.Errorf("Normalize(%g, %q) = %g %q %v, want %g %q", c.f, c.in, f, got, err, c.want, c.unit)
		}
	}
	// One scale has one representation, however it is written, and its text reads back as it.
	for _, pair := range [][2]string{{"min", "60*s"}, {"min", "6e1*s"}, {"h", "3600*s"}, {"KiB", "1024*B"}, {"1/min", "1/(60*s)"}, {"ks", "1000*s"}} {
		a, err1 := u.ParseUnit(pair[0])
		b, err2 := u.ParseUnit(pair[1])
		if err1 != nil || err2 != nil || !a.Same(b) {
			t.Errorf("%s and %s aren't one unit: %v %v %v %v", pair[0], pair[1], a, b, err1, err2)
		}
		back, err := u.ParseUnit(a.String())
		if err != nil || !back.Same(a) {
			t.Errorf("%s writes %q, which reads back as %v (%v)", pair[0], a.String(), back, err)
		}
	}
}

// Clone copies the unit table and whether a Source's units are folded, and adding a unit to the copy
// leaves the original as it was.
func TestCloneKeepsTheUnitTable(t *testing.T) {
	v := electrical(t)
	v.FoldSourceUnits()
	c := v.Clone()
	if !c.FoldsSourceUnits() {
		t.Error("the clone doesn't fold a Source's units")
	}
	if f, got, _ := c.Units().Normalize(1, "kohm"); f != 1000 || got != "ohm" {
		t.Errorf("the clone reads kohm as %g %q, want 1000 ohm", f, got)
	}
	if err := c.AddUnit("speed", "m/s"); err != nil {
		t.Fatal(err)
	}
	if _, got, _ := v.Units().Normalize(1, "m/s"); got != "m/s" {
		t.Errorf("a unit added to the clone reached the original: m/s is written %q", got)
	}
	if MustVocabulary(nil).FoldsSourceUnits() {
		t.Error("a new vocabulary folds a Source's units without being asked")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
