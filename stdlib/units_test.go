package stdlib

import (
	"testing"

	"github.com/panyam/jaala/ns"
)

func units(t *testing.T) *ns.Vocabulary {
	t.Helper()
	v := ns.MustVocabulary(nil)
	if err := Units(v); err != nil {
		t.Fatal(err)
	}
	return v
}

// Each unit is written as it was when V was a base unit and g the base of mass (before #205), which
// is what hosts' answers show.
func TestDecomposedUnitsAreWrittenAsBefore(t *testing.T) {
	u := units(t).Units()
	for _, c := range [][2]string{
		{"V*V", "V^2"}, {"V/m", "V/m"}, {"ohm/s", "ohm/s"}, {"W/m^2", "W/m^2"}, {"V*s", "V*s"}, {"1/V", "1/V"},
		{"F*s", "C*s/V"}, {"V/s", "V/s"}, {"A*s/m", "C/m"}, {"J/s", "W"}, {"V^3", "V^3"}, {"ohm*m", "m*ohm"},
		{"W*s/m^2", "W*s/m^2"}, {"A/m^2", "A/m^2"}, {"V*A*s", "J"}, {"1/ohm", "A/V"}, {"H/m", "ohm*s/m"},
		{"F/m", "C/(V*m)"}, {"A^2*ohm", "W"}, {"V/(A*s)", "ohm/s"}, {"m/s^2", "m/s^2"}, {"V*K", "K*V"}, {"W/K", "W/K"},
		{"kV*mA", "W"}, {"kohm", "ohm"},
	} {
		if _, got, err := u.Normalize(1, c[0]); err != nil || got != c[1] {
			t.Errorf("%s is written %q (%v), want %q", c[0], got, err, c[1])
		}
	}
}

// With V, ohm, W and J products of the SI base units, a unit a host names over kg, m and s meets
// them: N*m is J, and N*m/s is W (#205). Before, V was a base unit of its own and N*m never was J.
func TestTwoSpellingsOfOneDimensionAreOneUnit(t *testing.T) {
	v := units(t)
	if err := v.AddUnit("N", "kg*m/s^2"); err != nil {
		t.Fatal(err)
	}
	u := v.Units()
	for _, c := range [][2]string{{"N*m", "J"}, {"N*m/s", "W"}, {"kg*m^2/(A*s^3)", "V"}, {"J/C", "V"}, {"N", "N"}} {
		if _, got, err := u.Normalize(1, c[0]); err != nil || got != c[1] {
			t.Errorf("%s is written %q (%v), want %q", c[0], got, err, c[1])
		}
	}
	if !u.SameUnit("N*m", "V*A*s") || u.SameUnit("N", "J") {
		t.Error("SameUnit should equate N*m with V*A*s and tell N from J")
	}
}

func TestMassIsInKilograms(t *testing.T) {
	u := units(t).Units()
	for _, c := range []struct {
		in   string
		f    float64
		want string
	}{{"g", 0.001, "kg"}, {"kg", 1, "kg"}, {"mg", 1e-06, "kg"}, {"W*s^3/m^2", 1, "kg"}} {
		if f, got, err := u.Normalize(1, c.in); err != nil || got != c.want || f != c.f {
			t.Errorf("Normalize(1, %q) = %g %q %v, want %g %q", c.in, f, got, err, c.f, c.want)
		}
	}
}

// A prefix moves the decimal point rather than multiplying by an inexact power of ten, so 3300 mV is
// the 3.3 V a reading in volts holds, where 3300*1e-3 is 3.3000000000000003 (#205).
func TestFoldReadsAPrefixedValueAsTheDecimalItSpells(t *testing.T) {
	u := units(t).Units()
	for _, c := range []struct {
		f    float64
		in   string
		want float64
		unit string
	}{{3300, "mV", 3.3, "V"}, {1000, "mV", 1, "V"}, {4.7, "kohm", 4700, "ohm"}, {0.1, "uA", 1e-07, "A"}, {2.2, "nF", 2.2e-09, "F"}, {5, "V", 5, "V"}} {
		f, unit, changed := u.Fold(c.f, c.in)
		if f != c.want || unit != c.unit || changed != (c.in != c.unit) {
			t.Errorf("Fold(%g, %q) = %.17g %q %v, want %.17g %q", c.f, c.in, f, unit, changed, c.want, c.unit)
		}
	}
	if f, unit, changed := u.Fold(5, "m/"); changed || f != 5 || unit != "m/" {
		t.Errorf("a unit that doesn't parse folds to %g %q %v, want it kept", f, unit, changed)
	}
}
