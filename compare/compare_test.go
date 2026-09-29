package compare

import (
	"testing"

	"perfcore/model"
	"perfcore/tree"
)

func fr(k string) model.Frame { return model.Frame{Key: k, Symbol: k, Resolved: true} }

func s(frames ...model.Frame) []model.Frame {
	out := make([]model.Frame, len(frames))
	for i, x := range frames {
		out[len(frames)-1-i] = x
	}
	return out
}

func build(t *testing.T, label string, weights map[string]int64) *tree.Profile {
	t.Helper()
	var samples []model.Sample
	id := 0
	for leaf, w := range weights {
		id++
		samples = append(samples, model.Sample{
			ID: string(rune('a' + id)), Weight: w,
			Stack: s(fr("main"), fr("handle"), fr(leaf)),
		})
	}
	p, err := tree.Build(label, model.Batch{ID: "b", Collector: "unit", Samples: samples})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDiffOnSamePaths(t *testing.T) {
	v1 := build(t, "v1", map[string]int64{"parse": 3, "fetch": 2, "old": 1})
	v2 := build(t, "v2", map[string]int64{"parse": 1, "fetch": 3, "new": 1})
	r := Diff(v1, v2)

	row := func(pth string) *Row {
		for i := range r.Rows {
			if r.Rows[i].Path.String() == pth {
				return &r.Rows[i]
			}
		}
		return nil
	}
	parse := row("main -> handle -> parse")
	if parse == nil || parse.DeltaSelf != -2 || parse.Status != "cooler" {
		t.Fatalf("parse row wrong: %+v", parse)
	}
	fetchRow := row("main -> handle -> fetch")
	if fetchRow == nil || fetchRow.DeltaSelf != 1 || fetchRow.Status != "hotter" {
		t.Fatalf("fetch row wrong: %+v", fetchRow)
	}
	if r.Common != 4 { // main, handle, parse, fetch
		t.Errorf("common = %d, want 4", r.Common)
	}
	if len(r.Added) != 1 || r.Added[0].Path.String() != "main -> handle -> new" {
		t.Errorf("added = %+v", r.Added)
	}
	if len(r.Removed) != 1 || r.Removed[0].Path.String() != "main -> handle -> old" {
		t.Errorf("removed = %+v", r.Removed)
	}
	// Percentages are normalized per run total (both total 6 here).
	if !near(parse.FromSelfPct, 50) || !near(parse.ToSelfPct, 20) {
		t.Errorf("pct parse from=%f to=%f", parse.FromSelfPct, parse.ToSelfPct)
	}
}

func near(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
