package ns

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// A Unit is a dimension as a scale and symbols with exponents: mV is {Scale: -3, V: 1} and m/s is
// {m: 1, s: -1} (jaala#5). The scale is a power of ten times an exact factor (Factor), so a host can
// name min as 60*s or KiB as 1024*B (jaala#303). Multiplying adds exponents and multiplies scales, so
// a unit nobody named (distance over time) is still a unit, written m/s. A Vocabulary names units
// through AddUnit, so A*ohm is V once V is defined as A*ohm.
//
// A unit is only symbols to the engine, which never knows what a volt or a metre is: two numbers
// order and aggregate together when their units are the same symbols, and a symbol nobody defined is
// a unit of its own, compared by its text.
type Unit struct {
	Scale int
	Dims  map[string]int
	// num over den is the factor beside the power of ten, in the one form normFactor writes, so a
	// scale has one representation; 0 reads as 1, so Unit{} is no unit. num is -1 once a product
	// overflowed, which parse refuses.
	num, den int64
}

// Factor is the exact factor u's scale has beside its power of ten, as a fraction: 6/1 for min
// defined as 60*s (with Scale 1), 1/1 for mV. The scale is num/den times ten to the Scale.
func (u Unit) Factor() (num, den int64) {
	num, den = u.num, u.den
	if num == 0 {
		num = 1
	}
	if den == 0 {
		den = 1
	}
	return num, den
}

// Unscaled is u's symbols with no scale: mV's is V, and min's (60*s) is s.
func (u Unit) Unscaled() Unit { return Unit{Dims: u.Dims} }

// scaled reports whether u has a scale other than one.
func (u Unit) scaled() bool {
	n, d := u.Factor()
	return u.Scale != 0 || n != 1 || d != 1
}

// overflowed reports whether a product of u's factors no longer fits exactly.
func (u Unit) overflowed() bool { return u.num < 0 || u.den < 0 }

// IsZero reports whether u is no unit at all: no symbols and no scale.
func (u Unit) IsZero() bool { return !u.scaled() && len(u.Dims) == 0 }

// Mul is u times o: scales multiply and exponents add.
func (u Unit) Mul(o Unit) Unit {
	un, ud := u.Factor()
	on, od := o.Factor()
	n, okn := mulExact(un, on)
	d, okd := mulExact(ud, od)
	out := Unit{Dims: map[string]int{}}
	if !okn || !okd || u.overflowed() || o.overflowed() {
		out.num = -1
	} else {
		out.Scale, out.num, out.den = normFactor(u.Scale+o.Scale, n, d)
	}
	for s, e := range u.Dims {
		out.Dims[s] += e
	}
	for s, e := range o.Dims {
		out.Dims[s] += e
	}
	for s, e := range out.Dims {
		if e == 0 {
			delete(out.Dims, s)
		}
	}
	return out
}

// Div is u over o.
func (u Unit) Div(o Unit) Unit { return u.Mul(o.Pow(-1)) }

// Pow is u to the n.
func (u Unit) Pow(n int) Unit {
	out := Unit{Dims: map[string]int{}}
	for s, e := range u.Dims {
		if e*n != 0 {
			out.Dims[s] = e * n
		}
	}
	un, ud := u.Factor()
	if n < 0 {
		un, ud = ud, un
	}
	num, den, ok := int64(1), int64(1), !u.overflowed()
	for i := 0; i < max(n, -n) && ok; i++ {
		var okn, okd bool
		num, okn = mulExact(num, un)
		den, okd = mulExact(den, ud)
		ok = okn && okd
	}
	if !ok {
		out.num = -1
		return out
	}
	out.Scale, out.num, out.den = normFactor(u.Scale*n, num, den)
	return out
}

// Same reports whether two units have one scale and the same symbols to the same powers.
func (u Unit) Same(o Unit) bool {
	un, ud := u.Factor()
	on, od := o.Factor()
	if u.Scale != o.Scale || un != on || ud != od || len(u.Dims) != len(o.Dims) {
		return false
	}
	for s, e := range u.Dims {
		if o.Dims[s] != e {
			return false
		}
	}
	return true
}

// String writes u with its symbols sorted, the ones with a positive power over the rest, as in
// A*ohm, m/s, m/(K*s^2) or 1/s, and a scale as a number first (1e3*V, 6e1*s, 1/(6e1*s)). ParseUnit
// reads it back.
func (u Unit) String() string {
	var num, den []string
	fn, fd := u.Factor()
	if fd != 1 {
		den = append(den, strconv.FormatInt(fd, 10))
	}
	syms := make([]string, 0, len(u.Dims))
	for s := range u.Dims {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	pow := func(s string, e int) string {
		if e == 1 {
			return s
		}
		return s + "^" + strconv.Itoa(e)
	}
	for _, s := range syms {
		if e := u.Dims[s]; e > 0 {
			num = append(num, pow(s, e))
		} else {
			den = append(den, pow(s, -e))
		}
	}
	if u.Scale != 0 || fn != 1 {
		coef := strconv.FormatInt(fn, 10)
		if u.Scale != 0 {
			coef += "e" + strconv.Itoa(u.Scale)
		}
		num = append([]string{coef}, num...)
	}
	out := strings.Join(num, "*")
	switch {
	case len(den) == 0:
		return out
	case out == "":
		out = "1"
	}
	if len(den) == 1 {
		return out + "/" + den[0]
	}
	return out + "/(" + strings.Join(den, "*") + ")"
}

// normFactor is the scale num/den times ten to the scale in its one form: the fraction in lowest
// terms with every power of ten moved into scale (1000 is 1e3, 1/4 is 25e-2, 60 is 6e1), so den has
// no factor of 2 or 5 and num no factor of 10. Same compares units by it.
func normFactor(scale int, num, den int64) (int, int64, int64) {
	g := gcd(num, den)
	num, den = num/g, den/g
	twos, fives := 0, 0
	for den%2 == 0 {
		den, twos = den/2, twos+1
	}
	for den%5 == 0 {
		den, fives = den/5, fives+1
	}
	k := max(twos, fives)
	var ok bool
	for i := twos; i < k; i++ {
		if num, ok = mulExact(num, 2); !ok {
			return scale, -1, 1
		}
	}
	for i := fives; i < k; i++ {
		if num, ok = mulExact(num, 5); !ok {
			return scale, -1, 1
		}
	}
	scale -= k
	for num%10 == 0 {
		num, scale = num/10, scale+1
	}
	return scale, num, den
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// mulExact is a times b for positive a and b, and false when it doesn't fit in an int64.
func mulExact(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a != 0 && b > math.MaxInt64/a {
		return 0, false
	}
	return a * b, true
}

// Units is a vocabulary's unit table: units defined in terms of others, and prefixes. A symbol it
// doesn't define is a base unit of its own, so a host's data can carry any unit.
type Units struct {
	defs     map[string]Unit // a defined unit, expanded into base symbols
	prefixes map[string]Unit // a prefix's scale, with no symbols: k is {Scale: 3}
	order    []string        // defined units in registration order: the first name of a unit is its name
	// through names, per base unit, the unit answers may write it through (see WriteBaseThrough), and
	// throughOrder those base units in the order they were named.
	through      map[string]string
	throughOrder []string
	// folds caches Fold per unit text, since a Source's values repeat a handful of units over many
	// tuples. AddUnit and WriteBaseThrough clear it.
	folds sync.Map // unit text -> folded
}

// folded is how Fold rewrites a number in one unit: scaled by scale, in unit.
type folded struct {
	scale Unit
	unit  string
	ok    bool
}

// AddUnit names a unit or a prefix. def is "" for a base unit, a number for a prefix ("1e3" for k,
// "1e-3" for m, "1024" for Ki), or a unit expression over units already known, such as "A*ohm" or
// "V/A" or "m/s^2", which may carry a number as a factor: "60*s" for min, "1024*B" for KiB
// (jaala#303). A number is a positive decimal with an optional exponent, kept exact. A unit's first
// name is how answers write it: once V is A*ohm, a product of amps and ohms is written V. Later
// names for the same unit (Ω for ohm) are read but not written.
func (v *Vocabulary) AddUnit(name, def string) error {
	if !isUnitSymbol(name) {
		return fmt.Errorf("query: unit %q isn't a name: letters only", name)
	}
	if v.units == nil {
		v.units = &Units{defs: map[string]Unit{}, prefixes: map[string]Unit{}}
	}
	t := v.units
	def = strings.TrimSpace(def)
	u, err := t.parse(def)
	if def != "" && err != nil {
		return fmt.Errorf("query: unit %q: %w", name, err)
	}
	// A prefix and a unit may share a name (m is milli and metre); a second of the same kind may not.
	if _, dup := t.defs[name]; dup && (def == "" || len(u.Dims) > 0) {
		return fmt.Errorf("query: unit %q is defined twice", name)
	}
	if _, dup := t.prefixes[name]; dup && def != "" && len(u.Dims) == 0 {
		return fmt.Errorf("query: prefix %q is defined twice", name)
	}
	if def == "" {
		t.defs[name] = Unit{Dims: map[string]int{name: 1}}
		t.order = append(t.order, name)
		t.folds.Clear()
		v.cache = newMemo()
		return nil
	}
	if len(u.Dims) == 0 {
		t.prefixes[name] = u
	} else {
		t.defs[name] = u
		t.order = append(t.order, name)
	}
	t.folds.Clear()
	v.cache = newMemo()
	return nil
}

// Units is the vocabulary's unit table, nil when it names none. A query's constants and num.*'s
// results are always written through it; a Source's values are read through it only after
// FoldSourceUnits.
func (v *Vocabulary) Units() *Units { return v.units }

// FoldSourceUnits has the engine fold a Source's numbers through the unit table as it reads them
// (jaala#205; opt-in since jaala#303): served as 1000 in mV, a value reads as 1 in V and equals, keys,
// groups and orders with the 1 V a query or another Source writes. Without it, a Source's unit is a
// tag compared by its text, as a host serving lengths in mm wants: 0.08 in mm stays 0.08 in mm, and
// a bare 0.1 compares with it in mm. With it, the engine also asks a store by folded values, in a
// lookup, a pushed conjunction (Conjunction.Units) and a walk, and a Federation folds what it carries
// from one member to the next. Call it before the vocabulary is used.
func (v *Vocabulary) FoldSourceUnits() {
	v.foldSource = true
	v.cache = newMemo()
}

// FoldsSourceUnits reports whether FoldSourceUnits was called on v.
func (v *Vocabulary) FoldsSourceUnits() bool { return v != nil && v.foldSource }

// clone is an independent copy of t, for Vocabulary.Clone.
func (t *Units) clone() *Units {
	if t == nil {
		return nil
	}
	return &Units{defs: maps.Clone(t.defs), prefixes: maps.Clone(t.prefixes), order: slices.Clone(t.order),
		through: maps.Clone(t.through), throughOrder: slices.Clone(t.throughOrder)}
}

// ParseUnit reads a unit as a query writes it: a symbol, a prefixed symbol (kohm, mV), or a product
// or quotient of them with powers (A*ohm, m/s^2, m/(K*s^2)), expanded through the table's
// definitions. An unknown symbol is a base unit of its own. A nil table knows no names or prefixes.
func (t *Units) ParseUnit(s string) (Unit, error) { return t.parse(s) }

// Normalize is a number in a unit as the engine keeps it: the scale folded into the number, and the
// unit written by its name (5 in kohm is 5000 in ohm; 6 in A*ohm is 6 in V). A dimensionless unit
// leaves no symbol. An empty unit is the number as it is.
func (t *Units) Normalize(f float64, unit string) (float64, string, error) {
	if unit == "" {
		return f, "", nil
	}
	u, err := t.parse(unit)
	if err != nil {
		return 0, "", err
	}
	return t.Write(f, u)
}

// Write is a number in a unit as the engine keeps it, as Normalize returns it.
func (t *Units) Write(f float64, u Unit) (float64, string, error) {
	if u.scaled() {
		f = scale(f, u)
		u = u.Unscaled()
	}
	return f, t.name(u), nil
}

// scale is f in a unit with u's scale, written in the unit without it: f times the factor, then
// shifted by the power of ten, so 1.5 in min (60*s) is 90 and 3300 in mV is 3.3.
func scale(f float64, u Unit) float64 {
	if n, d := u.Factor(); n != 1 || d != 1 {
		f = f * float64(n) / float64(d)
	}
	if u.Scale == 0 {
		return f
	}
	return shift(f, u.Scale)
}

// shift is f times ten to the n, as the decimal f is written would read with its exponent moved, so
// 3300 in mV is the 3.3 a V reads as, where 3300*1e-3 is 3.3000000000000003 (jaala#205).
func shift(f float64, n int) float64 {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return f
	}
	mant, exp, _ := strings.Cut(strconv.FormatFloat(f, 'e', -1, 64), "e")
	e, _ := strconv.Atoi(exp)
	out, err := strconv.ParseFloat(mant+"e"+strconv.Itoa(e+n), 64)
	if err != nil && !math.IsInf(out, 0) {
		return f * math.Pow(10, float64(n))
	}
	return out
}

// Fold is a number read from a Source in unit as the engine keeps it, as Normalize writes it (5 in mV
// is 0.005 in V), and whether that changed anything (jaala#205). A unit that doesn't parse is kept as
// it is, a unit of its own, so a Source's value is read and never refused. Cached per unit, and safe
// for concurrent use once the table is built.
func (t *Units) Fold(f float64, unit string) (float64, string, bool) {
	if unit == "" || t == nil {
		return f, unit, false // with no table, no names and no prefixes: every unit is its own
	}
	var fo folded
	if c, ok := t.folds.Load(unit); ok {
		fo = c.(folded)
	} else {
		if u, err := t.parse(unit); err == nil {
			fo = folded{scale: Unit{Scale: u.Scale, num: u.num, den: u.den}, unit: t.name(u.Unscaled()), ok: true}
		}
		t.folds.Store(unit, fo)
	}
	if !fo.ok || !fo.scale.scaled() && fo.unit == unit {
		return f, unit, false
	}
	return scale(f, fo.scale), fo.unit, true
}

// FoldValue is v as the engine holds it once its number's unit is folded (Fold): NU(1000, "mV") is
// NU(1, "V"). A number whose text only spelled it spells the folded number; text saying more, such as
// "5 mV", is kept. Anything else, and every value under a nil table, comes back as it is. A store
// holding prefixed values folds its own with it to compare them with what the engine asks (#205).
func (t *Units) FoldValue(v Value) Value {
	if v.Num == nil || v.BaseUnit == "" || v.Absent {
		return v
	}
	f, unit, changed := t.Fold(*v.Num, v.BaseUnit)
	if !changed {
		return v
	}
	if n, err := strconv.ParseFloat(v.S, 64); err == nil && n == *v.Num {
		v.S = ftoa(f)
	}
	v.Num, v.BaseUnit = &f, unit
	return v
}

// SameUnit reports whether two units the engine holds are one unit, through the table's names (so
// ohm and V/A are). An empty unit is no unit.
func (t *Units) SameUnit(a, b string) bool {
	if a == b {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	ua, err1 := t.parse(a)
	ub, err2 := t.parse(b)
	return err1 == nil && err2 == nil && ua.Same(ub)
}

// name is u's first registered name when it has one. Otherwise it's u spelled with at most one named
// unit factored out, the one leaving the fewest symbols behind (ohm/s, not V/(A*s)), or u's own
// spelling when no name shortens it. A base unit written through another (see WriteBaseThrough) is
// spelled both ways, and the shorter is written, the one through the unit on a tie: with kg written
// through V, V/m is V/m rather than kg*m/(A*s^3), and kg*m stays kg*m.
func (t *Units) name(u Unit) string {
	if len(u.Dims) == 0 && !u.scaled() {
		return ""
	}
	if t == nil {
		return u.String()
	}
	for _, n := range t.order {
		if t.defs[n].Same(u) {
			return n
		}
	}
	if len(t.throughOrder) == 0 {
		return t.spell(u, "").String()
	}
	var best Unit
	for i, base := range t.throughOrder {
		if s := t.spell(u, base); i == 0 || s.size() < best.size() {
			best = s
		}
	}
	return best.String()
}

// spell is u with at most one named unit factored out (see name), in base symbols, or with base
// written through the unit WriteBaseThrough named for it when base isn't "".
func (t *Units) spell(u Unit, base string) Unit {
	in, via := func(x Unit) Unit { return x }, ""
	if base != "" {
		via = t.through[base]
		r := t.defs[via].Div(Unit{Dims: map[string]int{base: 1}}) // base is via over r
		in = func(x Unit) Unit {
			k := x.Dims[base]
			if k == 0 {
				return x
			}
			y := x.Div(Unit{Dims: map[string]int{base: k}}).Div(r.Pow(k))
			y.Dims[via] += k
			return y
		}
	}
	w := in(u)
	best, bestName := w, ""
	for _, n := range t.order {
		def := t.defs[n]
		if def.scaled() || len(def.Dims) == 1 && def.Dims[n] == 1 || n == via {
			continue // a base unit is already its own symbol, as is the one written through, and u holds no scale
		}
		def = in(def)
		if rest := w.Div(def); rest.size()+1 < best.size() {
			best, bestName = rest, n
		}
	}
	if bestName != "" {
		best.Dims[bestName]++
	}
	return best
}

// size is how many symbols u is written with, powers counted.
func (u Unit) size() int {
	s := 0
	for _, e := range u.Dims {
		s += max(e, -e)
	}
	return s
}

// WriteBaseThrough lets answers write the base unit base through unit, a unit defined with base to
// the first power: stdlib writes kg through V, so that once V is kg*m^2/(A*s^3) an electrical unit
// is still written in volts (V/m, not kg*m/(A*s^3)). A unit is spelled both with base and through
// unit, and the shorter is written, the one through unit on a tie. It changes no unit's value, only
// how one with no name of its own is written.
func (v *Vocabulary) WriteBaseThrough(base, unit string) error {
	t := v.units
	if t == nil || !t.defs[base].Same(Unit{Dims: map[string]int{base: 1}}) {
		return fmt.Errorf("query: unit %q isn't a base unit", base)
	}
	def, ok := t.defs[unit]
	if !ok || def.scaled() || def.Dims[base] != 1 {
		return fmt.Errorf("query: unit %q isn't defined with %q to the first power", unit, base)
	}
	if via, dup := t.through[base]; dup {
		return fmt.Errorf("query: unit %q is already written through %q", base, via)
	}
	if t.through == nil {
		t.through = map[string]string{}
	}
	t.through[base] = unit
	t.throughOrder = append(t.throughOrder, base)
	t.folds.Clear()
	v.cache = newMemo()
	return nil
}

func (t *Units) symbol(s string) (Unit, bool) {
	if t != nil {
		if u, ok := t.defs[s]; ok {
			return u, true
		}
		// A prefix only when what follows it is a unit the table knows, so min stays min, and the
		// longest prefix first, so the reading doesn't depend on map order.
		ps := make([]string, 0, len(t.prefixes))
		for p := range t.prefixes {
			ps = append(ps, p)
		}
		sort.Slice(ps, func(i, j int) bool {
			if len(ps[i]) != len(ps[j]) {
				return len(ps[i]) > len(ps[j])
			}
			return ps[i] < ps[j]
		})
		for _, p := range ps {
			if rest, ok := strings.CutPrefix(s, p); ok && rest != "" {
				if u, ok := t.defs[rest]; ok {
					return u.Mul(t.prefixes[p]), true
				}
			}
		}
	}
	return Unit{}, false
}

// parse reads a unit expression: factors joined by * and /, a factor a symbol with an optional ^n, a
// number (1e3, 60, 2.54e-2; 1 for none), or a parenthesized expression.
func (t *Units) parse(s string) (Unit, error) {
	p := &unitParser{t: t, s: s}
	u, err := p.expr()
	if err != nil {
		return Unit{}, err
	}
	if p.skip(); p.i < len(p.s) {
		return Unit{}, fmt.Errorf("unit %q: unexpected %q", s, p.s[p.i:])
	}
	if u.overflowed() {
		return Unit{}, fmt.Errorf("unit %q: its scale is too large to keep exactly", s)
	}
	return u, nil
}

type unitParser struct {
	t *Units
	s string
	i int
}

func (p *unitParser) skip() {
	for p.i < len(p.s) && p.s[p.i] == ' ' {
		p.i++
	}
}

func (p *unitParser) expr() (Unit, error) {
	u, err := p.factor()
	if err != nil {
		return Unit{}, err
	}
	for {
		p.skip()
		if p.i >= len(p.s) || (p.s[p.i] != '*' && p.s[p.i] != '/') {
			return u, nil
		}
		op := p.s[p.i]
		p.i++
		f, err := p.factor()
		if err != nil {
			return Unit{}, err
		}
		if op == '*' {
			u = u.Mul(f)
		} else {
			u = u.Div(f)
		}
	}
}

func (p *unitParser) factor() (Unit, error) {
	p.skip()
	if p.i >= len(p.s) {
		return Unit{}, fmt.Errorf("unit %q ends early", p.s)
	}
	var u Unit
	switch c := p.s[p.i]; {
	case c == '(':
		p.i++
		inner, err := p.expr()
		if err != nil {
			return Unit{}, err
		}
		if p.skip(); p.i >= len(p.s) || p.s[p.i] != ')' {
			return Unit{}, fmt.Errorf("unit %q: unclosed (", p.s)
		}
		p.i++
		u = inner
	case c >= '0' && c <= '9':
		start := p.i
		for p.i < len(p.s) && (p.s[p.i] >= '0' && p.s[p.i] <= '9' || p.s[p.i] == '.' || p.s[p.i] == 'e' || p.s[p.i] == '-' || p.s[p.i] == '+') {
			p.i++
		}
		text := p.s[start:p.i]
		n, ok := scaleOf(text)
		if !ok {
			return Unit{}, fmt.Errorf("unit %q: %q isn't a positive decimal number (60, 1e3, 2.54e-2)", p.s, text)
		}
		u = n
	default:
		start := p.i
		for p.i < len(p.s) {
			r, w := utf8.DecodeRuneInString(p.s[p.i:])
			if !unicode.IsLetter(r) {
				break
			}
			p.i += w
		}
		sym := p.s[start:p.i]
		if sym == "" {
			return Unit{}, fmt.Errorf("unit %q: unexpected %q", p.s, p.s[p.i:])
		}
		if known, ok := p.t.symbol(sym); ok {
			u = known
		} else {
			u = Unit{Dims: map[string]int{sym: 1}}
		}
	}
	if p.skip(); p.i < len(p.s) && p.s[p.i] == '^' {
		p.i++
		start := p.i
		if p.i < len(p.s) && p.s[p.i] == '-' {
			p.i++
		}
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
		}
		n, err := strconv.Atoi(p.s[start:p.i])
		if err != nil {
			return Unit{}, fmt.Errorf("unit %q: a power is a whole number", p.s)
		}
		u = u.Pow(n)
	}
	return u, nil
}

// scaleOf is a positive decimal number as a scale with no symbols, kept exact: 2.54e-2 is 254 times
// ten to the -4.
func scaleOf(text string) (Unit, bool) {
	mant, exp, hasExp := strings.Cut(text, "e")
	e := 0
	if hasExp {
		var err error
		if e, err = strconv.Atoi(exp); err != nil {
			return Unit{}, false
		}
	}
	whole, frac, _ := strings.Cut(mant, ".")
	digits := whole + frac
	if digits == "" || strings.ContainsAny(digits, ".+-") {
		return Unit{}, false
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 {
		return Unit{}, false
	}
	scale, num, den := normFactor(e-len(frac), n, 1)
	return Unit{Scale: scale, num: num, den: den}, true
}

func isUnitSymbol(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}
