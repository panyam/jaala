package cypher

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type tokKind int

const (
	tEOF    tokKind = iota
	tIdent          // a name or keyword; backticked names are tIdent with quoted set
	tString         // 'text' or "text", val holds it unescaped
	tNumber         // 12, 1.5, 1e3
	tParam          // $name, val holds the name
	tSym            // punctuation and operators, text holds it
)

type token struct {
	kind   tokKind
	text   string // as written
	val    string // a string's or parameter's value, a name's text
	quoted bool   // a `backticked` name, never a keyword
	pos    Pos
}

// is reports whether t is the keyword kw, which Cypher reads in any case.
func (t token) is(kw string) bool {
	return t.kind == tIdent && !t.quoted && strings.EqualFold(t.val, kw)
}

func (t token) sym(s string) bool { return t.kind == tSym && t.text == s }

// symbols are tried longest first.
var symbols = []string{"..", "<>", "!=", "<=", ">=", "=~", "(", ")", "[", "]", "{", "}", ":", ",", ".", "|", "*", "-", "+", "/", "%", "^", "<", ">", "=", ";"}

// lex reads text into tokens, ending with a tEOF token at the end of the text.
func lex(text string) ([]token, error) {
	var toks []token
	line, col := 1, 1
	at := func(i int) Pos { return Pos{Offset: i, Line: line, Column: col} }
	advance := func(s string) {
		for _, r := range s {
			if r == '\n' {
				line, col = line+1, 1
			} else {
				col++
			}
		}
	}
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			advance(text[i : i+1])
			i++
			continue
		case strings.HasPrefix(text[i:], "//"):
			j := strings.IndexByte(text[i:], '\n')
			if j < 0 {
				j = len(text) - i
			}
			advance(text[i : i+j])
			i += j
			continue
		case strings.HasPrefix(text[i:], "/*"):
			j := strings.Index(text[i+2:], "*/")
			if j < 0 {
				return nil, errAt(at(i), "a /* comment is never closed")
			}
			advance(text[i : i+j+4])
			i += j + 4
			continue
		}
		start := at(i)
		switch {
		case c == '\'' || c == '"':
			val, n, err := lexString(text[i:])
			if err != nil {
				return nil, errAt(start, "%s", err.Error())
			}
			toks = append(toks, token{kind: tString, text: text[i : i+n], val: val, pos: start})
			advance(text[i : i+n])
			i += n
		case c == '`':
			j := strings.IndexByte(text[i+1:], '`')
			if j < 0 {
				return nil, errAt(start, "a `name is never closed")
			}
			toks = append(toks, token{kind: tIdent, text: text[i : i+j+2], val: text[i+1 : i+1+j], quoted: true, pos: start})
			advance(text[i : i+j+2])
			i += j + 2
		case c == '$':
			n := identLen(text[i+1:])
			if n == 0 {
				return nil, errAt(start, "a $ must be followed by a parameter's name")
			}
			toks = append(toks, token{kind: tParam, text: text[i : i+1+n], val: text[i+1 : i+1+n], pos: start})
			advance(text[i : i+1+n])
			i += 1 + n
		case c >= '0' && c <= '9':
			n := numberLen(text[i:])
			toks = append(toks, token{kind: tNumber, text: text[i : i+n], val: text[i : i+n], pos: start})
			advance(text[i : i+n])
			i += n
		default:
			if n := identLen(text[i:]); n > 0 {
				toks = append(toks, token{kind: tIdent, text: text[i : i+n], val: text[i : i+n], pos: start})
				advance(text[i : i+n])
				i += n
				continue
			}
			sym := ""
			for _, s := range symbols {
				if strings.HasPrefix(text[i:], s) {
					sym = s
					break
				}
			}
			if sym == "" {
				r, _ := utf8.DecodeRuneInString(text[i:])
				return nil, errAt(start, "unexpected character %q", r)
			}
			toks = append(toks, token{kind: tSym, text: sym, pos: start})
			advance(sym)
			i += len(sym)
		}
	}
	return append(toks, token{kind: tEOF, pos: at(len(text))}), nil
}

func identLen(s string) int {
	n := 0
	for n < len(s) {
		r, size := utf8.DecodeRuneInString(s[n:])
		if r == '_' || unicode.IsLetter(r) || (n > 0 && unicode.IsDigit(r)) {
			n += size
			continue
		}
		break
	}
	return n
}

// numberLen is the length of the decimal number s starts with. A "." begins a fraction only before a
// digit, so the range in *1..3 lexes as 1, .., 3.
func numberLen(s string) int {
	digits := func(i int) int {
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		return i
	}
	i := digits(0)
	if i+1 < len(s) && s[i] == '.' && s[i+1] >= '0' && s[i+1] <= '9' {
		i = digits(i + 1)
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		if k := digits(j); k > j {
			i = k
		}
	}
	return i
}

// lexString reads the quoted string s starts with, returning its value and its length as written.
func lexString(s string) (string, int, error) {
	q := s[0]
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c == q:
			return b.String(), i + 1, nil
		case c == '\\' && i+1 < len(s):
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '\\', '\'', '"':
				b.WriteByte(s[i])
			default:
				b.WriteByte('\\')
				b.WriteByte(s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, errorString("a string is never closed")
}

type errorString string

func (e errorString) Error() string { return string(e) }
