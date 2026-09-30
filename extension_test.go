package perfsample

import (
	"testing"
)

func TestReinterpretPreservesAddressesAndWeights(t *testing.T) {
	tree := NewTree()
	tree.AddSample(Sample{
		ID:     "s1",
		Weight: 7,
		Stack: []Frame{
			{Symbol: "main"},
			{Missing: true, Addr: 0x4010},
		},
		Prov: Provenance{Source: "manual", SymbolVersion: "symbols-v1", Batch: "b1"},
	})

	retree := tree.Reinterpret(func(f Frame) []Frame {
		if !f.Missing || f.Addr != 0x4010 {
			return nil
		}
		return []Frame{
			{Module: "app", Symbol: "optimized", Addr: f.Addr, InlineDepth: 1},
			{Module: "app", Symbol: "hot", Addr: f.Addr, InlineDepth: 2},
		}
	})

	if !approxEq(retree.Root.TotalWeight, tree.Root.TotalWeight) || retree.Root.SampleCount != tree.Root.SampleCount {
		t.Fatalf("reinterpret changed totals: %v/%d want %v/%d",
			retree.Root.TotalWeight, retree.Root.SampleCount, tree.Root.TotalWeight, tree.Root.SampleCount)
	}

	outer := retree.FindID(
		FrameID{Symbol: "main"},
		FrameID{Module: "app", Symbol: "optimized", Addr: 0x4010, InlineDepth: 1},
	)
	inner := retree.FindID(
		FrameID{Symbol: "main"},
		FrameID{Module: "app", Symbol: "optimized", Addr: 0x4010, InlineDepth: 1},
		FrameID{Module: "app", Symbol: "hot", Addr: 0x4010, InlineDepth: 2},
	)
	if outer == nil || inner == nil {
		t.Fatalf("inline chain missing: outer=%v inner=%v", outer, inner)
	}
	if outer.ID.Addr != 0x4010 || inner.ID.Addr != 0x4010 {
		t.Fatalf("expanded frames lost raw address: %x %x", outer.ID.Addr, inner.ID.Addr)
	}
	if !approxEq(outer.TotalWeight, 7) || !approxEq(outer.SelfWeight, 0) {
		t.Errorf("outer inline self/total = %v/%v, want 0/7", outer.SelfWeight, outer.TotalWeight)
	}
	if !approxEq(inner.TotalWeight, 7) || !approxEq(inner.SelfWeight, 7) {
		t.Errorf("inner inline self/total = %v/%v, want 7/7", inner.SelfWeight, inner.TotalWeight)
	}

	raw := retree.RawSamples()
	if len(raw) != 1 || len(raw[0].Stack) != 2 || !raw[0].Stack[1].Missing || raw[0].Weight != 7 {
		t.Fatalf("reinterpreted raw samples not retained: %+v", raw)
	}
}

func TestSameNameInDifferentModulesKeepsIdentity(t *testing.T) {
	tree := NewTree()
	tree.AddSample(sampleWithModule("libA", "foo", 2))
	tree.AddSample(sampleWithModule("libB", "foo", 3))

	a := tree.FindID(FrameID{Symbol: "main"}, FrameID{Module: "libA", Symbol: "foo"})
	b := tree.FindID(FrameID{Symbol: "main"}, FrameID{Module: "libB", Symbol: "foo"})
	if a == nil || b == nil {
		t.Fatalf("same-named functions were merged: a=%v b=%v", a, b)
	}
	if !approxEq(a.TotalWeight, 2) || !approxEq(b.TotalWeight, 3) {
		t.Fatalf("module identity weights = %v/%v, want 2/3", a.TotalWeight, b.TotalWeight)
	}

	cmp := Compare(tree, tree)
	shared := cmp.Shared()
	if len(shared) != 3 { // main + 两个模块中的 foo 身份
		t.Fatalf("shared paths = %d, want 3: %+v", len(shared), shared)
	}
}

func sampleWithModule(module, leaf string, weight float64) Sample {
	return Sample{
		Weight: weight,
		Stack: []Frame{
			{Symbol: "main"},
			{Module: module, Symbol: leaf},
		},
		Prov: Provenance{Source: "manual"},
	}
}

func TestFunctionMoveMappingAndUnmatched(t *testing.T) {
	old := NewTree()
	old.AddSample(Sample{
		Weight: 4,
		Stack:  []Frame{{Module: "oldlib", Symbol: "work"}},
	})
	moved := NewTree()
	moved.AddSample(Sample{
		Weight: 4,
		Stack:  []Frame{{Module: "newlib", Symbol: "workV2"}},
	})

	cmp := CompareBuilds(old, moved, []FrameMapping{{
		From: FrameID{Module: "oldlib", Symbol: "work"},
		To:   FrameID{Module: "newlib", Symbol: "workV2"},
	}})
	if len(cmp.Shared()) != 1 {
		t.Fatalf("mapped path not shared: %+v", cmp.Changes)
	}
	c := cmp.Shared()[0]
	if !c.SymbolChanged || c.SampleChanged || !approxEq(c.DeltaTotal, 0) {
		t.Fatalf("moved function classified wrong: %+v", c)
	}

	withoutMapping := Compare(old, moved)
	if len(withoutMapping.Shared()) != 0 || len(withoutMapping.Unmatched()) != 2 {
		t.Fatalf("unreliable paths should be unmatched: %+v", withoutMapping.Changes)
	}
}

func TestLegacyReportStillReadable(t *testing.T) {
	legacy := []byte(`{"changes":[{"path":["main","f"],"old_total":2,"new_total":5,"delta_total":3,"in_old":true,"in_new":true}]}`)
	rep, err := UnmarshalReport(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Version != 1 || len(rep.Comparison.Changes) != 1 {
		t.Fatalf("legacy report = %+v", rep)
	}
	c := rep.Comparison.Changes[0]
	if c.Status != Matched || !c.SampleChanged || len(c.NewIDPath) != 2 {
		t.Fatalf("legacy change normalized wrong: %+v", c)
	}
}
