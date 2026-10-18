package datalog

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/panyam/jaala/ns"
)

// A CheckError is what Validate and Eval return for a program that can't run as written: a name the
// vocabulary doesn't hold, a call at the wrong arity, a variable nothing binds, a cycle through
// negation (#321). Its message is the one the engine has always written, so a host matching on
// fragments sees no change; the fields say the same thing as data, so a host can count failures by
// Kind and a model can be told how to fix one.
//
// A Query carries no positions (a host may build one in Go, and two parses of one text compare
// equal), so the error names what it is about instead: Locate finds that in the program's text.
type CheckError struct {
	Kind ErrorKind
	// Fix says how to change the program when the engine can tell: the vocabulary's suggestion for a
	// name, the expected signature for an arity, what to bind for an unbound variable. "" when it
	// can't, as for a cycle through negation.
	Fix string
	// Rule is the head relation of the rule the error is in, "" for the goal or when the check
	// doesn't know.
	Rule string
	// Relation is the call the error is about, and Args its argument count as written, or -1.
	Relation string
	Args     int
	// Var is the variable the error is about, without "?".
	Var string
	msg string
}

func (e *CheckError) Error() string { return e.msg }

// An ErrorKind sorts CheckErrors by what a program got wrong.
type ErrorKind string

// The kinds of CheckError.
const (
	KindUnknown    ErrorKind = "unknown"    // a relation, module or aggregate the vocabulary doesn't hold
	KindArity      ErrorKind = "arity"      // a call with the wrong number of arguments
	KindUnbound    ErrorKind = "unbound"    // a variable that has to be bound where nothing binds it
	KindType       ErrorKind = "type"       // a value or unit an argument or comparison can't take
	KindStratify   ErrorKind = "stratify"   // recursion through negation, an aggregate or arithmetic
	KindDefinition ErrorKind = "definition" // a rule head that can't define its relation
	KindAggregate  ErrorKind = "aggregate"  // an aggregate written where or how it can't reduce
	KindAnswer     ErrorKind = "answer"     // order by, having, limit, offset or after
	KindFact       ErrorKind = "fact"       // an `as ?r` on an atom that matches no stored fact (#279)
)

// checkErr builds a CheckError with no call or variable to point at.
func checkErr(kind ErrorKind, msg string) *CheckError {
	return &CheckError{Kind: kind, Args: -1, msg: msg}
}

func (e *CheckError) call(rel string, args int) *CheckError {
	e.Relation, e.Args = shownName(rel), args
	return e
}

func (e *CheckError) inRule(rel string) *CheckError {
	e.Rule = shownName(rel)
	return e
}

func (e *CheckError) about(v Var) *CheckError {
	if !strings.ContainsRune(string(v), 0) { // a rewrite's own variable names nothing the author wrote
		e.Var = string(v)
	}
	return e
}

func (e *CheckError) fix(s string) *CheckError {
	e.Fix = s
	return e
}

// unknownFix is the hint after "; " in the vocabulary's description of a name, as a Fix: `did you
// mean "edge"?`, or a module's members.
func unknownFix(desc string) string {
	if _, hint, ok := strings.Cut(desc, "; "); ok {
		return hint
	}
	return ""
}

// A Position is a place in a program's text, as ParseError gives one.
type Position struct {
	Offset int // bytes from the start of the text
	Line   int // 1 for the first line
	Column int // 1 for a line's first character, counting characters rather than bytes
}

// Locate says where in text, the program err came from, the error points: a ParseError's own
// position, or for a CheckError the first call to its Relation with its Args (in its Rule's clauses
// when it names one), else the first ?Var there. It reports false when the text holds no such
// place, as for an error in a module's rules rather than the program's.
func Locate(text string, err error) (Position, bool) {
	var pe *ParseError
	if errors.As(err, &pe) {
		return Position{pe.Offset, pe.Line, pe.Column}, true
	}
	var ce *CheckError
	if !errors.As(err, &ce) {
		return Position{}, false
	}
	src := lex(text)
	clauses := src.clauseSpans()
	if ce.Rule != "" {
		var in [][2]int
		for _, c := range clauses {
			if src.headName(c) == ce.Rule {
				in = append(in, c)
			}
		}
		if len(in) > 0 {
			clauses = in
		}
	}
	at := -1
	if ce.Relation != "" {
		at = src.findCall(clauses, ce.Relation, ce.Args)
	}
	if at < 0 && ce.Var != "" {
		at = src.findVar(clauses, ce.Var)
	}
	if at < 0 && ce.Kind == KindDefinition && ce.Rule != "" && len(clauses) > 0 && src.headName(clauses[0]) == ce.Rule {
		at = src.toks[clauses[0][0]].pos // a head that can't define its relation: the head itself
	}
	if at < 0 {
		return Position{}, false
	}
	return src.position(at), true
}

func (src *source) position(offset int) Position {
	line := 1 + strings.Count(src.text[:offset], "\n")
	col := 1 + utf8.RuneCountInString(src.text[strings.LastIndexByte(src.text[:offset], '\n')+1:offset])
	return Position{offset, line, col}
}

// clauseSpans cuts the tokens into clauses at a top-level ";" or a clause-ending ".", as token index
// ranges.
func (src *source) clauseSpans() [][2]int {
	var out [][2]int
	depth, start := 0, 0
	for i, t := range src.toks {
		if t.kind != tokSymbol {
			continue
		}
		switch t.text {
		case "(", "{":
			depth++
		case ")", "}":
			depth--
		case ";", ".":
			if depth == 0 && (t.text == ";" || t.flag) {
				out = append(out, [2]int{start, i})
				start = i + 1
			}
		}
	}
	return append(out, [2]int{start, len(src.toks)})
}

// nameAt reads a dotted name starting at token i (net.pins), returning it and the index after it.
func (src *source) nameAt(i, hi int) (string, int) {
	if i >= hi || src.toks[i].kind != tokWord {
		return "", i
	}
	name, j := src.toks[i].text, i+1
	for j+1 < hi && src.toks[j].kind == tokSymbol && src.toks[j].text == "." && src.toks[j+1].kind == tokWord {
		name += "." + src.toks[j+1].text
		j += 2
	}
	return name, j
}

// headName is a rule clause's head relation, or "" for a clause with no ":-".
func (src *source) headName(c [2]int) string {
	for i := c[0]; i < c[1]; i++ {
		if t := src.toks[i]; t.kind == tokSymbol && t.text == ":-" {
			name, _ := src.nameAt(c[0], c[1])
			return name
		}
	}
	return ""
}

// findCall returns the offset of the first call to rel among the clauses with args arguments, or
// with any number when args is -1 or none has that many.
func (src *source) findCall(clauses [][2]int, rel string, args int) int {
	first := -1
	for _, c := range clauses {
		for i := c[0]; i < c[1]; i++ {
			if i > c[0] && src.toks[i-1].kind == tokSymbol && src.toks[i-1].text == "." {
				continue // the tail of a dotted name
			}
			name, j := src.nameAt(i, c[1])
			if name != rel || j >= c[1] || src.toks[j].text != "(" {
				continue
			}
			if args < 0 || src.argCount(j, c[1]) == args {
				return src.toks[i].pos
			}
			if first < 0 {
				first = src.toks[i].pos
			}
		}
	}
	return first
}

// argCount counts the arguments of the parenthesis opening at token open.
func (src *source) argCount(open, hi int) int {
	depth, n, empty := 0, 1, true
	for i := open; i < hi; i++ {
		t := src.toks[i]
		if t.kind == tokSymbol {
			switch t.text {
			case "(", "{":
				depth++
				if depth == 1 {
					continue
				}
			case ")", "}":
				depth--
				if depth == 0 {
					if empty {
						return 0
					}
					return n
				}
			case ",":
				if depth == 1 {
					n++
				}
			}
		}
		empty = false
	}
	return n
}

// findVar returns the offset of the first ?v among the clauses.
func (src *source) findVar(clauses [][2]int, v string) int {
	for _, c := range clauses {
		for i := c[0]; i < c[1]; i++ {
			if t := src.toks[i]; t.kind == tokVar && t.text == "?"+v {
				return t.pos
			}
		}
	}
	return -1
}

// signatureFix is how rel is called, as a Fix for a call at the wrong arity: `edge(src: node, kind,
// dst: node)`, an optional trailing argument in brackets. "" when rel is unknown.
func (b *Base) signatureFix(rel string) string {
	var labels []string
	var types []ns.ArgType
	n, required := 0, -1
	if bi, ok := b.reg.Predicate(rel); ok {
		labels, types, n = bi.Labels, bi.Types, max(bi.Arity, bi.MaxArity)
		if bi.MaxArity > 0 {
			required = bi.Arity
		}
	} else if s, ok := b.schemaOf(rel); ok {
		labels, types, n = s.Labels, s.Types, s.Arity
	} else if s, ok := b.derivedSchema(rel); ok {
		labels, types, n = s.Labels, s.Types, s.Arity
	} else if ar, ok := b.idbArity[rel]; ok {
		n = ar
	} else {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(shownName(rel) + "(")
	for i := 0; i < n; i++ {
		a := ns.ArgSig{Name: fmt.Sprintf("arg%d", i)}
		if i < len(labels) && labels[i] != "" {
			a.Name = labels[i]
		}
		if i < len(types) {
			a.ArgType = types[i]
		}
		switch {
		case i == required:
			sb.WriteString("[, ")
		case i > 0:
			sb.WriteString(", ")
		}
		sb.WriteString(a.String())
	}
	if required >= 0 {
		sb.WriteString("]")
	}
	return sb.String() + ")"
}

// arityErr is a CheckError for atom called at the wrong arity.
func (b *Base) arityErr(msg string, atom *Atom) *CheckError {
	return checkErr(KindArity, msg).call(atom.Relation, len(atom.Args)).fix(b.signatureFix(atom.Relation))
}

// unknownErr is a CheckError for atom naming a relation the vocabulary doesn't hold: format's last
// verb is the vocabulary's description of the name, after args.
func (b *Base) unknownErr(atom *Atom, format string, args ...any) *CheckError {
	desc := b.reg.Unknown(atom.Relation)
	fix := unknownFix(desc)
	if module, leaf := splitLeaf(atom.Relation); module != "" {
		if ok, known := b.arityAccepts(leaf, len(atom.Args)); ok && known {
			fix = fmt.Sprintf("%s has no module; call %s", leaf, b.signatureFix(leaf))
		}
	}
	if fix == "" {
		fix = b.relatedFix(atom)
	}
	return checkErr(KindUnknown, fmt.Sprintf(format, append(args, desc)...)).call(atom.Relation, len(atom.Args)).fix(fix)
}

// unboundOperand is the CheckError for a comparison reaching an operand nothing bound, naming the
// first variable bnd doesn't hold (nil: the first variable).
func unboundOperand(c Compare, bnd *binding) *CheckError {
	e := checkErr(KindUnbound, "query: comparison operand is unbound (a variable must appear in a relation before it is compared)")
	for _, t := range []Term{c.Left, c.Right} {
		if t.Var == "" {
			continue
		}
		if bnd != nil {
			if _, ok := bnd.get(t.Var); ok {
				continue
			}
		}
		if e.about(t.Var); e.Var != "" {
			e.fix(fmt.Sprintf("add a relation that binds ?%s to the same body", e.Var))
		}
		break
	}
	return e
}

// unknownAggregateErr is the CheckError for an aggregate function jaala doesn't have.
func unknownAggregateErr(f string) *CheckError {
	return checkErr(KindUnknown, fmt.Sprintf("query: unknown aggregate %q (want count/min/max/sum/list)", f)).fix("use count, min, max, sum or list")
}

// localFix points an unknown call's Fix at the program's own rule it likely means (#341), since the
// vocabulary's suggestion can't see the program's rules: a qualified call whose last segment is one
// (str.mine after a qualified head was renamed mine, but not its calls), or, when the vocabulary
// suggested no name, a misspelling of one at its arity. Any other error is returned as it is.
func localFix(err error, rules []Rule) error {
	var ce *CheckError
	if !errors.As(err, &ce) || ce.Kind != KindUnknown || ce.Relation == "" {
		return err
	}
	arity := map[string]int{}
	var names []string
	for _, r := range rules {
		if h := r.Head.Relation; !strings.ContainsAny(h, ".\x00") {
			if _, ok := arity[h]; !ok {
				names = append(names, h)
			}
			arity[h] = len(r.Head.Args)
		}
	}
	if _, leaf := splitLeaf(ce.Relation); leaf != ce.Relation {
		if n, ok := arity[leaf]; ok && (ce.Args < 0 || n == ce.Args) {
			ce.Fix = fmt.Sprintf("%s is the program's own rule; call it %s, without a module", leaf, leaf)
		}
		return err
	}
	if strings.Contains(ce.Fix, "did you mean") {
		return err
	}
	var near []string
	for _, n := range names {
		if ce.Args < 0 || arity[n] == ce.Args {
			near = append(near, n)
		}
	}
	if s := ns.DidYouMeanValue(near, ce.Relation); s != "" {
		for _, n := range near {
			if s == fmt.Sprintf(", did you mean %q?", n) {
				ce.Fix = fmt.Sprintf("did you mean %s, the program's own rule?", n)
			}
		}
	}
	return err
}

// plural is n and noun, with an s unless n is 1.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// splitLeaf splits a qualified name into its module and its last segment ("" and name when bare).
func splitLeaf(name string) (module, leaf string) {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[:i], name[i+1:]
	}
	return "", name
}

// relatedFix is the Fix for a name nothing plausibly misspells (#336): the members taking as many
// arguments, written as calls, those sharing a word with the name first, or failing those, that
// nothing by that name exists, so a model stops inventing names.
func (b *Base) relatedFix(atom *Atom) string {
	rel := shownName(atom.Relation)
	related := b.reg.Related(rel, len(atom.Args))
	if len(related) == 0 {
		return fmt.Sprintf("nothing is named %s, and no member takes %s; use one the vocabulary lists", rel, plural(len(atom.Args), "argument"))
	}
	calls := make([]string, len(related))
	for i, r := range related {
		calls[i] = b.signatureFix(r)
	}
	return fmt.Sprintf("nothing is named %s; members taking %s include %s", rel, plural(len(atom.Args), "argument"), strings.Join(calls, ", "))
}
