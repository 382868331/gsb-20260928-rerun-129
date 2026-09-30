package perfsample

import "testing"

func samplesWeightCount(samples []Sample) (float64, int64) {
	var w float64
	for _, s := range samples {
		w += s.weight()
	}
	return w, int64(len(samples))
}

// 缺符号的原始样本：0x4010 处的函数没有符号。
func rawMissingSamples() []Sample {
	return []Sample{
		{ID: "s1", Weight: 3, Stack: []Frame{
			{Symbol: "main"},
			{Missing: true, Addr: 0x4010},
			{Symbol: "g"},
		}, Prov: Provenance{Source: "manual", SymbolVersion: "sym-old", Batch: "b"}},
		{ID: "s2", Weight: 2, Stack: []Frame{
			{Symbol: "main"},
			{Missing: true, Addr: 0x4010},
		}, Prov: Provenance{Source: "manual", SymbolVersion: "sym-old", Batch: "b"}},
	}
}

func TestReinterpretMissingSymbolPreservesWeights(t *testing.T) {
	raw := rawMissingSamples()
	old := NewTree()
	oldW, oldN := old.AddBatch(raw)

	// 新符号资料把 0x4010 解析为 libfoo.so!decode。
	resolver := ResolverFunc(func(f Frame) []FrameInfo {
		if f.Missing && f.Addr == 0x4010 {
			return []FrameInfo{{Module: "libfoo.so", Symbol: "decode"}}
		}
		return nil
	})
	re := Reinterpret(raw, resolver)
	newT := NewTree()
	newW, newN := newT.AddBatch(re)

	// 原始地址与样本权重完全不变。
	if !approxEq(oldW, newW) || oldN != newN {
		t.Fatalf("weight/count changed: %v/%d vs %v/%d", oldW, oldN, newW, newN)
	}
	for i := range raw {
		if raw[i].Weight != re[i].Weight || raw[i].ID != re[i].ID ||
			raw[i].Prov != re[i].Prov {
			t.Fatalf("sample meta changed on reinterpret: %+v vs %+v", raw[i], re[i])
		}
	}
	// 解析出的帧必须保留原始地址。
	decode := re[0].Stack[1]
	if decode.Addr != 0x4010 || decode.Symbol != "decode" || decode.Module != "libfoo.so" {
		t.Fatalf("resolved frame lost addr/identity: %+v", decode)
	}

	// 自身权重之和仍等于总权重；占位节点消失，decode 节点出现。
	node := newT.Find("main", "libfoo.so\x01:decode")
	if node == nil {
		t.Fatal("decode node missing after symbol completion")
	}
	if !approxEq(node.TotalWeight, 5) || !approxEq(node.SelfWeight, 2) {
		t.Errorf("decode total/self = %v/%v, want 5/2", node.TotalWeight, node.SelfWeight)
	}
	if old.Find("main", "(unknown@0x4010)") == nil {
		t.Fatal("old tree should still hold the unknown placeholder")
	}
}

func TestReinterpretInlineExpansion(t *testing.T) {
	// 采样点物理函数是 work；新符号资料显示 hot 内联进了 work。
	raw := []Sample{{ID: "i1", Weight: 4, Stack: []Frame{
		{Symbol: "main"},
		{Symbol: "work", Addr: 0x500},
	}, Prov: Provenance{SymbolVersion: "sym-old"}}}
	resolver := ResolverFunc(func(f Frame) []FrameInfo {
		if f.Symbol == "work" && f.Addr == 0x500 {
			// 调用方向：外层内联 hot，物理函数 work 是采样点。
			return []FrameInfo{{Symbol: "hot", InlineLevel: 1}, {Symbol: "work", InlineLevel: 0}}
		}
		return nil
	})
	re := Reinterpret(raw, resolver)
	tree := NewTree()
	tree.AddBatch(re)

	hot := tree.Find("main", "hot\x01i1")
	work := tree.Find("main", "hot\x01i1", "work")
	if hot == nil || work == nil {
		t.Fatalf("inline expansion path missing: %v %v", hot, work)
	}
	// 内联层只拿包含权重，物理叶子拿自身权重。
	if !approxEq(hot.TotalWeight, 4) || !approxEq(hot.SelfWeight, 0) {
		t.Errorf("inline hot total/self = %v/%v, want 4/0", hot.TotalWeight, hot.SelfWeight)
	}
	if !approxEq(work.TotalWeight, 4) || !approxEq(work.SelfWeight, 4) {
		t.Errorf("physical work total/self = %v/%v, want 4/4", work.TotalWeight, work.SelfWeight)
	}
	if !approxEq(hot.AnchorWeight("i1"), 4) || !approxEq(work.AnchorWeight("i1"), 4) {
		t.Errorf("anchor weight not preserved across inline layers")
	}
	if approxEq(hot.AnchorWeight("i1"), 0) {
		t.Fatal("anchor missing")
	}
	if len(hot.LeafAnchors()) != 0 || len(work.LeafAnchors()) != 1 {
		t.Errorf("leaf anchors = %v / %v, want none / i1", hot.LeafAnchors(), work.LeafAnchors())
	}
}

func TestSameNameDifferentModulesAreDistinct(t *testing.T) {
	tree := NewTree()
	tree.AddBatch([]Sample{
		{ID: "a", Weight: 3, Stack: []Frame{
			{Symbol: "parse", Module: "libA.so"},
		}, Prov: Provenance{SymbolVersion: "v1"}},
		{ID: "b", Weight: 5, Stack: []Frame{
			{Symbol: "parse", Module: "libB.so"},
		}, Prov: Provenance{SymbolVersion: "v1"}},
	})
	a := tree.Find("libA.so\x01:parse")
	b := tree.Find("libB.so\x01:parse")
	if a == nil || b == nil {
		t.Fatal("same-name functions in different modules were folded")
	}
	if !approxEq(a.TotalWeight, 3) || !approxEq(b.TotalWeight, 5) {
		t.Errorf("module identity weights = %v/%v, want 3/5", a.TotalWeight, b.TotalWeight)
	}
}

func TestCompareClassifiesSymbolMoveVsSampleChange(t *testing.T) {
	// 旧构建：oldmod!oldname 是叶子；新构建它移动到 newmod!newname。
	old := NewTree()
	old.AddBatch([]Sample{{ID: "x", Weight: 4, Stack: []Frame{
		{Symbol: "main"}, {Symbol: "oldname", Module: "oldmod"},
	}, Prov: Provenance{SymbolVersion: "build-1"}}})

	build := func(weight float64) *Tree {
		tree := NewTree()
		tree.AddBatch([]Sample{{ID: "x", Weight: weight, Stack: []Frame{
			{Symbol: "main"}, {Symbol: "newname", Module: "newmod"},
		}, Prov: Provenance{SymbolVersion: "build-2"}}})
		return tree
	}
	mapping := NewMapping(Move{
		OldModule: "oldmod", OldSymbol: "oldname",
		NewModule: "newmod", NewSymbol: "newname",
	})

	// 权重不变：差异纯粹来自符号（移动），Delta 必须为 0。
	same := CompareMapped(old, build(4), mapping)
	var moved *PathChange
	for i := range same.Changes {
		c := &same.Changes[i]
		if c.SymbolMove {
			moved = c
		}
	}
	if moved == nil {
		t.Fatal("moved path not matched through mapping")
	}
	if moved.Kind != KindSymbol || !approxEq(moved.DeltaTotal, 0) ||
		moved.FullName() != "main/newmod!newname" {
		t.Errorf("want pure symbol change with zero delta, got %+v", moved)
	}
	if len(same.Unmatched()) != 0 {
		t.Errorf("unexpected unmatched paths: %d", len(same.Unmatched()))
	}

	// 权重变化：同一路径对应，但这是真实的样本变化。
	diff := CompareMapped(old, build(7), mapping)
	var changed *PathChange
	for i := range diff.Changes {
		if diff.Changes[i].SymbolMove {
			changed = &diff.Changes[i]
		}
	}
	if changed == nil || changed.Kind != KindSample || !approxEq(changed.DeltaTotal, 3) {
		t.Errorf("want sample change delta +3, got %+v", changed)
	}
}

func TestCompareWithoutMappingIsBackwardCompatible(t *testing.T) {
	old := NewTree()
	old.AddBatch([]Sample{{ID: "x", Weight: 4, Stack: []Frame{
		{Symbol: "main"}, {Symbol: "f"},
	}, Prov: Provenance{SymbolVersion: "v1"}}})
	newT := NewTree()
	newT.AddBatch([]Sample{{ID: "x", Weight: 6, Stack: []Frame{
		{Symbol: "main"}, {Symbol: "f"},
	}, Prov: Provenance{SymbolVersion: "v2"}}})

	cmp := Compare(old, newT)
	var f *PathChange
	for i := range cmp.Shared() {
		if cmp.Shared()[i].FullName() == "main/f" {
			f = &cmp.Shared()[i]
		}
	}
	if f == nil || !approxEq(f.DeltaTotal, 2) || f.Kind != KindSample {
		t.Fatalf("old-style compare broken: %+v", f)
	}
}

func TestUnmappedMissingPathIsUnmatched(t *testing.T) {
	// 旧树叶子是缺符号占位；新树该地址解析成具体函数，但没有提供映射，
	// 也没有共同样本锚点——必须标为未匹配，不能凭空对齐或搬运权重。
	old := NewTree()
	old.AddBatch([]Sample{{ID: "m", Weight: 4, Stack: []Frame{
		{Symbol: "main"}, {Missing: true, Addr: 0x999},
	}, Prov: Provenance{SymbolVersion: "v1"}}})
	newT := NewTree()
	newT.AddBatch([]Sample{{ID: "z", Weight: 9, Stack: []Frame{
		{Symbol: "main"}, {Symbol: "resolved", Module: "lib.so"},
	}, Prov: Provenance{SymbolVersion: "v2"}}})

	cmp := CompareMapped(old, newT, NewMapping())
	for _, c := range cmp.Shared() {
		if c.FullName() == "main/(unknown@0x999)" || c.FullName() == "main/lib.so\x01:resolved" {
			t.Fatalf("unknown/resolved path must not be matched: %+v", c)
		}
	}
	um := cmp.Unmatched()
	// 旧占位路径与新解析路径都应出现为未匹配，且 Delta 不产生虚假数字。
	var oldPlace, newResolved bool
	for _, c := range um {
		if c.InOld && c.FullName() == "main/(unknown@0x999)" && !approxEq(c.DeltaTotal, 0) {
			t.Errorf("unmatched old path leaked delta %v", c.DeltaTotal)
		}
		if c.InOld && c.FullName() == "main/(unknown@0x999)" {
			oldPlace = true
		}
		if c.InNew && c.FullName() == "main/lib.so!resolved" {
			newResolved = true
		}
	}
	if !oldPlace || !newResolved {
		t.Fatalf("both sides should be unmatched, got %+v", um)
	}
}

func TestAnchorFallbackRecoversMoveWithoutPathMapping(t *testing.T) {
	// 即使忘了给 Mapping，共同的样本 ID 也能把移动后的叶子可靠对应回来。
	old := NewTree()
	old.AddBatch([]Sample{{ID: "keep", Weight: 5, Stack: []Frame{
		{Symbol: "main"}, {Symbol: "alpha"},
	}}})
	newT := NewTree()
	newT.AddBatch([]Sample{{ID: "keep", Weight: 5, Stack: []Frame{
		{Symbol: "main"}, {Symbol: "beta"},
	}}})
	cmp := CompareMapped(old, newT, nil)
	var matched *PathChange
	for i := range cmp.Changes {
		if cmp.Changes[i].Matched {
			matched = &cmp.Changes[i]
		}
	}
	if matched == nil {
		t.Fatal("anchor fallback failed to match renamed leaf")
	}
	if matched.FullName() != "main/beta" || matched.Kind != KindSymbol {
		t.Errorf("want symbol change onto main/beta, got %+v", matched)
	}
}

func TestReinterpretBatchWeightConservation(t *testing.T) {
	raw := rawMissingSamples()
	resolver := ResolverFunc(func(f Frame) []FrameInfo {
		if f.Missing {
			return []FrameInfo{{Module: "m", Symbol: "s"}}
		}
		return nil
	})
	re := Reinterpret(raw, resolver)
	w1, n1 := samplesWeightCount(raw)
	w2, n2 := samplesWeightCount(re)
	if !approxEq(w1, w2) || n1 != n2 {
		t.Fatalf("reinterpret changed weight: %v/%d vs %v/%d", w1, n1, w2, n2)
	}
}
