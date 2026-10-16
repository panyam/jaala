package nl

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/panyam/jaala/datalog"
)

// Explain writes err, from parsing, validating or evaluating the program text, as a model reads it
// to fix the program: the message, then the line it points at with a caret under the place, then the
// fix when the engine knows one. An error Locate can't place in text keeps just its message and fix.
func Explain(text string, err error) string {
	if err == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(err.Error())
	if pos, ok := datalog.Locate(text, err); ok {
		start := strings.LastIndexByte(text[:pos.Offset], '\n') + 1
		end := strings.IndexByte(text[pos.Offset:], '\n')
		if end < 0 {
			end = len(text)
		} else {
			end += pos.Offset
		}
		line := text[start:end]
		fmt.Fprintf(&b, "\nline %d, column %d:\n  %s\n  %s^", pos.Line, pos.Column, line,
			strings.Repeat(" ", utf8.RuneCountInString(text[start:pos.Offset])))
	}
	var ce *datalog.CheckError
	if errors.As(err, &ce) && ce.Fix != "" {
		b.WriteString("\nfix: " + ce.Fix)
	}
	return b.String()
}
