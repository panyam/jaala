package ns

import "testing"

// A timed federation's version keeps its members' names and Holds, and is partitioned when the
// federation is, so it pushes and routes at a version as it does as a plain Source (#312).
func TestATimedFederationsVersionIsTheSameKindOfFederation(t *testing.T) {
	timed := NewTimedMemSource(Since(0))
	timed.Declare("edge", "from", "to")
	holds := HoldsPrefix("a", map[string][]int{"edge": {0}})
	for _, build := range []func(...Member) (*Federation, error){NewFederation, NewPartitionedFederation} {
		f, err := build(Member{Name: "rev", Source: timed, Holds: holds}, Member{Name: "lib", Source: NewMemSource().Declare("label", "name", "text")})
		if err != nil {
			t.Fatal(err)
		}
		tf, err := f.Timed()
		if err != nil {
			t.Fatal(err)
		}
		src, err := tf.SourceAt(0)
		if err != nil {
			t.Fatal(err)
		}
		at := src.(*Federation)
		if at.partitioned != f.partitioned || at.members[0].Name != "rev" || at.members[0].Holds == nil || at.members[1].Source != f.members[1].Source {
			t.Errorf("partitioned %v: version 0 is partitioned %v with members %+v", f.partitioned, at.partitioned, at.members)
		}
		if _, ok := at.members[0].Source.(versionView); !ok {
			t.Errorf("a TimedSource member without SourceAt is %T at a version, want its facts at it", at.members[0].Source)
		}
	}
}
