package ns

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// A Unit is a dimension as a power of ten and symbols with exponents: mV is {Scale: -3, V: 1} and m/s
// is {m: 1, s: -1} (jaala#5). Multiplying adds exponents, scale included, so a unit nobody named
// (distance over time) is still a unit, written m/s. A Vocabulary names units through AddUnit, so
// A*ohm is V once V is defined as A*ohm.
type Unit struct {
	Scale int
	Dims  map[string]int
}

// IsZero reports whether u is no unit at all: no symbols and no scale.
func (u Unit) IsZero() bool { return u.Scale == 0 && len(u.Dims) == 0 }

// Mul is u times o: scales and exponents add.
func (u Unit) Mul(o Unit) Unit {
	out := Unit{Scale: u.Scale + o.Scale, Dims: map[string]int{}}
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
	out := Unit{Scale: u.Scale * n, Dims: map[string]int{}}
	for s, e := range u.Dims {
		if e*n != 0 {
			out.Dims[s] = e * n
		}
	}
	return out
}

// Same reports whether two units have one scale and the same symbols to the same powers.
func (u Unit) Same(o Unit) bool {
	if u.Scale != o.Scale || len(u.Dims) != len(o.Dims) {
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
// A*ohm, m/s, m/(K*s^2) or 1/s, and a scale as a factor of ten first (1e3*V). ParseUnit reads it back.
func (u Unit) String() string {
	var num, den []string
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
	if u.Scale != 0 {
		num = append([]string{"1e" + strconv.Itoa(u.Scale)}, num...)
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

// Units is a vocabulary's unit table: units defined in terms of others, and prefixes. A symbol it
// doesn't define is a base unit of its own, so a host's data can carry any unit.
type Units struct {
	defs     map[string]Unit // a defined unit, expanded into base symbols
	prefixes map[string]int  // a prefix's power of ten: k is 3
	order    []string        // defined units in registration order: the first name of a unit is its name
	// through names, per base unit, the unit answers may write it through (see WriteBaseThrough), and
	// throughOrder those base units in the order they were named.
	through      map[string]string
	throughOrder []string
	// folds caches Fold per unit text, since a Source's values repeat a handful of units over many
	// tuples. AddUnit and WriteBaseThrough clear it.
	folds sync.Map // unit text -> folded
}

// folded is how Fold rewrites a number in one unit: shifted by scale powers of ten, in unit.
type folded struct {
	scale int
	unit  string
	ok    bool
}

// AddUnit names a unit or a prefix. def is "" for a base unit, a power of ten for a prefix ("1e3"
// for k, "1e-3" for m), or a unit expression over units already known, such as "A*ohm" or "V/A"
// or "m/s^2". A unit's first name is how answers write it: once V is A*ohm, a product of amps and
// ohms is written V. Later names for the same unit (Ω for ohm) are read but not written.
func (v *Vocabulary) AddUnit(name, def string) error {
	if !isUnitSymbol(name) {
		return fmt.Errorf("query: unit %q isn't a name: letters only", name)
	}
	if v.units == nil {
		v.units = &Units{defs: map[string]Unit{}, prefixes: map[string]int{}}
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
		t.prefixes[name] = u.Scale
	} else {
		t.defs[name] = u
		t.order = append(t.order, name)
	}
	t.folds.Clear()
	v.cache = newMemo()
	return nil
}

// Units is the vocabulary's unit table, nil when it names none.
func (v *Vocabulary) Units() *Units { return v.units }

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
	if u.Scale != 0 {
		f = shift(f, u.Scale)
		u = Unit{Dims: u.Dims}
	}
	return f, t.name(u), nil
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
			fo = folded{scale: u.Scale, unit: t.name(Unit{Dims: u.Dims}), ok: true}
		}
		t.folds.Store(unit, fo)
	}
	if !fo.ok || fo.scale == 0 && fo.unit == unit {
		return f, unit, false
	}
	return shift(f, fo.scale), fo.unit, true
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
	if len(u.Dims) == 0 && u.Scale == 0 {
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
		if def.Scale != 0 || len(def.Dims) == 1 && def.Dims[n] == 1 || n == via {
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
	if !ok || def.Scale != 0 || def.Dims[base] != 1 {
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
					return u.Mul(Unit{Scale: t.prefixes[p]}), true
				}
			}
		}
	}
	return Unit{}, false
}

// parse reads a unit expression: factors joined by * and /, a factor a symbol with an optional ^n, a
// power of ten (1e3, written as 1 for none), or a parenthesized expression.
func (t *Units) parse(s string) (Unit, error) {
	p := &unitParser{t: t, s: s}
	u, err := p.expr()
	if err != nil {
		return Unit{}, err
	}
	if p.skip(); p.i < len(p.s) {
		return Unit{}, fmt.Errorf("unit %q: unexpected %q", s, p.s[p.i:])
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
		for p.i < len(p.s) && (p.s[p.i] >= '0' && p.s[p.i] <= '9' || p.s[p.i] == 'e' || p.s[p.i] == '-' || p.s[p.i] == '+') {
			p.i++
		}
		text := p.s[start:p.i]
		if text == "1" {
			u = Unit{}
			break
		}
		mant, exp, ok := strings.Cut(text, "e")
		n, err := strconv.Atoi(exp)
		if !ok || mant != "1" || err != nil {
			return Unit{}, fmt.Errorf("unit %q: %q isn't a power of ten (want 1eN)", p.s, text)
		}
		u = Unit{Scale: n}
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
