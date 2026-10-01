package perfsample

import "testing"

func reloadHistory() *ModuleHistory {
	return NewModuleHistory(
		// libplug.so 第一次装载：[0,100)，基址 0x1000。
		ModuleSpan{Module: "libplug.so", BuildID: "b1", Base: 0x1000, End: 0x2000, LoadAt: 0, UnloadAt: 100},
		// 卸载后在同一地址范围重新装载，但是不同构建。
		ModuleSpan{Module: "libplug.so", BuildID: "b2", Base: 0x1000, End: 0x2000, LoadAt: 100, UnloadAt: 0},
	)
}

func reloadProfiles() (*SymbolProfile, *SymbolProfile) {
	p := NewSymbolProfile(
		ModuleSymbols{
			Module:  "libplug.so",
			BuildID: "b1",
			Funcs:   []FuncSym{{Name: "alpha", Start: 0x100, End: 0x120}},
		},
		ModuleSymbols{
			Module:  "libplug.so",
			BuildID: "b2",
			Funcs:   []FuncSym{{Name: "beta", Start: 0x100, End: 0x120}},
		},
	)
	return p, p
}

func TestTimedReinterpretDistinguishesReloadedSameAddress(t *testing.T) {
	history := reloadHistory()
	profile, _ := reloadProfiles()
	raw := []Sample{
		{ID: "before", Weight: 3, Time: 50, Stack: []Frame{
			{Symbol: "main"},
			{Missing: true, Addr: 0x1110},
		}},
		{ID: "after", Weight: 5, Time: 150, Stack: []Frame{
			{Symbol: "main"},
			{Missing: true, Addr: 0x1110},
		}},
	}

	re := Reinterpret(raw, NewTimedResolver(history, profile))
	tree := NewTree()
	w, n := tree.AddBatch(re)
	if !approxEq(w, 8) || n != 2 {
		t.Fatalf("weight/count changed: %.1f/%d", w, n)
	}

	alpha := tree.Find("main", "libplug.so"+buildSep+"b1"+moduleSep+"alpha")
	beta := tree.Find("main", "libplug.so"+buildSep+"b2"+moduleSep+"beta")
	if alpha == nil || beta == nil {
		t.Fatalf("same address in two reloads was merged: alpha=%v beta=%v", alpha, beta)
	}
	if !approxEq(alpha.TotalWeight, 3) || !approxEq(beta.TotalWeight, 5) {
		t.Errorf("reload weights alpha/beta = %.1f/%.1f, want 3/5", alpha.TotalWeight, beta.TotalWeight)
	}
	for _, s := range re {
		leaf := s.Stack[len(s.Stack)-1]
		if leaf.Addr != 0x1110 || leaf.Uncertain {
			t.Fatalf("raw addr/time ownership not preserved: %+v", leaf)
		}
	}
}

func TestTimedReinterpretMarksUnownedAddressUncertain(t *testing.T) {
	// b1 在 t=100 已卸载，b2 在 t=200 才装载；t=150 的地址归属不能猜。
	history := NewModuleHistory(
		ModuleSpan{Module: "m", BuildID: "b1", Base: 0x100, End: 0x200, UnloadAt: 100},
		ModuleSpan{Module: "m", BuildID: "b2", Base: 0x100, End: 0x200, LoadAt: 200},
	)
	profile := NewSymbolProfile()
	raw := []Sample{{ID: "gap", Weight: 2, Time: 150, Stack: []Frame{
		{Missing: true, Addr: 0x110},
	}}}
	re := Reinterpret(raw, NewTimedResolver(history, profile))
	leaf := re[0].Stack[0]
	if !leaf.Missing || !leaf.Uncertain || leaf.Addr != 0x110 || leaf.Symbol != "" {
		t.Fatalf("unowned address should stay addr and be uncertain: %+v", leaf)
	}
	tree := NewTree()
	tree.AddBatch(re)
	if !approxEq(tree.Root.TotalWeight, 2) {
		t.Fatal("uncertain samples must still contribute weight")
	}
	if tree.Find("(unknown@0x110)"+uncertainSuffix+timeSep+"150") == nil {
		t.Fatal("uncertain placeholder missing")
	}
}

func TestTimedReinterpretMarksAmbiguousOwnersUncertain(t *testing.T) {
	history := NewModuleHistory(
		ModuleSpan{Module: "a.so", BuildID: "1", Base: 0x100, End: 0x200},
		ModuleSpan{Module: "b.so", BuildID: "2", Base: 0x100, End: 0x200},
	)
	resolver := NewTimedResolver(history, NewSymbolProfile())
	got := resolver.ResolveFrame(ResolveContext{Time: 10}, Frame{Missing: true, Addr: 0x110})
	if len(got) != 1 || !got[0].Missing || !got[0].Uncertain {
		t.Fatalf("ambiguous owner must remain uncertain: %+v", got)
	}
}

func TestSymbolDataDiffClassifiesRenameWeightUncertain(t *testing.T) {
	old := []Sample{
		{ID: "rename", Weight: 4, Stack: []Frame{{Module: "m", BuildID: "b", Symbol: "oldName"}}},
		{ID: "weight", Weight: 3, Stack: []Frame{{Module: "m", BuildID: "b", Symbol: "same"}}},
		{ID: "gone", Weight: 2, Stack: []Frame{{Module: "m", BuildID: "b", Symbol: "gone"}}},
	}
	newer := []Sample{
		// 同模块、同构建：只是名称修订；原始地址相同。
		{ID: "rename", Weight: 4, Stack: []Frame{{Module: "m", BuildID: "b", Symbol: "newName", Addr: 0x10}}},
		{ID: "weight", Weight: 9, Stack: []Frame{{Module: "m", BuildID: "b", Symbol: "same"}}},
		{ID: "arrived", Weight: 1, Stack: []Frame{{Module: "m", BuildID: "b", Symbol: "arrived"}}},
	}
	old[0].Stack[0].Addr = 0x10

	diff := CompareInterpretations(old, newer)
	kinds := map[DiffKind]DiffEntry{}
	for _, e := range diff.Entries {
		kinds[e.Kind] = e
	}
	if _, ok := kinds[DiffRename]; !ok {
		t.Fatalf("rename missing: %+v", diff.Entries)
	}
	w := kinds[DiffWeight]
	if !approxEq(w.NewWeight-w.OldWeight, 6) {
		t.Errorf("weight delta = %.1f, want +6", w.NewWeight-w.OldWeight)
	}
	if _, ok := kinds[DiffOnlyOld]; !ok {
		t.Fatalf("unmatched old sample dropped: %+v", diff.Summarize())
	}
	if _, ok := kinds[DiffOnlyNew]; !ok {
		t.Fatalf("unmatched new sample dropped: %+v", diff.Summarize())
	}
	// 未匹配样本也计入两侧合计，因此可直接审计真实占比，不能只挑匹配项。
	if !approxEq(diff.OldTotal, 9) || !approxEq(diff.NewTotal, 14) {
		t.Errorf("totals dropped unmatched samples: %.1f/%.1f", diff.OldTotal, diff.NewTotal)
	}
}

func TestSymbolCompletionAtSameAddressIsRename(t *testing.T) {
	old := []Sample{{ID: "s", Weight: 5, Time: 42, Stack: []Frame{
		{Missing: true, Addr: 0x88},
	}}}
	newer := []Sample{{ID: "s", Weight: 5, Time: 42, Stack: []Frame{
		{Module: "m", BuildID: "b", Symbol: "resolved", Addr: 0x88},
	}}}
	diff := CompareInterpretations(old, newer)
	if len(diff.Entries) != 1 || diff.Entries[0].Kind != DiffRename {
		t.Fatalf("symbol completion should be rename: %+v", diff.Entries)
	}
	if !diff.Conserved {
		t.Fatal("same-batch reinterpretation must conserve total weight")
	}
}

func TestRelocationAndInlineChangeConservesTotalWeight(t *testing.T) {
	history := NewModuleHistory(
		ModuleSpan{Module: "libx.so", BuildID: "old", Base: 0x1000, End: 0x2000, UnloadAt: 100},
		ModuleSpan{Module: "libx.so", BuildID: "new", Base: 0x3000, End: 0x4000, LoadAt: 100},
	)
	oldProfile := NewSymbolProfile(ModuleSymbols{
		Module: "libx.so", BuildID: "old",
		Funcs: []FuncSym{{Name: "work", Start: 0x20, End: 0x40}},
	})
	newProfile := NewSymbolProfile(ModuleSymbols{
		Module: "libx.so", BuildID: "new",
		Funcs: []FuncSym{{Name: "workV2", Start: 0x20, End: 0x40}},
		InlineAt: map[uint64][]InlineSite{
			0x20: {{Name: "hot", InlineLevel: 1}},
		},
	})
	oldRaw := []Sample{{ID: "s", Weight: 7, Time: 50, Stack: []Frame{{Missing: true, Addr: 0x1020}}}}
	newRaw := []Sample{{ID: "s", Weight: 7, Time: 150, Stack: []Frame{{Missing: true, Addr: 0x3020}}}}
	oldSamples := Reinterpret(oldRaw, NewTimedResolver(history, oldProfile))
	newSamples := Reinterpret(newRaw, NewTimedResolver(history, newProfile))

	oldTree, newTree := TreeFor(oldSamples), TreeFor(newSamples)
	if !approxEq(oldTree.Root.TotalWeight, newTree.Root.TotalWeight) ||
		!approxEq(oldTree.Root.TotalWeight, 7) {
		t.Fatalf("relocation + inline change broke total weight: %.1f vs %.1f",
			oldTree.Root.TotalWeight, newTree.Root.TotalWeight)
	}
	hot := newTree.Find("libx.so" + buildSep + "new" + moduleSep + "hot" + inlineSep + "1")
	leaf := newTree.Find(
		"libx.so"+buildSep+"new"+moduleSep+"hot"+inlineSep+"1",
		"libx.so"+buildSep+"new"+moduleSep+"workV2",
	)
	if hot == nil || leaf == nil || !approxEq(hot.TotalWeight, 7) || !approxEq(leaf.SelfWeight, 7) {
		t.Fatal("new inline tree did not preserve the full sample weight")
	}
	diff := CompareInterpretations(oldSamples, newSamples)
	if diff.ByKind(DiffAttribution) == nil || !approxEq(diff.OldTotal, diff.NewTotal) {
		t.Fatalf("relocation should be attribution with conserved totals: %+v", diff.Summarize())
	}
}

func TestSessionUndoRestoresPreviousView(t *testing.T) {
	history := reloadHistory()
	profile, _ := reloadProfiles()
	raw := []Sample{{ID: "s", Weight: 1, Time: 50, Stack: []Frame{{Missing: true, Addr: 0x1110}}}}
	session := NewSession(raw)

	before := session.Samples()[0].Stack[0]
	session.Reinterpret(NewTimedResolver(history, profile))
	after := session.Samples()[0].Stack[0]
	if after.Symbol != "alpha" || after.Addr != 0x1110 {
		t.Fatalf("reinterpret did not apply: %+v", after)
	}
	if !session.CanUndo() || !session.Undo() {
		t.Fatal("undo unavailable after reinterpret")
	}
	restored := session.Samples()[0].Stack[0]
	if !restored.Missing || restored.Addr != before.Addr || session.CanUndo() {
		t.Fatalf("old view not restored: %+v", restored)
	}
	if session.Undo() {
		t.Fatal("only one undo level is supported")
	}
}
