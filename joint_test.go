package perfsample

import (
	"reflect"
	"testing"
)

func jointHistory() *ModuleHistory {
	return NewModuleHistory(
		ModuleSpan{Module: "libv1.so", Base: 0x1000, Limit: 0x2000, Start: 0, End: 100, Generation: 1},
		ModuleSpan{Module: "libv2.so", Base: 0x1000, Limit: 0x2000, Start: 100, Generation: 2},
	)
}

func jointSample(id string, t uint64, w float64, src string) Sample {
	return Sample{
		ID:     id,
		Time:   t,
		Weight: w,
		Prov:   Provenance{Source: src},
		Stack: []Frame{
			{Symbol: "main", Addr: 0x500},
			{Symbol: "work", Addr: 0x1800},
		},
	}
}

func hotspotByName(hs []Hotspot, name string) (Hotspot, bool) {
	for _, h := range hs {
		if h.Name == name {
			return h, true
		}
	}
	return Hotspot{}, false
}

// 归属判定：确实能对应的区间才归到装载代次，先后无法确定的明确标示。
func TestJointAttribution(t *testing.T) {
	h := jointHistory()
	tr := Track{Source: "cpu", Offset: OffsetRange{Min: -10, Max: 10}, Samples: []Sample{
		jointSample("a", 50, 1, "cpu"),  // ref [40,60] 完全在 gen1 内
		jointSample("b", 95, 2, "cpu"),  // ref [85,105] 跨越装卸边界：无法确定先后
		jointSample("c", 150, 4, "cpu"), // ref [140,160] 完全在 gen2 内
	}}
	v := NewJointView("v", []Track{tr}, h)

	g1, ok := hotspotByName(v.Hotspots(), "libv1.so!work[g1]")
	if !ok || !approxEq(g1.SelfWeight, 1) {
		t.Fatalf("gen1 hotspot = %+v, ok=%v", g1, ok)
	}
	g2, ok := hotspotByName(v.Hotspots(), "libv2.so!work[g2]")
	if !ok || !approxEq(g2.SelfWeight, 4) {
		t.Fatalf("gen2 hotspot = %+v, ok=%v", g2, ok)
	}
	amb := v.Ambiguous()
	if len(amb) != 1 || !approxEq(amb[0].SelfWeight, 2) {
		t.Fatalf("ambiguous = %+v", amb)
	}
	if len(amb[0].Candidates) != 2 {
		t.Fatalf("ambiguous candidates = %+v", amb[0].Candidates)
	}
	// 归属不定的权重绝不进入任何热点，总权重仍守恒。
	if !approxEq(v.TotalWeight(), 7) {
		t.Fatalf("total = %v", v.TotalWeight())
	}
	var hotSum float64
	for _, hs := range v.Hotspots() {
		hotSum += hs.SelfWeight
	}
	if !approxEq(hotSum+v.AmbiguousWeight()+v.UnattributedWeight(), v.TotalWeight()) {
		t.Fatalf("weights not conserved: hot=%v amb=%v total=%v", hotSum, v.AmbiguousWeight(), v.TotalWeight())
	}
}

// 无法归属：任何可行偏移下地址都不在已知模块内。
func TestJointUnattributed(t *testing.T) {
	h := jointHistory()
	s := jointSample("x", 50, 3, "cpu")
	s.Stack[1].Addr = 0x9000
	v := NewJointView("v", []Track{{Source: "cpu", Offset: OffsetRange{Min: -10, Max: 10}, Samples: []Sample{s}}}, h)
	if !approxEq(v.UnattributedWeight(), 3) {
		t.Fatalf("unattributed = %v", v.UnattributedWeight())
	}
	if len(v.Hotspots()) != 0 {
		t.Fatalf("hotspots = %+v", v.Hotspots())
	}
}

// 调整某来源的对齐判断可以看到热点归属变化；撤销一次只恢复它影响的来源；
// 原始样本与先前结论仍然可用。
func TestJointSessionAlignmentUndo(t *testing.T) {
	h := jointHistory()
	tracks := []Track{
		{Source: "cpu", Offset: OffsetRange{Min: -10, Max: 10}, Samples: []Sample{jointSample("a", 95, 1, "cpu")}},
		{Source: "gpu", Offset: OffsetRange{Min: -5, Max: 5}, Samples: []Sample{jointSample("b", 97, 1, "gpu")}},
	}
	s := NewJointSession("s", tracks, h)
	if !approxEq(s.View().AmbiguousWeight(), 2) {
		t.Fatalf("initial ambiguous = %v", s.View().AmbiguousWeight())
	}

	if err := s.SetAlignment("cpu", OffsetRange{Min: 0, Max: 0}); err != nil {
		t.Fatal(err)
	}
	// 先前结论仍可用。
	if s.Previous() == nil || !approxEq(s.Previous().AmbiguousWeight(), 2) {
		t.Fatalf("previous view not kept: %+v", s.Previous())
	}
	if !approxEq(s.View().AmbiguousWeight(), 1) {
		t.Fatalf("after cpu align ambiguous = %v", s.View().AmbiguousWeight())
	}
	if _, ok := hotspotByName(s.View().Hotspots(), "libv1.so!work[g1]"); !ok {
		t.Fatalf("cpu sample should resolve to gen1: %+v", s.View().Hotspots())
	}

	if err := s.SetAlignment("gpu", OffsetRange{Min: 5, Max: 5}); err != nil {
		t.Fatal(err)
	}
	if !approxEq(s.View().AmbiguousWeight(), 0) {
		t.Fatalf("after gpu align ambiguous = %v", s.View().AmbiguousWeight())
	}

	// 撤销一次：只恢复 gpu 的归属，cpu 的对齐结论保留。
	if !s.Undo() {
		t.Fatal("undo should succeed")
	}
	if !approxEq(s.View().AmbiguousWeight(), 1) {
		t.Fatalf("after undo ambiguous = %v", s.View().AmbiguousWeight())
	}
	if _, ok := hotspotByName(s.View().Hotspots(), "libv1.so!work[g1]"); !ok {
		t.Fatalf("cpu attribution must survive gpu undo: %+v", s.View().Hotspots())
	}
	if !s.Undo() {
		t.Fatal("second undo should succeed")
	}
	if !approxEq(s.View().AmbiguousWeight(), 2) {
		t.Fatalf("after second undo ambiguous = %v", s.View().AmbiguousWeight())
	}
	if s.Undo() {
		t.Fatal("third undo should fail")
	}

	// 原始样本从未被修改。
	if s.Tracks()[0].Samples[0].Time != 95 || s.Tracks()[1].Samples[0].Time != 97 {
		t.Fatalf("raw samples modified: %+v", s.Tracks())
	}

	// 对齐判断不能超出声明的已知范围。
	if err := s.SetAlignment("cpu", OffsetRange{Min: -20, Max: 0}); err == nil {
		t.Fatal("out-of-range alignment should fail")
	}
	if err := s.SetAlignment("nope", OffsetRange{Min: 0, Max: 0}); err == nil {
		t.Fatal("unknown source should fail")
	}
}

// 比较两段运行：采样覆盖不同与真实权重变化分开，缺少观察不算零开销。
func TestCompareRunsCoverage(t *testing.T) {
	h := jointHistory()
	runA := NewJointView("A", []Track{{Source: "cpu", Offset: OffsetRange{Min: -10, Max: 10}, Samples: []Sample{
		jointSample("a1", 70, 2, "cpu"),  // gen1，B 也观察到该区间
		jointSample("a2", 500, 3, "cpu"), // gen2，B 在此处没有观察
	}}}, h)
	runB := NewJointView("B", []Track{{Source: "cpu", Offset: OffsetRange{Min: -10, Max: 10}, Samples: []Sample{
		jointSample("b1", 75, 1, "cpu"), // gen1
	}}}, h)

	rc := CompareRuns(runA, runB)
	if !rc.Balanced {
		t.Fatalf("unbalanced: %+v", rc)
	}
	var g1, g2 *HotspotDelta
	for i := range rc.Hotspots {
		switch rc.Hotspots[i].Name {
		case "libv1.so!work[g1]":
			g1 = &rc.Hotspots[i]
		case "libv2.so!work[g2]":
			g2 = &rc.Hotspots[i]
		}
	}
	if g1 == nil || !approxEq(g1.WeightA, 2) || !approxEq(g1.WeightB, 1) || !approxEq(g1.Delta, -1) {
		t.Fatalf("gen1 delta = %+v", g1)
	}
	// gen2 区间 B 缺少观察：不是 B 的零开销，而是覆盖差异。
	if g2 == nil || !approxEq(g2.OnlyA, 3) || !approxEq(g2.WeightB, 0) || !approxEq(g2.Delta, 0) {
		t.Fatalf("gen2 delta = %+v", g2)
	}
	if !approxEq(rc.CoverageOnlyA, 3) || !approxEq(rc.CoverageOnlyB, 0) {
		t.Fatalf("coverage only: A=%v B=%v", rc.CoverageOnlyA, rc.CoverageOnlyB)
	}
	if !approxEq(rc.TotalA, 5) || !approxEq(rc.TotalB, 1) {
		t.Fatalf("totals: %+v", rc)
	}
}

// 保存重开后复现所选视图。
func TestJointSessionSaveRestore(t *testing.T) {
	h := jointHistory()
	tracks := []Track{
		{Source: "cpu", Offset: OffsetRange{Min: -10, Max: 10}, Samples: []Sample{
			jointSample("a", 50, 2, "cpu"),
			jointSample("b", 95, 1, "cpu"),
		}},
	}
	s := NewJointSession("s", tracks, h)
	if err := s.SetAlignment("cpu", OffsetRange{Min: 3, Max: 7}); err != nil {
		t.Fatal(err)
	}
	data, err := s.Save()
	if err != nil {
		t.Fatal(err)
	}
	back, err := RestoreJointSession(tracks, h, data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.View().Hotspots(), back.View().Hotspots()) {
		t.Fatalf("hotspots differ: %+v vs %+v", s.View().Hotspots(), back.View().Hotspots())
	}
	if !reflect.DeepEqual(s.View().Ambiguous(), back.View().Ambiguous()) {
		t.Fatalf("ambiguous differ")
	}
	al, ok := back.Alignment("cpu")
	if !ok || al != (OffsetRange{Min: 3, Max: 7}) {
		t.Fatalf("alignment = %+v ok=%v", al, ok)
	}
	if _, err := RestoreJointSession(tracks, h, []byte("{bad")); err == nil {
		t.Fatal("bad state should fail")
	}
}

// 模块地址复用、符号更新与部分来源缺失都展示在对比报告中。
func TestCompareRunsReuseSymbolsMissing(t *testing.T) {
	h := jointHistory()
	reuse := h.AddressReuse()
	if len(reuse) != 1 || len(reuse[0].Owners) != 2 || reuse[0].Base != 0x1000 {
		t.Fatalf("reuse = %+v", reuse)
	}

	tracksA := []Track{
		{Source: "cpu", Offset: OffsetRange{Min: 0, Max: 0}, Samples: []Sample{jointSample("a", 50, 1, "cpu")}},
		{Source: "gpu", Offset: OffsetRange{Min: 0, Max: 0}, Samples: []Sample{jointSample("g", 60, 1, "gpu")}},
	}
	sa := NewJointSession("A", tracksA, h)
	sa.ApplySymbols("build-2", Reinterpretation{Resolver: ResolverFunc(func(f Frame) []FrameInfo {
		if f.Symbol == "work" {
			return []FrameInfo{{Symbol: "workV2"}}
		}
		return nil
	})})
	sb := NewJointSession("B", tracksA[:1], h)

	rc := CompareRuns(sa.View(), sb.View())
	if len(rc.MissingInB) != 1 || rc.MissingInB[0] != "gpu" {
		t.Fatalf("missing in B = %+v", rc.MissingInB)
	}
	if len(rc.SymbolUpdates) != 1 || rc.SymbolUpdates[0] != "symbols:build-2" {
		t.Fatalf("symbol updates = %+v", rc.SymbolUpdates)
	}
	if len(rc.ModuleReuse) != 1 {
		t.Fatalf("module reuse = %+v", rc.ModuleReuse)
	}
	// 符号更新后热点使用新符号名，权重守恒。
	if _, ok := hotspotByName(sa.View().Hotspots(), "libv1.so!workV2[g1]"); !ok {
		t.Fatalf("renamed hotspot missing: %+v", sa.View().Hotspots())
	}
	if !approxEq(sa.View().TotalWeight(), 2) {
		t.Fatalf("total = %v", sa.View().TotalWeight())
	}
}
