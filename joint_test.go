package perfsample

import (
	"encoding/json"
	"testing"
)

func jointReloadHistory() *ModuleHistory {
	return NewModuleHistory(
		ModuleSpan{Module: "v1.so", Base: 100, Limit: 200, Start: 0, End: 100, Generation: 1},
		ModuleSpan{Module: "v2.so", Base: 100, Limit: 200, Start: 100, End: 200, Generation: 2},
	)
}

func jointResolver() JointTimedResolver {
	return JointResolverFunc(func(f Frame, local uint64, tr *SourceTrack, span ModuleSpan) []FrameInfo {
		if span.Generation == 1 {
			return []FrameInfo{{Symbol: "old"}}
		}
		return []FrameInfo{{Symbol: "new"}}
	})
}

func jointSample(id string, weight float64, local uint64) Sample {
	return Sample{ID: id, Weight: weight, Time: local, Prov: Provenance{Source: "s1"},
		Stack: []Frame{{Symbol: "main"}, {Missing: true, Addr: 110}}}
}

func TestJointReinterpretMarksUncertainModuleBoundary(t *testing.T) {
	tr := NewSourceTrack("s1", []Sample{
		jointSample("a", 2, 40),
		jointSample("b", 3, 60),
	}, 0, 50)
	run := NewJointRun("run", tr)
	samples := ReinterpretJoint(run, jointReloadHistory(), jointResolver())
	tree := NewTree()
	w, n := tree.AddBatch(samples)
	if !approxEq(w, 5) || n != 2 {
		t.Fatalf("joint samples must be conserved: %v/%d", w, n)
	}
	uncertain := tree.Find("main", "(time-uncertain@0x6e)")
	if uncertain == nil || !uncertain.TimeUncertain || !approxEq(uncertain.SelfWeight, 3) {
		t.Fatalf("boundary sample must remain uncertain: %+v", uncertain)
	}
	if tree.Find("main", "v1.so\x01:old\x01g1") == nil {
		t.Fatal("fully inside old generation sample should be attributed")
	}
}

func TestJointReinterpretKeepsFramesWhenModuleHistoryAbsentOrMisses(t *testing.T) {
	tr := NewSourceTrack("s1", []Sample{jointSample("a", 2, 60)}, 0, 50)
	run := NewJointRun("run", tr)
	samples := ReinterpretJoint(run, nil, nil)
	if got := samples[0].Stack[1]; got.Uncertain || got.Symbol != "" || got.Module != "" {
		t.Fatalf("missing module history must preserve raw frame: %+v", got)
	}
	samples = ReinterpretJoint(run, NewModuleHistory(), nil)
	if got := samples[0].Stack[1]; got.Uncertain || got.Symbol != "" || got.Module != "" {
		t.Fatalf("address outside all spans must preserve raw frame: %+v", got)
	}
}

func TestAdjustAlignmentChangesOnlyAffectedAttributionAndUndo(t *testing.T) {
	tr := NewSourceTrack("s1", []Sample{jointSample("a", 4, 60)}, 0, 50)
	sess := NewJointSession(NewJointRun("run", tr), "bounded")
	sess.SetSymbols(jointReloadHistory(), jointResolver(), "symbols")
	if sess.Current().Tree.Find("main", "(time-uncertain@0x6e)") == nil {
		t.Fatal("expected initial uncertain attribution")
	}
	sess.SaveView("before")
	if _, err := sess.AdjustAlignment("s1", 0, 0); err != nil {
		t.Fatal(err)
	}
	oldNode := sess.Current().Tree.Find("main", "v1.so\x01:old\x01g1")
	if oldNode == nil || !approxEq(oldNode.SelfWeight, 4) {
		t.Fatalf("exact alignment did not move attribution to v1: %+v", oldNode)
	}
	before, ok := sess.View("before")
	if !ok || before.Tree.Find("main", "(time-uncertain@0x6e)") == nil {
		t.Fatal("previous conclusion was overwritten")
	}
	edit, ok := sess.UndoLastAlignment()
	if !ok || edit.Source != "s1" || edit.NewOffsetMin != 0 || edit.NewOffsetMax != 0 {
		t.Fatalf("unexpected undo edit: %+v %v", edit, ok)
	}
	if sess.Current().Tree.Find("main", "(time-uncertain@0x6e)") == nil {
		t.Fatal("undo did not restore only affected attribution")
	}
}

func TestCompareJointRunsKeepsCoverageAndWeightSeparate(t *testing.T) {
	oldTrack := &SourceTrack{ID: "s1", Name: "s1", OffsetMin: 0, OffsetMax: 0,
		CoverageStart: 0, CoverageEnd: 100, Samples: []Sample{
			jointSample("a", 2, 10),
			jointSample("b", 5, 50),
		}}
	newTrack := &SourceTrack{ID: "s1", Name: "s1", OffsetMin: 0, OffsetMax: 0,
		CoverageStart: 0, CoverageEnd: 100, Samples: []Sample{
			jointSample("a", 7, 10),
			jointSample("c", 4, 70),
		}}
	cmp := CompareJointRuns(NewJointRun("old", oldTrack), NewJointRun("new", newTrack))
	if len(cmp.Hotspots) != 1 {
		t.Fatalf("same leaf path expected one hotspot, got %+v", cmp.Hotspots)
	}
	h := cmp.Hotspots[0]
	if !approxEq(h.OldObserved, 7) || !approxEq(h.NewObserved, 11) {
		t.Errorf("observed weights = %v/%v, want 7/11", h.OldObserved, h.NewObserved)
	}
	if !approxEq(h.OldComparable, 2) || !approxEq(h.NewComparable, 7) ||
		!approxEq(h.DeltaComparable, 5) {
		t.Errorf("only paired time points should compare: %+v", h)
	}

	partial := CompareJointRuns(NewJointRun("old", oldTrack), NewJointRun("new",
		NewSourceTrack("other-source", []Sample{jointSample("x", 9, 10)}, 0, 0)))
	if partial.Hotspots[0].Comparable || !approxEq(partial.Hotspots[0].OldComparable, 0) {
		t.Error("missing source must not contribute zero or paired comparable weight")
	}
}

func TestJointSessionStateRoundTrip(t *testing.T) {
	tr := NewSourceTrack("s1", []Sample{jointSample("a", 4, 60)}, 0, 0)
	sess := NewJointSession(NewJointRun("run", tr), "bounded")
	sess.SetSymbols(jointReloadHistory(), jointResolver(), "symbols")
	sess.AdjustAlignment("s1", 0, 50)
	sess.SaveView("saved-uncertain")
	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	var state JointSessionState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	restored := RestoreJointSession(&state, jointResolver())
	if restored.Current().Tree.Find("main", "(time-uncertain@0x6e)") == nil {
		t.Fatal("restored view did not reproduce selected alignment")
	}
	v, ok := restored.View("saved-uncertain")
	if !ok || v.Tree.Find("main", "(time-uncertain@0x6e)") == nil {
		t.Fatal("saved conclusion not restored")
	}
	edit, ok := restored.UndoLastAlignment()
	if !ok || edit.OldOffsetMin != 0 || edit.OldOffsetMax != 0 || edit.NewOffsetMax != 50 {
		t.Fatalf("undo metadata not restored: %+v %v", edit, ok)
	}
	if restored.Current().Tree.Find("main", "v1.so\x01:old\x01g1") == nil {
		t.Fatal("restored undo did not return to raw alignment")
	}
}
