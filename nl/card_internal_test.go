package nl

import (
	"sync"
	"testing"

	"github.com/panyam/jaala/ns"
)

// A host can ask for the card on every prompt: the vocabulary keeps it until a registration changes
// what the card would say.
func TestTheCardIsBuiltOncePerVocabulary(t *testing.T) {
	src := ns.NewMemSource().Declare("edge", "from", "to")
	v, err := ns.NewVocabulary(src)
	if err != nil {
		t.Fatal(err)
	}
	before := cardBuilds.Load()
	first, err := Card(v)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if again, err := Card(v); err != nil || again != first {
				t.Errorf("a repeated card differs (%v)", err)
			}
		}()
	}
	wg.Wait()
	if n := cardBuilds.Load() - before; n != 1 {
		t.Fatalf("17 calls built %d cards, want 1", n)
	}
	if err := v.AddRelation("node", ns.Schema{Arity: 1, Labels: []string{"name"}}); err != nil {
		t.Fatal(err)
	}
	changed, err := Card(v)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first || cardBuilds.Load()-before != 2 {
		t.Fatalf("a registration didn't give a new card:\n%s", changed)
	}
}
