package datalog

import (
	"fmt"

	"github.com/panyam/jaala/ns"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The query text is read as tokens (#167): lex decides once what each run of characters is, a word, a
// number with its unit, a ?variable, a string or a symbol, and the parser reads those. Before, each
// part of the parser cut its piece of text and decided for itself, so text none of them expected got
// through as something else (#89, #129, #163, #286).

type tokenKind uint8

const (
	tokWord   tokenKind = iota // letters, digits and _, starting with a letter or _: edge, _, not
	tokNumber                  // a number as the grammar spells one, then any letters as its unit: 3.3V
	tokVar                     // ? and the ASCII ident after it, which may be empty: ?n, ?
	tokString                  // "text", unclosed when the input ends first
	tokSymbol                  // punctuation, an operator, or any other character
)

type token struct {
	kind tokenKind
	text string
	pos  int // byte offset of the token's first byte
	end  int // byte offset just past it
	unit int // tokNumber: where its unit starts in text, len(text) when it has none
	// tokString: closed; tokSymbol ".": followed by whitespace, ";" or the end, so it ends a clause (#276).
	flag bool
}

// symbols are the multi-character symbols, matched before a single character.
var symbols = []string{":-", "=>", "<=", ">=", "!="}

// source is a query's text, comments blanked, and its tokens. Blanking (rather than cutting) keeps
// every offset and the text an error quotes as it was written.
type source struct {
	text string
	toks []token
}

func lex(s string) *source {
	src := &source{text: blankComments(s)}
	src.toks = lexRange(src.text, 0, len(src.text))
	return src
}

// blankComments replaces every "#" comment, from the "#" to the end of its line, with spaces, unless the
// "#" sits inside a string.
func blankComments(s string) string {
	if !strings.Contains(s, "#") {
		return s
	}
	b := []byte(s)
	inQuote, inComment := false, false
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case inComment:
			if c == '\n' {
				inComment = false
			} else {
				b[i] = ' '
			}
		case c == '"':
			inQuote = !inQuote
		case c == '\\' && inQuote:
			i++ // the next character is the string's, a quote included (#314)
		case c == '#' && !inQuote:
			inComment = true
			b[i] = ' '
		}
	}
	return string(b)
}

// stringEnd returns the index in s of the quote that ends the string s starts inside, or -1. A backslash
// keeps the character after it in the string, so \" is a quote inside it; both stay as written, since a
// string takes no escapes (#314).
func stringEnd(s string) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

// lexRange reads the tokens of text[from:to], with offsets into text.
func lexRange(text string, from, to int) []token {
	var toks []token
	s := text[:to]
	for i := from; i < len(s); {
		r, w := utf8.DecodeRuneInString(s[i:])
		start := i
		switch {
		case unicode.IsSpace(r):
			i += w
			continue
		case r == '"':
			end := stringEnd(s[i+1:])
			closed := end >= 0
			if closed {
				i += end + 2
			} else {
				i = len(s)
			}
			toks = append(toks, token{kind: tokString, text: s[start:i], pos: start, end: i, flag: closed})
			continue
		case r == '?':
			i++
			for i < len(s) && isNameByte(s[i]) {
				i++
			}
			toks = append(toks, token{kind: tokVar, text: s[start:i], pos: start, end: i})
			continue
		case r >= '0' && r <= '9':
			var unit int
			i, unit = lexNumber(s, i)
			toks = append(toks, token{kind: tokNumber, text: s[start:i], pos: start, end: i, unit: unit - start})
			continue
		case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			for i < len(s) {
				r, w := utf8.DecodeRuneInString(s[i:])
				if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
					break
				}
				i += w
			}
			toks = append(toks, token{kind: tokWord, text: s[start:i], pos: start, end: i})
			continue
		}
		i += w
		for _, sym := range symbols {
			if strings.HasPrefix(s[start:], sym) {
				i = start + len(sym)
				break
			}
		}
		t := token{kind: tokSymbol, text: s[start:i], pos: start, end: i}
		t.flag = t.text == "." && (i == len(s) || isSpace(s[i]) || s[i] == ';')
		toks = append(toks, t)
	}
	return toks
}

// lexNumber reads digits, a fraction, an exponent and then letters, the number's unit, from s[i:]. A
// fraction or exponent counts only when a digit follows it, so 1. ends a clause and 1eV is in eV.
func lexNumber(s string, i int) (end, unit int) {
	digits := func(i int) int {
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		return i
	}
	isDigit := func(i int) bool { return i < len(s) && s[i] >= '0' && s[i] <= '9' }
	i = digits(i)
	if i < len(s) && s[i] == '.' && isDigit(i+1) {
		i = digits(i + 1)
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		if isDigit(j) {
			i = digits(j)
		}
	}
	unit = i
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		if !unicode.IsLetter(r) {
			break
		}
		i += w
	}
	return i, unit
}

func isNameByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// isIdentByte reports whether c may sit inside a name, a path or a variable, so a keyword beside one
// (?shaving, having_x, x.desc) is part of it rather than a keyword.
func isIdentByte(c byte) bool {
	return isNameByte(c) || c == '?' || c == '.' || c == '-'
}

// span is a run of a source's tokens, toks[lo:hi], and the text they came from, text[from:to], which
// takes in the whitespace around them up to the separators either side.
type span struct {
	src      *source
	lo, hi   int
	from, to int
}

func (src *source) all() span { return span{src, 0, len(src.toks), 0, len(src.text)} }

func (s span) toks() []token { return s.src.toks[s.lo:s.hi] }
func (s span) empty() bool   { return s.lo == s.hi }
func (s span) raw() string   { return s.src.text[s.from:s.to] }
func (s span) text() string  { return strings.TrimSpace(s.raw()) }

func (s span) first() token { return s.src.toks[s.lo] }
func (s span) last() token  { return s.src.toks[s.hi-1] }

// trim narrows the text to the tokens, as strings.TrimSpace would: an unclosed string runs to the
// end of the text, its whitespace included.
func (s span) trim() span {
	if s.empty() {
		return span{s.src, s.lo, s.hi, s.from, s.from}
	}
	from := s.first().pos
	return span{s.src, s.lo, s.hi, from, from + len(strings.TrimRightFunc(s.src.text[from:s.last().end], unicode.IsSpace))}
}

// tokens returns the span of toks[lo:hi], relative to s, with the text between their neighbours.
func (s span) tokens(lo, hi int) span {
	from, to := s.from, s.to
	if lo > 0 {
		from = s.src.toks[s.lo+lo-1].end
	}
	if s.lo+hi < s.hi {
		to = s.src.toks[s.lo+hi].pos
	}
	return span{s.src, s.lo + lo, s.lo + hi, from, to}
}

// after is the span after toks[i], and before the span before it.
func (s span) after(i int) span  { return s.tokens(i+1, s.hi-s.lo) }
func (s span) before(i int) span { return s.tokens(0, i) }

// slice is the span of text[a:b] within s: its tokens, or the text re-read when a or b falls inside
// one, as the "=" of a "!=" does when the text is cut at it (split).
func (s span) slice(a, b int) span {
	toks := s.toks()
	lo := 0
	for lo < len(toks) && toks[lo].end <= a {
		lo++
	}
	hi := lo
	for hi < len(toks) && toks[hi].pos < b {
		hi++
	}
	if lo < hi && (toks[lo].pos < a || toks[hi-1].end > b) {
		sub := &source{text: s.src.text, toks: lexRange(s.src.text, a, b)}
		return span{sub, 0, len(sub.toks), a, b}
	}
	return span{s.src, s.lo + lo, s.lo + hi, a, b}
}

// cut is the span from byte b to the span's end.
func (s span) cut(b int) span { return s.slice(b, s.to) }

// top calls f with each token outside brackets, by index into the span. Brackets count as the text
// reads them, unbalanced too: a stray ")" leaves the rest one level in.
func (s span) top(f func(i int, t token)) {
	depth := 0
	for i, t := range s.toks() {
		if isBracket(t) {
			if t.text == "(" || t.text == "{" {
				depth++
			} else {
				depth--
			}
			continue
		}
		if depth == 0 {
			f(i, t)
		}
	}
}

func isBracket(t token) bool {
	return t.kind == tokSymbol && (t.text == "(" || t.text == "{" || t.text == ")" || t.text == "}")
}

// occurrences are the byte offsets of op outside strings and brackets, read in the text of the
// symbols, so an op may start inside one: "!=>" holds "=>", and "?n:-1" a ":". Separators were
// always found in the text this way, and some programs spell them so.
func (s span) occurrences(op string) []int {
	var out []int
	toks := s.toks()
	s.top(func(i int, t token) {
		if t.kind != tokSymbol || i > 0 && toks[i-1].kind == tokSymbol && !isBracket(toks[i-1]) && toks[i-1].end == t.pos {
			return // inside a run of symbols already read
		}
		j := i
		for j+1 < len(toks) && toks[j+1].kind == tokSymbol && !isBracket(toks[j+1]) && toks[j+1].pos == toks[j].end {
			j++
		}
		run := s.src.text[t.pos:toks[j].end]
		for k := 0; k+len(op) <= len(run); {
			if strings.HasPrefix(run[k:], op) {
				out = append(out, t.pos+k)
				k += len(op)
			} else {
				k++
			}
		}
	})
	return out
}

// split cuts the span at each occurrence of sep.
func (s span) split(sep string) []span {
	var parts []span
	from := s.from
	for _, at := range s.occurrences(sep) {
		parts = append(parts, s.slice(from, at))
		from = at + len(sep)
	}
	return append(parts, s.slice(from, s.to))
}

// count is how often op occurs, the "<" of a "<=" included, so `?a <= ?b < ?c` is no single comparison.
func (s span) count(op string) int { return len(s.occurrences(op)) }

// find is the index of the first token of kind and text, at any depth, or -1.
func (s span) find(text string) int {
	for i, t := range s.toks() {
		if t.kind == tokSymbol && t.text == text {
			return i
		}
	}
	return -1
}

// is reports whether toks[i] is the symbol text.
func (s span) is(i int, text string) bool {
	return i >= 0 && i < s.hi-s.lo && s.src.toks[s.lo+i].kind == tokSymbol && s.src.toks[s.lo+i].text == text
}

// ParseError is the error Parse and ParseRules return. Its message is the one the parser has always
// written, so a host matching on its text sees no change, and it says where in the text the piece it
// is about starts, so an editor or an agent can point at it. A message about a part of a having or an
// order by (`query: having "count(?b) > ?x": ...`) wraps the part's own error, which Unwrap returns,
// and takes its position.
type ParseError struct {
	Offset int // bytes from the start of the text
	Line   int // 1 for the first line
	Column int // 1 for a line's first character, counting characters rather than bytes
	msg    string
	cause  error
}

func (e *ParseError) Error() string { return e.msg }

// Unwrap returns the error a contextual message wraps, or nil.
func (e *ParseError) Unwrap() error { return e.cause }

// at is where an error about the span points: its first token, or its start when it has none.
func (s span) at() int {
	if s.empty() {
		return s.from
	}
	return s.first().pos
}

func (s span) errorf(format string, args ...any) error {
	return s.src.errorAt(s.at(), fmt.Sprintf(format, args...), nil)
}

// wrap gives an error from outside the parser (parseArgType's) the span's position.
func (s span) wrap(err error) error {
	if _, ok := err.(*ParseError); ok || err == nil {
		return err
	}
	return s.src.errorAt(s.at(), err.Error(), nil)
}

// within says which part of a having or an order by an error is about.
func within(context string, err error) error {
	pe, ok := err.(*ParseError)
	if !ok {
		return fmt.Errorf("query: %s: %s", context, strings.TrimPrefix(err.Error(), "query: "))
	}
	return &ParseError{Offset: pe.Offset, Line: pe.Line, Column: pe.Column,
		msg: fmt.Sprintf("query: %s: %s", context, strings.TrimPrefix(pe.Error(), "query: ")), cause: pe}
}

func (src *source) errorAt(offset int, msg string, cause error) error {
	line := 1 + strings.Count(src.text[:offset], "\n")
	col := 1 + utf8.RuneCountInString(src.text[strings.LastIndexByte(src.text[:offset], '\n')+1:offset])
	return &ParseError{Offset: offset, Line: line, Column: col, msg: msg, cause: cause}
}

// number reads a number token, with a sign before it when one is given, as a constant. A unit of e
// alone reads as an exponent left unfinished, which #163 refuses (1eV is a unit), and a number too large
// for a float64 is refused as strconv refuses it.
func numberConst(sign string, t token) (*Term, bool) {
	num, unit := sign+t.text[:t.unit], t.text[t.unit:]
	if unit == "e" || unit == "E" {
		return nil, false
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return nil, false
	}
	return &Term{Const: &ns.Value{S: num, Num: &f, BaseUnit: unit}}, true
}
