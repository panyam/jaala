package ns

import (
	"context"
	"fmt"
	"time"
)

// StreamCheckTimeout is how long CheckStreamReentrant waits for its reads when ctx has no deadline.
const StreamCheckTimeout = 10 * time.Second

// CheckStreamReentrant reports whether src keeps StreamSource's promise for rel: that nothing a later
// read needs is held while TuplesSeq yields (#539). The engine solves a goal's first literal as its
// tuples arrive and runs the rest of the body for each one, so a Lookup, a LookupBatch or another
// stream can come between two of a stream's tuples. A store that keeps its one database connection
// in an open cursor across its yields waits on itself there forever (declaire#370).
//
// It streams rel and, at the first tuple, reads again before asking for the next: rel streamed anew
// up to its first tuple, then, when src looks rel up, Lookup and LookupBatch with the first tuple's
// first value bound. It then stops the outer stream. A src that isn't a StreamSource, or a rel with no
// tuples, passes, since nothing is held across a yield. An error from any read is returned wrapped.
//
// The reads run on a goroutine under ctx, or StreamCheckTimeout when ctx has no deadline. A read
// still blocked then fails the check, and its goroutine stays blocked unless src gives up when its
// context ends, so a store's test calls this once, not in a loop.
func CheckStreamReentrant(ctx context.Context, src Source, rel string) error {
	ss, ok := src.(StreamSource)
	if !ok {
		return nil
	}
	wait := StreamCheckTimeout
	if d, ok := ctx.Deadline(); ok {
		wait = time.Until(d)
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	done := make(chan error, 1)
	step := make(chan string, 8)
	go func() { done <- reenter(ctx, src, ss, rel, step) }()
	last := "streaming " + rel
	for {
		select {
		case err := <-done:
			return err
		case s := <-step:
			last = s
		case <-ctx.Done():
			return fmt.Errorf("ns: %s: %s did not finish in %v: the source holds what a read needs while TuplesSeq yields (#539)", rel, last, wait.Round(time.Millisecond))
		}
	}
}

// reenter is CheckStreamReentrant's reads, reporting each before it starts on step.
func reenter(ctx context.Context, src Source, ss StreamSource, rel string, step chan<- string) error {
	for t, err := range ss.TuplesSeq(ctx, rel) {
		if err != nil {
			return fmt.Errorf("ns: %s: streaming: %w", rel, err)
		}
		step <- "a second stream of " + rel + " between its tuples"
		for _, err := range ss.TuplesSeq(ctx, rel) {
			if err != nil {
				return fmt.Errorf("ns: %s: a second stream: %w", rel, err)
			}
			break
		}
		if len(t.Vals) == 0 || !looksUp(src, rel) {
			return nil
		}
		bound := map[int]Value{0: t.Vals[0]}
		step <- "a Lookup of " + rel + " between its tuples"
		if _, err := src.(LookupSource).Lookup(ctx, rel, bound); err != nil {
			return fmt.Errorf("ns: %s: a Lookup between its tuples: %w", rel, err)
		}
		if bs, ok := src.(BatchLookupSource); ok {
			step <- "a LookupBatch of " + rel + " between its tuples"
			if _, err := bs.LookupBatch(ctx, rel, []int{0}, [][]Value{{t.Vals[0]}}); err != nil {
				return fmt.Errorf("ns: %s: a LookupBatch between its tuples: %w", rel, err)
			}
		}
		return nil
	}
	return nil
}

// looksUp says src answers Lookup for rel, as the engine decides.
func looksUp(src Source, rel string) bool {
	if _, ok := src.(LookupSource); !ok {
		return false
	}
	if lr, ok := src.(LookupRelations); ok {
		return lr.LooksUp(rel)
	}
	return true
}
