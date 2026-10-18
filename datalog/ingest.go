package datalog

import "github.com/panyam/jaala/ns"

// What a Source serves is normalized as the Base reads it, so by the time a query runs every value in
// a typed argument is that type and every plain number is written canonically (#162). A pin a Source
// stored as the number 3 in an entity argument reads as the text "3", as a query's 3 there does
// (coerceConstants); text in a number argument reads as its number when it is one; 1.50 reads as 1.5
// wherever it is. Text that says more than its number, such as 3.3V, keeps its text. Equality can then
// be strict, a number never equalling text, without a pin stored the wrong way going unmatched.
//
// When the vocabulary folds a Source's units (ns.Vocabulary.FoldSourceUnits, #205, opt-in since #303),
// a number's unit is folded as a query's constant is (foldUnits): 5 in mV reads as 0.005 in V, and 1
// in A*ohm as 1 in V, so 1000 mV and 1 V are one value wherever values meet. A number whose text only
// spelled it is written as the folded number; text saying more, such as "5 mV", is kept, as data's
// spelling is (#148). Otherwise a Source's unit is read as it is served, a tag compared by its text.

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
		out[i] = ns.Tuple{Vals: vals, Cites: t.Cites, ID: t.ID}
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
		n = canonicalNumber(units.FoldValue(n))
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

// units is the unit table the Base reads a Source's values through (ns.Units.FoldValue): the
// vocabulary's when it folds a Source's units (FoldSourceUnits, #303), else nil, which folds nothing.
func (b *Base) units() *ns.Units {
	if !b.reg.FoldsSourceUnits() {
		return nil
	}
	return b.reg.Units()
}

// withUnits is c carrying the unit table its values were folded by, for the store (ns.Conjunction.Units).
func (b *Base) withUnits(c ns.Conjunction) ns.Conjunction {
	c.Units = b.units()
	return c
}
