package ns

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// Unknown describes a name the vocabulary does not hold, for an error that follows "query: ". It names
// what is missing and adds a hint, so the most common newcomer error, a mistyped or half-remembered
// name, is answered with the vocabulary at the moment it is needed:
//
//	unknown relation "egde"; did you mean "edge"?
//	unknown module "nett" in "nett.pin_count"; did you mean "net.pin_count"?
//	"str" is a module, not a relation; it holds contains, distance, glob, match, prefix, suffix
//	"pin" is a module, not a relation; it holds name, net; did you mean "component.pin"?
//
// A missing module is named as such because "unknown relation" would send the reader hunting a typo
// in the last segment when the first one is wrong.
func (v *Vocabulary) Unknown(name string) string {
	if !v.hasBase() {
		return fmt.Sprintf("unknown relation %q%s", name, v.Hint(name))
	}
	if v.IsModule(name) {
		return fmt.Sprintf("%q is a module, not a relation; it holds %s%s", name, strings.Join(v.children(name), ", "), v.endingHint(name))
	}
	if mod, ok := v.missingModule(name); ok {
		return fmt.Sprintf("unknown module %q in %q%s", mod, name, v.Hint(name))
	}
	return fmt.Sprintf("unknown relation %q%s", name, v.Hint(name))
}

// Hint is the text after the name in an unknown-name error: `; did you mean "X"?` when a registered
// name is a plausible typo, an explanation when no relation is registered at all, and "" otherwise.
//
// A bare name is most often a member typed without its module (`rail` for `net.rail`), so a member
// elsewhere whose last segment IS the name comes first; when several do, they are all listed, since
// picking one would be a guess. Only then does a typo of a root name count, and only then a module
// path spelled with "_" or "-" for "." (`net_pin_count` for `net.pin_count`). Every case is a hint:
// the name still does not resolve.
//
// When no base relation is registered it says that instead of guessing, because every name is
// unknown in that state and a typo hint would send the reader hunting a spelling mistake they did not
// make. A host that forgot to install its relations still runs; this is the one moment the omission
// is visible.
func (v *Vocabulary) Hint(name string) string {
	if !v.hasBase() {
		if v != nil && v.hinter != nil {
			return "; " + v.hinter.NoVocabularyHint()
		}
		return "; no relations are installed"
	}
	if h := v.endingHint(name); h != "" {
		return h
	}
	if s := v.suggest(name); s != "" {
		return fmt.Sprintf(`; did you mean %q?`, s)
	}
	if mod, _ := splitPath(name); mod != "" && v.IsModule(mod) {
		return fmt.Sprintf("; %q holds %s", mod, strings.Join(v.children(mod), ", "))
	}
	return ""
}

// endingHint names the members elsewhere whose last segment is a bare name, or "".
func (v *Vocabulary) endingHint(name string) string {
	ends := v.endingIn(name)
	switch len(ends) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf(`; did you mean %q?`, ends[0])
	}
	q := make([]string, len(ends))
	for i, e := range ends {
		q[i] = fmt.Sprintf("%q", e)
	}
	list, both := strings.Join(q[:len(q)-1], ", ")+" and "+q[len(q)-1], "all"
	if len(ends) == 2 {
		both = "both"
	}
	return fmt.Sprintf("; %s %s end in %q", list, both, name)
}

// endingIn returns, for a bare name, the member paths in some module whose last segment is that name,
// sorted. A private member has no path, so it is never among them.
func (v *Vocabulary) endingIn(name string) []string {
	if strings.Contains(name, ".") {
		return nil
	}
	var out []string
	for p := range v.members {
		if mod, leaf := splitPath(p); mod != "" && leaf == name {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// bySeparator reads "_" and "-" in a bare name as ".", at each position alone and then all at once,
// and returns the module path nearest any of those spellings, within a plausible-typo distance:
// `net_pin_count` spells net.pin_count exactly, and `component_on_net` comes nearest to
// component.net.
func (v *Vocabulary) bySeparator(name string) string {
	near := false
	for _, p := range v.candidates() {
		if strings.Contains(p, ".") && !tooFar(name, p, typoThreshold(name)) {
			near = true
			break
		}
	}
	if !near {
		return "" // and no spellings built: there is one per separator, each as long as name
	}
	var spellings []string
	for i, r := range name {
		if r == '_' || r == '-' {
			spellings = append(spellings, name[:i]+"."+name[i+1:])
		}
	}
	if len(spellings) == 0 {
		return ""
	}
	spellings = append(spellings, strings.Map(func(r rune) rune {
		if r == '_' || r == '-' {
			return '.'
		}
		return r
	}, name))
	threshold := typoThreshold(name)
	var dotted []string
	for _, p := range v.candidates() {
		if strings.Contains(p, ".") && !tooFar(name, p, threshold) {
			dotted = append(dotted, p)
		}
	}
	best, bestDist := "", 0
	for _, sp := range spellings {
		for _, p := range dotted {
			if d := distance(sp, p); best == "" || d < bestDist {
				best, bestDist = p, d
			}
		}
	}
	if best != "" && bestDist <= threshold {
		return best
	}
	return ""
}

// typoThreshold is the edit distance a name of this length tolerates: a longer name tolerates more
// slips, with a floor of 2.
func typoThreshold(name string) int {
	return max(len(name)/4+1, 2)
}

// missingModule reports the module part of a dotted name when no module of that path exists.
func (v *Vocabulary) missingModule(name string) (string, bool) {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return "", false
	}
	mod := name[:i]
	return mod, !v.IsModule(mod)
}

// suggest returns the registered path a mistyped name most plausibly meant, or "".
//
// It searches where the name says to look. A name whose module exists is compared against that
// module's members only, so `net.pin_cout` finds `net.pin_count` and never a closer-spelled member
// of another module. A name whose module is missing is repaired at the module: the nearest existing
// module stands in for it, and the member is suggested if that module holds it, else the module is.
// A bare name that is no typo of a root name is tried as a module path written with another
// separator (see bySeparator). Hint has already looked for it as a member's last segment.
func (v *Vocabulary) suggest(name string) string {
	mod, leaf := "", name
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		mod, leaf = name[:i], name[i+1:]
	}
	if mod != "" && !v.IsModule(mod) {
		near := closest(mod, v.modulePaths())
		if near == "" {
			return ""
		}
		if _, ok := v.members[near+"."+leaf]; ok {
			return near + "." + leaf
		}
		return near
	}
	var cands []string
	for _, p := range v.candidates() {
		if parent, _ := splitPath(p); parent == mod {
			cands = append(cands, p)
		}
	}
	best := closestBy(leaf, cands, func(p string) string { _, l := splitPath(p); return l })
	if best == "" && mod == "" {
		return v.bySeparator(name)
	}
	if best == "" {
		return byWord(leaf, cands)
	}
	return best
}

// byWord returns the one candidate whose last segment is a word of leaf, split at "_" and "-", or "":
// `str.has_suffix` is str.suffix with a word another library spells it with (#276). Two such
// candidates would be a guess, so neither is named.
func byWord(leaf string, cands []string) string {
	words := strings.FieldsFunc(leaf, func(r rune) bool { return r == '_' || r == '-' })
	if len(words) < 2 {
		return ""
	}
	found := ""
	for _, p := range cands {
		if _, l := splitPath(p); slices.Contains(words, l) {
			if found != "" {
				return ""
			}
			found = p
		}
	}
	return found
}

// candidates is every member path a suggestion may name: base relations in the Source's order, then
// predicates and derived relations, each sorted. On a tie the earlier candidate wins, which is why the
// Source controls the order. A private member is never registered at a path, so it is never offered.
func (v *Vocabulary) candidates() []string {
	out := append([]string(nil), v.baseOrder...)
	out = append(out, v.namesOf(kindPredicate)...)
	return append(out, v.namesOf(kindDerived)...)
}

// modulePaths is every module path, sorted.
func (v *Vocabulary) modulePaths() []string {
	out := make([]string, 0, len(v.modules))
	for m, n := range v.modules {
		if n > 0 {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

// splitPath splits a path into its module and its last segment.
func splitPath(p string) (module, leaf string) {
	if i := strings.LastIndexByte(p, '.'); i >= 0 {
		return p[:i], p[i+1:]
	}
	return "", p
}

// closest returns the candidate nearest to name when it is within a plausible-typo distance.
func closest(name string, cands []string) string {
	return closestBy(name, cands, func(s string) string { return s })
}

// closestBy is closest comparing name against key(candidate), within typoThreshold, so an unrelated
// token gets no misleading suggestion. On a tie the earlier candidate wins.
func closestBy(name string, cands []string, key func(string) string) string {
	best, bestDist := "", 0
	threshold := typoThreshold(name)
	for _, c := range cands {
		k := key(c)
		if tooFar(name, k, threshold) {
			continue
		}
		if d := distance(name, k); best == "" || d < bestDist {
			best, bestDist = c, d
		}
	}
	if best != "" && bestDist <= threshold {
		return best
	}
	return ""
}

// tooFar reports whether a and b differ in length by more than limit, which puts their edit distance
// over it without computing it. A suggestion only names a candidate within its typo threshold, so
// skipping these changes no suggestion, and keeps a long name from costing its length times every
// candidate's (#89: 2,000 hyphens took most of a second per Eval).
func tooFar(a, b string, limit int) bool {
	d := utf8.RuneCountInString(a) - utf8.RuneCountInString(b)
	return d > limit || -d > limit
}

// distance is the edit distance suggestions rank by, a variable so a test can count what it costs.
var distance = levenshtein

// levenshtein is the edit distance (insert/delete/substitute) between two strings — the closeness
// measure suggestRelation ranks candidates by.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
		}
		prev = cur
	}
	return prev[len(rb)]
}

// DidYouMeanValue suggests the closest legal value for a rejected constant, as `, did you mean "X"?`,
// the value-level twin of Hint. A typo is the common case this whole check exists for, so naming the intended
// value is most of its worth; returns "" when nothing is close enough to be worth guessing.
func DidYouMeanValue(allowed []string, got string) string {
	best, bestDist := "", 0
	threshold := typoThreshold(got)
	for _, want := range allowed {
		if tooFar(got, want, threshold) {
			continue
		}
		if d := distance(got, want); best == "" || d < bestDist {
			best, bestDist = want, d
		}
	}
	if bestDist > threshold {
		best = ""
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf(", did you mean %q?", best)
}

// DidYouMean returns the hint an engine appends to an unknown-name error: `; did you mean "X"?`
// when a registered path is a plausible typo of name, an explanation when v holds no base relation
// at all, and "" otherwise. It is v.Hint, for a host that validates names itself, such as one
// checking a rule that arrived over its own wire format, so its errors read the same as the engine's.
func DidYouMean(v *Vocabulary, name string) string { return v.Hint(name) }

// Related lists, for a name the vocabulary doesn't hold and nothing plausibly misspells, the members
// a caller might have meant instead (#336): those taking arity arguments (any, when arity is
// negative), relations before predicates, and among them first the ones whose path or doc shares a
// word with name, at most four. It reads only what was registered, never Check, so an engine may call
// it while resolving modules.
func (v *Vocabulary) Related(name string, arity int) []string {
	if v == nil {
		return nil
	}
	words := map[string]bool{}
	for _, w := range splitWords(name) {
		words[w] = true
	}
	type cand struct {
		path   string
		shares bool
	}
	var rels, preds []cand
	consider := func(path string, takes func(int) bool, doc string, into *[]cand) {
		if arity >= 0 && !takes(arity) {
			return
		}
		shares := false
		for _, w := range append(splitWords(path), splitWords(doc)...) {
			shares = shares || words[w]
		}
		*into = append(*into, cand{path, shares})
	}
	for _, p := range v.candidates() {
		m := v.members[p]
		switch m.kind {
		case kindBase:
			consider(p, func(n int) bool { return n == m.schema.Arity }, m.schema.Doc, &rels)
		case kindPredicate:
			consider(p, m.pred.Accepts, m.pred.Doc, &preds)
		case kindDerived:
			_, leaf := splitPath(p)
			for _, d := range v.mods[m.module].Members {
				if d.Name == leaf {
					consider(p, func(n int) bool { return n == d.Arity }, d.Doc, &rels)
				}
			}
		}
	}
	var out []string
	for _, pass := range []bool{true, false} {
		for _, c := range append(rels, preds...) {
			if c.shares == pass && len(out) < 4 {
				out = append(out, c.path)
			}
		}
	}
	return out
}

// splitWords is a name's or a doc's words, lower case, of three letters or more, so `a`, `of` and
// `the` never make two names look related.
func splitWords(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(w) >= 3 {
			out = append(out, w)
		}
	}
	return out
}
