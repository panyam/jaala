package datalog

import "github.com/panyam/jaala/ns"

// What a Source serves is normalized as the Base reads it, so by the time a query runs every value in
// a typed argument is that type and every plain number is written canonically (#162). A pin a Source
// stored as the number 3 in an entity argument reads as the text "3", as a query's 3 there does
// (coerceConstants); text in a number argument reads as its number when it is one; 1.50 reads as 1.5
// wherever it is. Text that says more than its number, such as 3.3V, keeps its text. Equality can then
// be strict, a number never equalling text, without a pin stored the wrong way going unmatched.
//
// A number's unit is folded as a query's constant is (foldUnits, #205): 5 in mV reads as 0.005 in V,
// and 1 in A*ohm as 1 in V, so a Source may serve prefixed values and 1000 mV and 1 V are one value
// wherever values meet. A number whose text only spelled it is written as the folded number; text
// saying more, such as "5 mV", is kept, as data's spelling is (#148).

// normalizeTuples returns tuples with each value read as its argument's type (coerceValue) and each
// plain number written canonically (canonicalNumber). A value the type can't read stays as the Source
// gave it. The Source's slices are never written: a tuple that changes is copied, and when none does
// tuples itself comes back.
func normalizeTuples(units *ns.Units, tuples []ns.Tuple, types []ns.ArgType) []ns.Tuple {
	var out []ns.Tuple
	for i, t := range tuples {
		vals, changed := normalizeValues(units, t.Vals, types)
		if !changed {
			continue
		}
		if out == nil {
			out = append([]ns.Tuple(nil), tuples...)
		}
		out[i] = ns.Tuple{Vals: vals, Cites: t.Cites}
	}
	if out == nil {
		return tuples
	}
	return out
}

// normalizeValues is one row's values read as types (see normalizeTuples): vals itself when nothing
// changes, else a copy, and whether it changed. A generator's emitted rows go through it too, typed by
// its Builtin's Types.
func normalizeValues(units *ns.Units, vals []ns.Value, types []ns.ArgType) ([]ns.Value, bool) {
	var out []ns.Value
	for j, v := range vals {
		n := v
		if j < len(types) {
			if c, ok := coerceValue(v, types[j]); ok {
				n = c
			}
		}
		n = canonicalNumber(foldUnit(units, n))
		if sameValue(n, v) {
			continue
		}
		if out == nil {
			out = append([]ns.Value(nil), vals...)
		}
		out[j] = n
	}
	if out == nil {
		return vals, false
	}
	return out, true
}

// sameValue reports whether a and b are the same value written the same way.
func sameValue(a, b ns.Value) bool {
	if a.Absent != b.Absent || a.S != b.S || a.BaseUnit != b.BaseUnit || (a.Num == nil) != (b.Num == nil) {
		return false
	}
	return a.Num == nil || *a.Num == *b.Num || (*a.Num != *a.Num && *b.Num != *b.Num)
}

// argTypesOf is rel's declared argument types, nil when it declares none.
func (b *Base) argTypesOf(rel string) []ns.ArgType {
	s, _ := b.schemaOf(rel)
	return s.Types
}

// foldUnit is v with its number's unit folded (see ns.Units.Fold): the scale into the number and the
// unit written by its name. Text that only spelled the number spells the folded one.
func foldUnit(units *ns.Units, v ns.Value) ns.Value {
	if v.Num == nil || v.BaseUnit == "" || v.Absent {
		return v
	}
	f, unit, changed := units.Fold(*v.Num, v.BaseUnit)
	if !changed {
		return v
	}
	if spellsNumber(v) {
		v.S = ftoa(f)
	}
	v.Num, v.BaseUnit = &f, unit
	return v
}

// units is the vocabulary's unit table, which folds what the Source serves (see foldUnit).
func (b *Base) units() *ns.Units {
	if b.reg == nil {
		return nil
	}
	return b.reg.Units()
}
