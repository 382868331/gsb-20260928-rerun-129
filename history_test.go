package perfsample

import "testing"

// 模块在采样期间被卸载并重新加载到同一地址：
//   - gen1: [0x1000,0x2000) 在 t=[0,100) 是 libv1.so 的 oldfn；
//   - 卸载后 gen2: 同地址区间在 t=[100,200) 是 libv2.so 的 newfn。
//
// 同一个 0x1010 在不同时刻属于不同代码，重解释后必须是两个独立节点。
func reloadHistory() *ModuleHistory {
	return NewModuleHistory(
		ModuleSpan{Module: "libv1.so", Base: 0x1000, Limit: 0x2000, Start: 0, End: 100, Generation: 1},
		ModuleSpan{Module: "libv2.so", Base: 0x1000, Limit: 0x2000, Start: 100, End: 0, Generation: 2},
	)
}

func reloadSamples() []Sample {
	return []Sample{
		{ID: "a", Weight: 3, Time: 50, Stack: []Frame{
			{Symbol: "main"},
			{Missing: true, Addr: 0x1010},
		}},
		{ID: "b", Weight: 4, Time: 150, Stack: []Frame{
			{Symbol: "main"},
			{Missing: true, Addr: 0x1010},
		}},
	}
}

func TestTimedReinterpretDistinguishesReloadedModule(t *testing.T) {
	hist := reloadHistory()
	resolver := TimedResolverFunc(func(f Frame, ts uint64, span ModuleSpan) []FrameInfo {
		switch span.Module {
		case "libv1.so":
			return []FrameInfo{{Symbol: "oldfn"}}
		case "libv2.so":
			return []FrameInfo{{Symbol: "newfn"}}
		}
		return nil
	})
	re := ReinterpretTimed(reloadSamples(), hist, resolver)
	tree := NewTree()
	w, n := tree.AddBatch(re)
	if !approxEq(w, 7) || n != 2 {
		t.Fatalf("weight/count changed: %v/%d", w, n)
	}
	oldNode := tree.Find("main", "libv1.so\x01:oldfn\x01g1")
	newNode := tree.Find("main", "libv2.so\x01:newfn\x01g2")
	if oldNode == nil || newNode == nil {
		t.Fatalf("two module generations were folded: old=%v new=%v", oldNode, newNode)
	}
	if !approxEq(oldNode.SelfWeight, 3) || !approxEq(newNode.SelfWeight, 4) {
		t.Errorf("generation self weights = %v/%v, want 3/4",
			oldNode.SelfWeight, newNode.SelfWeight)
	}

	// 原始地址与时刻必须保留。
	for i, src := range reloadSamples() {
		leaf := re[i].Stack[len(re[i].Stack)-1]
		if leaf.Addr != 0x1010 || re[i].Time != src.Time {
			t.Errorf("addr/time not preserved: %+v time=%d (want %d)", leaf, re[i].Time, src.Time)
		}
	}
}

func TestTimedLookupRejectsOverlapAndOutside(t *testing.T) {
	hist := reloadHistory()
	if _, ok := hist.Lookup(0x1010, 99); !ok {
		t.Error("t=99 should belong to gen1")
	}
	if s, ok := hist.Lookup(0x1010, 150); !ok || s.Module != "libv2.so" || s.Generation != 2 {
		t.Errorf("t=150 should belong to gen2, got %+v ok=%v", s, ok)
	}
	if _, ok := hist.Lookup(0x9999, 10); ok {
		t.Error("address outside any span must not be attributed")
	}
	// 无时刻样本：同一地址在历史上落入两个不同代次，归属不唯一，必须拒绝。
	if _, ok := hist.Lookup(0x1010, 0); ok {
		t.Error("timeless sample at an address reused across generations must be unresolved")
	}
}

// 不做时间感知（旧行为）时，同地址不同时刻的缺符号样本会被错误折叠成一个
// 占位节点；时间感知重解释必须避免这种混淆。
func TestNaiveAddressMergeContrastsWithTimed(t *testing.T) {
	naive := NewTree()
	naive.AddBatch(reloadSamples())
	merged := naive.Find("main", "(unknown@0x1010)")
	if merged == nil || !approxEq(merged.SelfWeight, 7) {
		t.Fatalf("naive view expected single merged placeholder with weight 7, got %+v", merged)
	}

	hist := reloadHistory()
	re := ReinterpretTimed(reloadSamples(), hist, nil)
	timed := NewTree()
	timed.AddBatch(re)
	count := 0
	for _, c := range timed.Root.Children["main"].Children {
		count++
		_ = c
	}
	if count != 2 {
		t.Fatalf("timed view must keep two generation nodes, got %d", count)
	}
}

func TestSessionUndoRestoresPreviousViewOnce(t *testing.T) {
	hist := reloadHistory()
	resolver := TimedResolverFunc(func(f Frame, ts uint64, span ModuleSpan) []FrameInfo {
		switch span.Module {
		case "libv1.so":
			return []FrameInfo{{Symbol: "oldfn"}}
		case "libv2.so":
			return []FrameInfo{{Symbol: "newfn"}}
		}
		return nil
	})
	sess := NewSession(reloadSamples(), "raw")
	if sess.CanUndo() {
		t.Fatal("fresh session should not be undoable")
	}
	sess.Apply(Reinterpretation{Label: "symbols-v2", History: hist, Timed: resolver})
	if !sess.CanUndo() {
		t.Fatal("apply should create an undo point")
	}
	if sess.Current().Tree.Find("main", "libv2.so\x01:newfn\x01g2") == nil {
		t.Fatal("applied view missing resolved gen2 node")
	}
	if !sess.Undo() {
		t.Fatal("undo should succeed once")
	}
	// 恢复旧视角：占位节点回来，已解析节点消失。
	if sess.Current().Tree.Find("main", "(unknown@0x1010)") == nil {
		t.Fatal("undo did not restore the raw placeholder view")
	}
	if sess.Current().Tree.Find("main", "libv2.so\x01:newfn\x01g2") != nil {
		t.Fatal("undo left resolved node in restored view")
	}
	if sess.Undo() {
		t.Fatal("undo must only be allowed once")
	}
}
