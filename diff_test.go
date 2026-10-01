package perfsample

import "testing"

// 两份符号资料对同一批样本给出不同解释：
//   - 资料A：0x4010 是 libA.so!decode，权重 3；0x500 是 work，权重 2；
//   - 资料B：decode 改名搬到 libB.so!decompress（同样本、同权重），
//     work 权重变为 5（真实采样变化），另外 0x600 在两版都缺符号，归属存疑。
func diffSamples() []Sample {
	return []Sample{
		{ID: "s1", Weight: 3, Stack: []Frame{{Symbol: "main"}, {Missing: true, Addr: 0x4010}}},
		{ID: "s2", Weight: 2, Stack: []Frame{{Symbol: "main"}, {Missing: true, Addr: 0x500}}},
		{ID: "s3", Weight: 7, Stack: []Frame{{Symbol: "main"}, {Missing: true, Addr: 0x600}}},
	}
}

func treeOf(samples []Sample, resolver SymbolResolver) *Tree {
	var ss []Sample
	if resolver != nil {
		ss = Reinterpret(samples, resolver)
	} else {
		ss = samples
	}
	t := NewTree()
	t.AddBatch(ss)
	return t
}

func TestDiffProfilesClassifiesSymbolSampleUncertain(t *testing.T) {
	old := treeOf(diffSamples(), ResolverFunc(func(f Frame) []FrameInfo {
		switch f.Addr {
		case 0x4010:
			return []FrameInfo{{Module: "libA.so", Symbol: "decode"}}
		case 0x500:
			return []FrameInfo{{Module: "libA.so", Symbol: "work"}}
		}
		return nil // 0x600 仍缺符号
	}))
	// 资料B：s2 的权重在这里无法模拟（树只消费给定样本），因此用另一批
	// 同 ID、不同权重的样本来表达“真实采样变化”的两棵对比树。
	newSamples := []Sample{
		{ID: "s1", Weight: 3, Stack: []Frame{{Symbol: "main"}, {Missing: true, Addr: 0x4010}}},
		{ID: "s2", Weight: 5, Stack: []Frame{{Symbol: "main"}, {Missing: true, Addr: 0x500}}},
		{ID: "s3", Weight: 7, Stack: []Frame{{Symbol: "main"}, {Missing: true, Addr: 0x600}}},
	}
	newT := treeOf(newSamples, ResolverFunc(func(f Frame) []FrameInfo {
		switch f.Addr {
		case 0x4010:
			return []FrameInfo{{Module: "libB.so", Symbol: "decompress"}}
		case 0x500:
			return []FrameInfo{{Module: "libA.so", Symbol: "work"}}
		}
		return nil
	}))

	mapping := NewMapping(Move{
		OldModule: "libA.so", OldSymbol: "decode",
		NewModule: "libB.so", NewSymbol: "decompress",
	})
	d := DiffProfiles(old, newT, mapping)

	if !d.Balanced {
		t.Fatalf("weight not conserved across classification: oldTotal=%v sum(sym=%v samp=%v unc=%v) newTotal=%v",
			d.OldTotal, d.OldSymbolWeight, d.OldSampleWeight, d.OldUncertainWeight,
			d.NewTotal)
	}
	if !approxEq(d.OldTotal, 12) || !approxEq(d.NewTotal, 15) {
		t.Errorf("totals = %v/%v, want 12/15", d.OldTotal, d.NewTotal)
	}
	// s1=3 纯名称/模块修订；s2=2 vs 5 采样变化；s3=7 两版都缺符号，归属存疑。
	if !approxEq(d.OldSymbolWeight, 3) || !approxEq(d.NewSymbolWeight, 3) {
		t.Errorf("symbol bucket = %v/%v, want 3/3", d.OldSymbolWeight, d.NewSymbolWeight)
	}
	if !approxEq(d.OldSampleWeight, 2) || !approxEq(d.NewSampleWeight, 5) {
		t.Errorf("sample bucket = %v/%v, want 2/5", d.OldSampleWeight, d.NewSampleWeight)
	}
	if !approxEq(d.OldUncertainWeight, 7) || !approxEq(d.NewUncertainWeight, 7) {
		t.Errorf("uncertain bucket = %v/%v, want 7/7 (must not be dropped)",
			d.OldUncertainWeight, d.NewUncertainWeight)
	}

	var foundSymbol, foundUncertain bool
	for _, c := range d.Changes {
		if c.Kind == KindSymbol && c.FullName() == "main/libB.so!decompress" {
			foundSymbol = true
		}
		if c.Kind == KindUncertain {
			foundUncertain = true
			if c.DeltaSelf != 0 {
				t.Errorf("uncertain change must report zero delta, got %v", c.DeltaSelf)
			}
		}
	}
	if !foundSymbol {
		t.Error("rename/relabel not classified as symbol change")
	}
	if !foundUncertain {
		t.Error("unresolved 0x600 samples must be reported as uncertain")
	}
}

// 内联关系改变与模块重定位同时发生时，重新解释前后的总权重必须守恒。
func TestInlineChangeAndRelocationConservesWeight(t *testing.T) {
	raw := []Sample{
		{ID: "x", Weight: 4, Stack: []Frame{
			{Symbol: "main"},
			{Symbol: "work", Module: "oldlib.so", Addr: 0x700},
		}},
	}
	old := NewTree()
	oldW, oldN := old.AddBatch(raw)

	// 新资料：work 移动到 newlib.so，并且物理函数 work 内联了 helper（层 1）。
	re := Reinterpret(raw, ResolverFunc(func(f Frame) []FrameInfo {
		if f.Symbol == "work" {
			return []FrameInfo{
				{Module: "newlib.so", Symbol: "helper", InlineLevel: 1},
				{Module: "newlib.so", Symbol: "work"},
			}
		}
		return nil
	}))
	newT := NewTree()
	newW, newN := newT.AddBatch(re)
	if !approxEq(oldW, newW) || oldN != newN {
		t.Fatalf("weight/count changed after inline+relocation: %v/%d vs %v/%d",
			oldW, oldN, newW, newN)
	}
	helper := newT.Find("main", "newlib.so\x01:helper\x01i1")
	work := newT.Find("main", "newlib.so\x01:helper\x01i1", "newlib.so\x01:work")
	if helper == nil || work == nil {
		t.Fatal("inlined+relocated path missing")
	}
	if !approxEq(helper.TotalWeight, 4) || !approxEq(work.SelfWeight, 4) {
		t.Errorf("weights not conserved through inline/relocation: helper=%v work=%v",
			helper.TotalWeight, work.SelfWeight)
	}
	if !approxEq(helper.AnchorWeight("x"), 4) || !approxEq(work.AnchorWeight("x"), 4) {
		t.Errorf("anchor weight lost across inline+relocation")
	}

	// 通过代次感知的 Move，把旧物理路径对应到新内联路径，差异应判为纯符号变化。
	mapping := NewMapping(Move{
		OldModule: "oldlib.so", OldSymbol: "work",
		NewModule: "newlib.so", NewSymbol: "work",
	})
	cmp := CompareMapped(old, newT, mapping)
	var leafMatched bool
	for _, c := range cmp.Shared() {
		if c.FullName() == "main/newlib.so!helper[i1]/newlib.so!work" {
			leafMatched = true
			if c.Kind != KindSymbol || !approxEq(c.OldTotal, 4) || !approxEq(c.NewTotal, 4) {
				t.Errorf("relocated leaf should match as symbol with conserved weight, got %+v", c)
			}
		}
	}
	if !leafMatched {
		t.Fatal("relocated physical leaf not matched through mapping + sample anchor")
	}

	// 根总权重严格守恒：内联展开+重定位没有新增或丢失任何采样权重。
	if !approxEq(old.Root.TotalWeight, newT.Root.TotalWeight) {
		t.Errorf("root total not conserved: %v vs %v", old.Root.TotalWeight, newT.Root.TotalWeight)
	}
}
