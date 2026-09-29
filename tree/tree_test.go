package tree

import (
	"testing"

	"perfcore/model"
)

func f(k string) model.Frame { return model.Frame{Key: k, Symbol: k, Resolved: true} }

func un(k string) model.Frame {
	return model.Frame{Key: k, Symbol: "?" + k, Resolved: false}
}

// leaf-first helpers: reverse outer-first lists.
func st(frames ...model.Frame) []model.Frame {
	out := make([]model.Frame, len(frames))
	for i, fr := range frames {
		out[len(frames)-1-i] = fr
	}
	return out
}

// Hand-computed small example:
//
//	sample A weight 1: main -> a -> leafX        (outer-first)
//	sample B weight 3: main -> a -> leafX
//	sample C weight 2: main -> a -> b
//
// Expected self: leafX=4, b=2, others=0.
// Expected inclusive: main=6, a=6, leafX=4, b=2.
func TestBuildHandComputed(t *testing.T) {
	b := model.Batch{
		ID: "b", Collector: "unit", ProgramVersion: "pv", SymbolVersion: "sv",
		Samples: []model.Sample{
			{ID: "A", Weight: 1, Stack: st(f("main"), f("a"), f("leafX"))},
			{ID: "B", Weight: 3, Stack: st(f("main"), f("a"), f("leafX"))},
			{ID: "C", Weight: 2, Stack: st(f("main"), f("a"), f("b"))},
		},
	}
	p, err := Build("t", b)
	if err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		path               Path
		wantSelf, wantIncl int64
	}{
		{Path{"main"}, 0, 6},
		{Path{"main", "a"}, 0, 6},
		{Path{"main", "a", "leafX"}, 4, 4},
		{Path{"main", "a", "b"}, 2, 2},
	}
	for _, c := range checks {
		n := p.Find(c.path)
		if n == nil {
			t.Fatalf("missing node %s", c.path)
		}
		if n.SelfWeight != c.wantSelf || n.InclusiveWeight != c.wantIncl {
			t.Errorf("%s: got self=%d incl=%d, want self=%d incl=%d",
				c.path, n.SelfWeight, n.InclusiveWeight, c.wantSelf, c.wantIncl)
		}
	}
	if p.TotalWeight != 6 {
		t.Errorf("total weight = %d, want 6", p.TotalWeight)
	}

	// Traceability: leafX carries exactly two contributions A=1 and B=3.
	leaf := p.Find(Path{"main", "a", "leafX"})
	got := map[string]int64{}
	for _, c := range leaf.Contributions {
		got[c.SampleID] += c.Weight
	}
	if got["A"] != 1 || got["B"] != 3 || len(got) != 2 {
		t.Errorf("contributions = %v, want A=1,B=3", got)
	}
}

// Recursion main -> fib -> fib -> fib must produce three distinct nodes,
// not be folded into one call.
func TestRecursionNotFolded(t *testing.T) {
	b := model.Batch{
		ID: "b", Collector: "unit",
		Samples: []model.Sample{
			{ID: "r1", Weight: 2, Stack: st(f("main"), f("fib"), f("fib"), f("fib"))},
			{ID: "r2", Weight: 1, Stack: st(f("main"), f("fib"))},
		},
	}
	p, err := Build("t", b)
	if err != nil {
		t.Fatal(err)
	}

	fib1 := p.Find(Path{"main", "fib"})
	fib2 := p.Find(Path{"main", "fib", "fib"})
	fib3 := p.Find(Path{"main", "fib", "fib", "fib"})
	if fib1 == nil || fib2 == nil || fib3 == nil {
		t.Fatal("recursive occurrences were folded together")
	}
	if fib1 == fib2 || fib2 == fib3 {
		t.Fatal("recursive occurrences share one node")
	}
	// Only the innermost occurrence gets self weight.
	if fib1.SelfWeight != 1 || fib2.SelfWeight != 0 || fib3.SelfWeight != 2 {
		t.Errorf("self weights fib1=%d fib2=%d fib3=%d, want 1/0/2",
			fib1.SelfWeight, fib2.SelfWeight, fib3.SelfWeight)
	}
	// Inclusive counts every sample passing through.
	if fib1.InclusiveWeight != 3 || fib2.InclusiveWeight != 2 || fib3.InclusiveWeight != 2 {
		t.Errorf("incl weights fib1=%d fib2=%d fib3=%d, want 3/2/2",
			fib1.InclusiveWeight, fib2.InclusiveWeight, fib3.InclusiveWeight)
	}

	// Recurrence is reported along the traversal path.
	rec := map[string]int{}
	p.Walk(func(e WalkEntry) {
		if e.Node == fib3 {
			rec["fib3"] = e.Recurrence
		}
	})
	if rec["fib3"] != 3 {
		t.Errorf("fib3 recurrence = %d, want 3", rec["fib3"])
	}

	// Flat aggregation sums occurrences: fib self = 3 across two leaf occurrences.
	flat := p.FlatByFrame()["fib"]
	if flat.SelfWeight != 3 || flat.Occurrences != 3 {
		t.Errorf("flat fib self=%d occurrences=%d, want 3/3", flat.SelfWeight, flat.Occurrences)
	}
}

// An unresolved leaf frame must be retained and counted, not dropped.
func TestUnresolvedFrameKept(t *testing.T) {
	b := model.Batch{
		ID: "b", Collector: "unit",
		Samples: []model.Sample{
			{ID: "u", Weight: 5, Stack: st(f("main"), f("a"), un("pc=0x9"))},
		},
	}
	p, err := Build("t", b)
	if err != nil {
		t.Fatal(err)
	}
	n := p.Find(Path{"main", "a", "pc=0x9"})
	if n == nil {
		t.Fatal("unresolved sample dropped")
	}
	if n.Frame.Resolved || n.SelfWeight != 5 {
		t.Errorf("unresolved node = %+v, want self 5", n)
	}
	if p.UnresolvedSelf != 5 || p.TotalWeight != 5 {
		t.Errorf("unresolved=%d total=%d, want 5/5", p.UnresolvedSelf, p.TotalWeight)
	}
}

// Merging batches must reconcile total weight across batches, tree and self sum.
func TestBatchMergeReconciliation(t *testing.T) {
	b1 := model.Batch{ID: "b1", Collector: "unit", ProgramVersion: "pv", SymbolVersion: "sv1",
		Samples: []model.Sample{{ID: "x", Weight: 4, Stack: st(f("main"), f("a"))}}}
	b2 := model.Batch{ID: "b2", Collector: "unit", ProgramVersion: "pv", SymbolVersion: "sv2",
		Samples: []model.Sample{{ID: "y", Weight: 6, Stack: st(f("main"), f("a"))}}}
	p, err := Build("t", b1, b2)
	if err != nil {
		t.Fatal(err)
	}
	if p.TotalWeight != 10 || p.SampleCount != 2 || len(p.Sources) != 2 {
		t.Errorf("total=%d count=%d sources=%d, want 10/2/2",
			p.TotalWeight, p.SampleCount, len(p.Sources))
	}
	n := p.Find(Path{"main", "a"})
	if n.SelfWeight != 10 || n.InclusiveWeight != 10 {
		t.Errorf("a self=%d incl=%d, want 10/10", n.SelfWeight, n.InclusiveWeight)
	}
	// Symbol version provenance is carried on the frame from its batch.
	if p.Sources[0].SymbolVersion != "sv1" || p.Sources[1].SymbolVersion != "sv2" {
		t.Error("symbol version provenance lost")
	}
}

func TestInvalidSamples(t *testing.T) {
	cases := []struct {
		name string
		b    model.Batch
	}{
		{"empty stack", model.Batch{ID: "b", Samples: []model.Sample{{ID: "s", Weight: 1}}}},
		{"zero weight", model.Batch{ID: "b", Samples: []model.Sample{{ID: "s", Weight: 0, Stack: st(f("main"))}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Build("t", c.b); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
