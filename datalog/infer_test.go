package datalog

import (
	"fmt"
	"testing"
	"time"
)

// A chain of one-rule relations each passing three columns on is typed once a relation, not once per
// column per path back down the chain (3^n: FuzzCompile's sixteen chained WITH * stages ran for
// minutes). Twenty would take hours that way.
func TestAChainOfRelationsIsTypedOncePerRelation(t *testing.T) {
	text := "r0(?a, ?b, ?c) :- edge(?a, ?b), edge(?b, ?c);"
	for i := 1; i <= 20; i++ {
		text += fmt.Sprintf(" r%d(?a, ?b, ?c) :- r%d(?a, ?b, ?c);", i, i-1)
	}
	q := mustParse(t, text+" r20(?a, ?b, ?c)")
	done := make(chan error, 1)
	go func() { done <- Validate(q, std(graph())) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("typing a chain of 21 relations took more than 10s")
	}
}
