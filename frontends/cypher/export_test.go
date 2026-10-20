package cypher

// Marked says whether p's text was marked with origins, which a query holding most control characters
// leaves no bytes free for (chooseMarks), so Locate can place none of its check errors.
func Marked(p Program) bool { return p.spans != nil }
