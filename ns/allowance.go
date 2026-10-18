package ns

import "context"

// An Allowance is what is left of an evaluation's budget for a Source's or generator's own work
// (#361): the steps of a walk, the rows a store scans to answer one lookup, a remote call. The engine
// charges a generator per row it emits and a Source per tuple it returns, which comes after the work
// that found them; a host whose one call does much more than it returns charges that work here as it
// goes, and stops once the budget is spent.
//
// The engine puts one on the context it hands a Source's read, lookup, push or walk and a
// generator's Gen. What a host spends counts against the evaluation's budget beside the engine's own
// work, and is reported apart from it (datalog's Report.Spent), so the engine's work counts don't
// move when a host starts spending.
type Allowance interface {
	// Spend charges n units of the host's own work. A non-nil error means the budget is spent: the
	// host stops and returns it from the call unchanged, and the evaluation stops with it. Spend is
	// called on the goroutine the engine called the host on, as a generator's emit is.
	Spend(n int64) error
	// Left is how many units the budget has left, and false when the evaluation has no budget.
	Left() (int64, bool)
}

type allowanceKey struct{}

// WithAllowance is ctx carrying a, as the engine hands a host its evaluation's context.
func WithAllowance(ctx context.Context, a Allowance) context.Context {
	return context.WithValue(ctx, allowanceKey{}, a)
}

// AllowanceFrom is the Allowance ctx carries, and false when it carries none, as when a host calls
// its own Source outside an evaluation.
func AllowanceFrom(ctx context.Context) (Allowance, bool) {
	a, ok := ctx.Value(allowanceKey{}).(Allowance)
	return a, ok
}
