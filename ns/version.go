package ns

import (
	"iter"
	"math"
	"math/bits"
	"slices"
	"sort"
)

// A Version names one version of a TimedSource's facts, such as a revision of a design or a commit of
// a repo (#179). The host numbers its versions; the engine only compares and collects them. Linear
// history numbers them in order, so a fact that holds from one revision on is one range. History that
// branches numbers them however it likes and uses a BitSet.
type Version uint32

// Open ends a range that still holds: [v, Open) is v and every version after it. It is never a
// version itself.
const Open Version = math.MaxUint32

// A VersionRange is the versions from From up to, but not including, To. To is Open for a range that
// still holds.
type VersionRange struct{ From, To Version }

// A VersionSet is the versions a fact holds in. Two implementations come with ns, IntervalSet for
// linear history and BitSet for history that branches, and either combines with the other: an
// operation returns the receiver's kind, reading the other through Ranges.
type VersionSet interface {
	// Contains reports whether v is in the set. It is false for Open.
	Contains(v Version) bool
	// Empty reports whether the set holds no version.
	Empty() bool
	// Ranges are the set's versions as ranges, ascending, none empty, and none touching the next: two
	// ranges that meet are reported as one.
	Ranges() iter.Seq[VersionRange]
	Intersect(o VersionSet) VersionSet
	Union(o VersionSet) VersionSet
	// Minus is the versions of the set that aren't in o.
	Minus(o VersionSet) VersionSet
}

// VersionsIn lists s's versions in ascending order. A set holding a range up to Open lists without
// end, so a caller ranging over one stops on its own.
func VersionsIn(s VersionSet) iter.Seq[Version] {
	return func(yield func(Version) bool) {
		for r := range s.Ranges() {
			for v := r.From; v < r.To; v++ {
				if !yield(v) {
					return
				}
			}
		}
	}
}

// SameVersions reports whether a and b hold the same versions, whatever their kinds.
func SameVersions(a, b VersionSet) bool {
	return slices.Equal(rangesOf(a), rangesOf(b))
}

// rangesOf is s's ranges as a slice, without copying an IntervalSet's.
func rangesOf(s VersionSet) []VersionRange {
	if s == nil {
		return nil
	}
	if is, ok := s.(IntervalSet); ok {
		return is.r
	}
	return slices.Collect(s.Ranges())
}

// An IntervalSet is a VersionSet held as ranges, for linear history: a fact that holds from one
// revision until another is one range, and a new version only changes the ranges of the facts it
// changed. Ranges that meet are joined, so a set has one form. The zero IntervalSet is empty.
type IntervalSet struct{ r []VersionRange }

// Intervals is the set holding every version of rs, which may overlap and come in any order.
func Intervals(rs ...VersionRange) IntervalSet {
	r := slices.Clone(rs)
	sort.Slice(r, func(i, j int) bool { return r[i].From < r[j].From })
	return IntervalSet{coalesce(r)}
}

// Span is the set of the versions from from up to, but not including, to.
func Span(from, to Version) IntervalSet { return Intervals(VersionRange{from, to}) }

// Since is the set of v and every version after it: a fact that still holds.
func Since(v Version) IntervalSet { return Span(v, Open) }

// IntervalsOf is s as an IntervalSet.
func IntervalsOf(s VersionSet) IntervalSet {
	if is, ok := s.(IntervalSet); ok {
		return is
	}
	return IntervalSet{rangesOf(s)}
}

func (s IntervalSet) Contains(v Version) bool {
	i := sort.Search(len(s.r), func(i int) bool { return s.r[i].To > v })
	return i < len(s.r) && s.r[i].From <= v && v != Open
}

func (s IntervalSet) Empty() bool { return len(s.r) == 0 }

func (s IntervalSet) Ranges() iter.Seq[VersionRange] {
	return func(yield func(VersionRange) bool) {
		for _, r := range s.r {
			if !yield(r) {
				return
			}
		}
	}
}

func (s IntervalSet) Intersect(o VersionSet) VersionSet {
	return IntervalSet{combine(s.r, rangesOf(o), func(a, b bool) bool { return a && b })}
}

func (s IntervalSet) Union(o VersionSet) VersionSet {
	return IntervalSet{combine(s.r, rangesOf(o), func(a, b bool) bool { return a || b })}
}

func (s IntervalSet) Minus(o VersionSet) VersionSet {
	return IntervalSet{combine(s.r, rangesOf(o), func(a, b bool) bool { return a && !b })}
}

// coalesce drops r's empty ranges and joins those that overlap or meet, r sorted by From.
func coalesce(r []VersionRange) []VersionRange {
	out := r[:0]
	for _, x := range r {
		if x.From >= x.To {
			continue
		}
		if n := len(out); n > 0 && x.From <= out[n-1].To {
			out[n-1].To = max(out[n-1].To, x.To)
			continue
		}
		out = append(out, x)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// combine sweeps two coalesced range lists and keeps the stretches where keep holds of their
// memberships.
func combine(a, b []VersionRange, keep func(inA, inB bool) bool) []VersionRange {
	var out []VersionRange
	i, j := 0, 0
	at := Version(0)
	for {
		// Membership is constant from at until the next boundary of either list.
		inA, nextA := sweep(a, &i, at)
		inB, nextB := sweep(b, &j, at)
		next := min(nextA, nextB)
		if keep(inA, inB) && at < next {
			if n := len(out); n > 0 && out[n-1].To == at {
				out[n-1].To = next
			} else {
				out = append(out, VersionRange{at, next})
			}
		}
		if next == Open {
			return out
		}
		at = next
	}
}

// sweep reports whether at is in r, and where that next changes, advancing *i past ranges ending at
// or before at.
func sweep(r []VersionRange, i *int, at Version) (bool, Version) {
	for *i < len(r) && r[*i].To <= at {
		*i++
	}
	if *i == len(r) {
		return false, Open
	}
	if at < r[*i].From {
		return false, r[*i].From
	}
	return true, r[*i].To
}

// A BitSet is a VersionSet held as bits, for history that branches, where a fact's versions are
// scattered rather than one range. Stretches of 64 versions are words, kept only where some are in
// the set, and a stretch of whole words all in it is kept as a run rather than its words, so a fact
// holding in most versions stays small too. The zero BitSet is empty.
type BitSet struct{ pieces []bitPiece }

// A bitPiece covers the words from word for n words: a run of whole words when bits is nil, else
// those words, the first and last nonzero.
type bitPiece struct {
	word, n uint64
	bits    []uint64
}

const lastWord = uint64(Open) / 64 // the word holding Open; one past every word is lastWord+1

func (p bitPiece) end() uint64 { return p.word + p.n }

// Bits is the set of the given versions.
func Bits(vs ...Version) BitSet {
	var r []VersionRange
	for _, v := range vs {
		if v != Open {
			r = append(r, VersionRange{v, v + 1})
		}
	}
	return BitsOf(Intervals(r...))
}

// BitsOf is s as a BitSet.
func BitsOf(s VersionSet) BitSet {
	if bs, ok := s.(BitSet); ok {
		return bs
	}
	var b bitBuilder
	for _, r := range rangesOf(s) {
		from, to := uint64(r.From), uint64(r.To)
		if r.To == Open {
			to = (lastWord + 1) * 64 // a run to the end; Contains and Ranges never report Open itself
		}
		for from < to {
			w := from / 64
			if from%64 == 0 && to-from >= 64 {
				n := (to - from) / 64
				b.add(w, n, nil)
				from += n * 64
				continue
			}
			hi := min(to, (w+1)*64)
			word := (^uint64(0) >> (64 - (hi - from))) << (from % 64)
			b.add(w, 1, []uint64{word})
			from = hi
		}
	}
	return b.set()
}

func (s BitSet) Contains(v Version) bool {
	if v == Open {
		return false
	}
	w := uint64(v) / 64
	i := sort.Search(len(s.pieces), func(i int) bool { return s.pieces[i].end() > w })
	if i == len(s.pieces) || s.pieces[i].word > w {
		return false
	}
	p := s.pieces[i]
	return p.bits == nil || p.bits[w-p.word]&(1<<(uint64(v)%64)) != 0
}

func (s BitSet) Empty() bool { return len(s.pieces) == 0 }

func (s BitSet) Ranges() iter.Seq[VersionRange] {
	return func(yield func(VersionRange) bool) {
		var cur VersionRange
		open := false
		emit := func(from, to uint64) bool {
			f, t := Version(min(from, uint64(Open))), Version(min(to, uint64(Open)))
			if f >= t {
				return true
			}
			if open && cur.To == f {
				cur.To = t
				return true
			}
			if open && !yield(cur) {
				return false
			}
			cur, open = VersionRange{f, t}, true
			return true
		}
		for _, p := range s.pieces {
			if p.bits == nil {
				if !emit(p.word*64, p.end()*64) {
					return
				}
				continue
			}
			for k, word := range p.bits {
				base := (p.word + uint64(k)) * 64
				for word != 0 {
					lo := uint64(bits.TrailingZeros64(word))
					ones := uint64(bits.TrailingZeros64(^(word >> lo)))
					if lo+ones >= 64 {
						ones = 64 - lo
					}
					if !emit(base+lo, base+lo+ones) {
						return
					}
					if lo+ones == 64 {
						break
					}
					word &^= (^uint64(0) >> (64 - ones)) << lo
				}
			}
		}
		if open {
			yield(cur)
		}
	}
}

func (s BitSet) Intersect(o VersionSet) VersionSet {
	return mergeBits(s, BitsOf(o), func(a, b uint64) uint64 { return a & b })
}

func (s BitSet) Union(o VersionSet) VersionSet {
	return mergeBits(s, BitsOf(o), func(a, b uint64) uint64 { return a | b })
}

func (s BitSet) Minus(o VersionSet) VersionSet {
	return mergeBits(s, BitsOf(o), func(a, b uint64) uint64 { return a &^ b })
}

// mergeBits combines two BitSets word by word through op, which must map two zero words to zero. A
// stretch where both sides are runs or absent is combined once, however many words it spans, so a run
// up to Open costs one step.
func mergeBits(a, b BitSet, op func(a, b uint64) uint64) BitSet {
	var out bitBuilder
	i, j := 0, 0
	at := uint64(0)
	for {
		ka, wa, nextA := pieceAt(a.pieces, &i, at)
		kb, wb, nextB := pieceAt(b.pieces, &j, at)
		next := min(nextA, nextB)
		if next == math.MaxUint64 {
			return out.set()
		}
		n := next - at
		if wa == nil && wb == nil {
			if word := op(ka, kb); word == ^uint64(0) {
				out.add(at, n, nil)
			} else if word != 0 {
				out.add(at, n, slices.Repeat([]uint64{word}, int(n)))
			}
		} else {
			words := make([]uint64, n)
			for k := range words {
				x, y := ka, kb
				if wa != nil {
					x = wa[k]
				}
				if wb != nil {
					y = wb[k]
				}
				words[k] = op(x, y)
			}
			out.add(at, n, words)
		}
		at = next
	}
}

// pieceAt reads ps at word at: the constant word it holds there (zero when absent, all ones in a run)
// or its words from at, and the word where that next changes (MaxUint64 past the last piece).
func pieceAt(ps []bitPiece, i *int, at uint64) (uint64, []uint64, uint64) {
	for *i < len(ps) && ps[*i].end() <= at {
		*i++
	}
	if *i == len(ps) {
		return 0, nil, math.MaxUint64
	}
	p := ps[*i]
	if at < p.word {
		return 0, nil, p.word
	}
	if p.bits == nil {
		return ^uint64(0), nil, p.end()
	}
	return 0, p.bits[at-p.word:], p.end()
}

// bitBuilder appends words in ascending order into a BitSet's one form: zero words dropped, two or
// more whole words in a row a run, and touching pieces of a kind joined.
type bitBuilder struct{ pieces []bitPiece }

func (b *bitBuilder) add(word, n uint64, words []uint64) {
	if words == nil {
		b.run(word, n)
		return
	}
	for k := 0; k < len(words); {
		switch {
		case words[k] == 0:
			k++
		case words[k] == ^uint64(0) && k+1 < len(words) && words[k+1] == ^uint64(0):
			e := k
			for e < len(words) && words[e] == ^uint64(0) {
				e++
			}
			b.run(word+uint64(k), uint64(e-k))
			k = e
		default:
			b.dense(word+uint64(k), words[k])
			k++
		}
	}
}

func (b *bitBuilder) run(word, n uint64) {
	if n == 0 {
		return
	}
	if last := len(b.pieces) - 1; last >= 0 && b.pieces[last].bits == nil && b.pieces[last].end() == word {
		b.pieces[last].n += n
		return
	}
	b.pieces = append(b.pieces, bitPiece{word: word, n: n})
}

func (b *bitBuilder) dense(word, bitsWord uint64) {
	last := len(b.pieces) - 1
	if last >= 0 && b.pieces[last].bits != nil && b.pieces[last].end() == word+1 {
		// Two ranges in one word, from BitsOf.
		b.pieces[last].bits[b.pieces[last].n-1] |= bitsWord
		return
	}
	if last >= 0 && b.pieces[last].bits != nil && b.pieces[last].end() == word {
		b.pieces[last].bits = append(b.pieces[last].bits, bitsWord)
		b.pieces[last].n++
		return
	}
	b.pieces = append(b.pieces, bitPiece{word: word, n: 1, bits: []uint64{bitsWord}})
}

func (b *bitBuilder) set() BitSet { return BitSet{b.pieces} }
