package ns

import "strconv"

// A Value is a scalar fact value. A fact carries a string and, when numeric, a number, so a bound
// term keeps both: string equality and numeric comparison both work with no re-parse.
type Value struct {
	S   string
	Num *float64
	// Absent marks a field the source did not state AT ALL, which is different from an empty string
	// and different from zero. A datasheet row stating only a maximum leaves its minimum absent.
	//
	// It is a field rather than something inferred from a nil Num because the two are not the same
	// question: a non-numeric string also has a nil Num, and conflating them means the engine
	// RECONSTRUCTS absence from a coincidence instead of representing it. Before it was
	// representable, an absent field bound to the empty string and ordering fell through to string
	// order, where "" precedes everything and so "passed" every upper-bound test, including against a
	// negative threshold.
	Absent bool
	// BaseUnit is the unit this value's number is expressed in ("V", "A", UnitOhm, "mm"), or "" for a
	// dimensionless value or a non-numeric one.
	//
	// To the engine a unit is a tag: two numbers order, and aggregate together, only when their units
	// are the same text, and an empty unit takes whichever unit it meets (jaala#303). What a unit means
	// (that mV is a thousandth of a V) is the vocabulary's, through its unit table (Vocabulary.AddUnit):
	// a query's constants and num.*'s results are written through it, and a Source's values are read
	// through it only when the vocabulary folds a Source's units (Vocabulary.FoldSourceUnits). Then a
	// Source may serve a prefixed or compound unit ("mV", "kohm", "A*ohm") and the engine folds it as it
	// reads the value (Units.Fold, jaala#205), so 1000 in mV reads as 1 in V and equals, keys, groups
	// and orders with a 1 V the Source also serves, and a Source answering a lookup, a pushed
	// conjunction or a filter is asked by the folded value and has to answer by it. Otherwise the unit
	// is read as served: a host keeping lengths in mm writes a bare threshold in mm.
	//
	// Distinct from the `param.unit` RELATION, which reports what the vendor PRINTED ("mV") for a
	// human checking a citation. This says what the number IS in, for a machine comparing it.
	BaseUnit string
}

// ftoa is the canonical string form of a number, the S that N and NU give a numeric Value.
func ftoa(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }
