package perfsample

import "testing"

// 一个可以直接手工统计的小样例。
//
//	S1 weight 3: main -> f -> g
//	S2 weight 1: main -> f           (采样点在 f)
//	S3 weight 2: main -> f -> f -> g (f 递归调用自身)
func recursionSamples() []Sample {
	return []Sample{
		{ID: "s1", Weight: 3, Stack: []Frame{
			{Symbol: "main"}, {Symbol: "f"}, {Symbol: "g"},
		}, Prov: Provenance{Source: "manual", SymbolVersion: "v1", Batch: "a"}},
		{ID: "s2", Weight: 1, Stack: []Frame{
			{Symbol: "main"}, {Symbol: "f"},
		}, Prov: Provenance{Source: "manual", SymbolVersion: "v1", Batch: "a"}},
		{ID: "s3", Weight: 2, Stack: []Frame{
			{Symbol: "main"}, {Symbol: "f"}, {Symbol: "f"}, {Symbol: "g"},
		}, Prov: Provenance{Source: "manual", SymbolVersion: "v1", Batch: "b"}},
	}
}

func TestRecursionNotFolded(t *testing.T) {
	tree := NewTree()
	tree.AddBatch(recursionSamples())

	mainNode := tree.Find("main")
	fOuter := tree.Find("main", "f")
	gOuter := tree.Find("main", "f", "g")
	fInner := tree.Find("main", "f", "f")
	gInner := tree.Find("main", "f", "f", "g")

	if mainNode == nil || fOuter == nil || gOuter == nil || fInner == nil || gInner == nil {
		t.Fatalf("recursion path missing: %v %v %v %v %v", mainNode, fOuter, gOuter, fInner, gInner)
	}

	// 外层 f 包含全部 3 条样本（3+1+2=6），自身只有 S2 的 1。
	if !approxEq(fOuter.TotalWeight, 6) {
		t.Errorf("outer f total = %v, want 6", fOuter.TotalWeight)
	}
	if !approxEq(fOuter.SelfWeight, 1) {
		t.Errorf("outer f self = %v, want 1", fOuter.SelfWeight)
	}
	// 递归内层 f 是独立节点：包含权重 2，自身权重 0，没有被折叠进外层。
	if !approxEq(fInner.TotalWeight, 2) {
		t.Errorf("inner f total = %v, want 2", fInner.TotalWeight)
	}
	if !approxEq(fInner.SelfWeight, 0) {
		t.Errorf("inner f self = %v, want 0", fInner.SelfWeight)
	}
	if !approxEq(gOuter.TotalWeight, 3) || !approxEq(gOuter.SelfWeight, 3) {
		t.Errorf("outer g total/self = %v/%v, want 3/3", gOuter.TotalWeight, gOuter.SelfWeight)
	}
	if !approxEq(gInner.TotalWeight, 2) || !approxEq(gInner.SelfWeight, 2) {
		t.Errorf("inner g total/self = %v/%v, want 2/2", gInner.TotalWeight, gInner.SelfWeight)
	}
}

func TestSelfWeightsSumToTotal(t *testing.T) {
	tree := NewTree()
	tree.AddBatch(recursionSamples())

	var leafSelf float64
	var walk func(n *Node)
	walk = func(n *Node) {
		leafSelf += n.SelfWeight
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(tree.Root) // 根 self 为 0

	if !approxEq(leafSelf, tree.Root.TotalWeight) {
		t.Errorf("sum of self weights %v != root total %v", leafSelf, tree.Root.TotalWeight)
	}
}

func TestMissingSymbolRetained(t *testing.T) {
	tree := NewTree()
	w, n := tree.AddBatch([]Sample{{
		ID:     "m1",
		Weight: 4,
		Stack: []Frame{
			{Symbol: "main"},
			{Missing: true, Addr: 0xABC},
			{Symbol: "g"},
		},
		Prov: Provenance{Source: "manual", SymbolVersion: "v1"},
	}})

	if !approxEq(w, 4) || n != 1 {
		t.Fatalf("batch weight/count = %v/%v, want 4/1", w, n)
	}
	unknown := tree.Find("main", "(unknown@0xabc)")
	if unknown == nil {
		t.Fatal("sample with missing symbol was dropped")
	}
	if !unknown.HasMissing {
		t.Fatal("HasMissing not set on unknown node")
	}
	if !approxEq(unknown.TotalWeight, 4) {
		t.Errorf("unknown total = %v, want 4", unknown.TotalWeight)
	}
	g := tree.Find("main", "(unknown@0xabc)", "g")
	if g == nil || !approxEq(g.TotalWeight, 4) {
		t.Fatalf("child g after missing frame lost: %+v", g)
	}
}

func TestBatchMergeWeightBalance(t *testing.T) {
	tree := NewTree()
	batchA := recursionSamples() // 权重 3+1+2 = 6
	batchB := []Sample{{
		ID: "b1", Weight: 5, Stack: []Frame{{Symbol: "main"}, {Symbol: "h"}},
		Prov: Provenance{Source: "new-collector", SymbolVersion: "v2", Batch: "c"},
	}} // 权重 5，来自一个后续新增的采集器

	wA, nA := tree.AddBatch(batchA)
	wB, nB := tree.AddBatch(batchB)

	rep := Reconcile(tree, wA+wB, nA+nB)
	if !rep.Balanced {
		t.Fatalf("weight not balanced: %+v", rep)
	}
	if !approxEq(rep.TreeTotal, 11) || rep.TreeCount != 4 {
		t.Fatalf("merged total/count = %v/%v, want 11/4", rep.TreeTotal, rep.TreeCount)
	}

	// 新增采集器的来源与符号版本仍可在节点上追溯。
	h := tree.Find("main", "h")
	breakdown := h.Provenance()
	if len(breakdown) != 1 || breakdown[0].Source != "new-collector" ||
		breakdown[0].SymbolVersion != "v2" || !approxEq(breakdown[0].TotalWeight, 5) {
		t.Fatalf("unexpected provenance: %+v", breakdown)
	}
}

func TestCompareTwoRuns(t *testing.T) {
	mk := func(gw, hw float64, ver string) *Tree {
		tree := NewTree()
		samples := []Sample{{
			ID: "g", Weight: gw,
			Stack: []Frame{{Symbol: "main"}, {Symbol: "f"}, {Symbol: "g"}},
			Prov:  Provenance{Source: "manual", SymbolVersion: ver},
		}}
		if hw > 0 {
			samples = append(samples, Sample{
				ID: "h", Weight: hw,
				Stack: []Frame{{Symbol: "main"}, {Symbol: "f"}, {Symbol: "h"}},
				Prov:  Provenance{Source: "manual", SymbolVersion: ver},
			})
		}
		tree.AddBatch(samples)
		return tree
	}

	cmp := Compare(mk(5, 0, "v1"), mk(8, 2, "v2"))

	shared := cmp.Shared()
	var gChange *PathChange
	for i := range shared {
		if shared[i].FullName() == "main/f/g" {
			gChange = &shared[i]
		}
	}
	if gChange == nil {
		t.Fatal("shared path main/f/g not found")
	}
	if !approxEq(gChange.OldTotal, 5) || !approxEq(gChange.NewTotal, 8) ||
		!approxEq(gChange.DeltaTotal, 3) {
		t.Errorf("g change = %+v, want old 5 new 8 delta +3", gChange)
	}

	// main/f/h 是第二版新增路径，不出现在 Shared 中。
	var foundNew bool
	for _, c := range cmp.Changes {
		if c.FullName() == "main/f/h" && !c.InOld && c.InNew && approxEq(c.NewTotal, 2) {
			foundNew = true
		}
	}
	if !foundNew {
		t.Fatal("new version path main/f/h not reported correctly")
	}
}

func TestDefaultWeightIsOne(t *testing.T) {
	tree := NewTree()
	tree.AddSample(Sample{Stack: []Frame{{Symbol: "main"}}})
	if !approxEq(tree.Root.TotalWeight, 1) {
		t.Fatalf("default weight = %v, want 1", tree.Root.TotalWeight)
	}
}
