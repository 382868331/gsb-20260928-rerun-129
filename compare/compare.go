// Package compare diffs two profiles on the same call paths.
package compare

import (
	"sort"

	"perfcore/tree"
)

// Row is the change on one identical call path between two runs.
type Row struct {
	Path           tree.Path `json:"path"`
	Symbol         string    `json:"symbol"`
	FromSelf       int64     `json:"from_self"`
	ToSelf         int64     `json:"to_self"`
	FromInclusive  int64     `json:"from_inclusive"`
	ToInclusive    int64     `json:"to_inclusive"`
	DeltaSelf      int64     `json:"delta_self"`
	DeltaInclusive int64     `json:"delta_inclusive"`
	FromSelfPct    float64   `json:"from_self_pct"`
	ToSelfPct      float64   `json:"to_self_pct"`
	DeltaSelfPct   float64   `json:"delta_self_pct"`
	Status         string    `json:"status"`
}

// Report compares two profiles. Paths are matched by frame-key sequence so the
// two runs must use the same key scheme (symbol version differences are still
// visible via the profiles' sources).
type Report struct {
	FromLabel string `json:"from_label"`
	ToLabel   string `json:"to_label"`
	FromTotal int64  `json:"from_total"`
	ToTotal   int64  `json:"to_total"`
	Rows      []Row  `json:"rows"`
	Common    int    `json:"common"`
	Added     []Row  `json:"added"`
	Removed   []Row  `json:"removed"`
}

// Diff builds a comparison report sorted by absolute self-weight delta.
func Diff(from, to *tree.Profile) *Report {
	r := &Report{
		FromLabel: from.Label, ToLabel: to.Label,
		FromTotal: from.TotalWeight, ToTotal: to.TotalWeight,
	}
	fromNodes := map[string]*tree.Node{}
	toNodes := map[string]*tree.Node{}
	index(from, fromNodes)
	index(to, toNodes)

	keys := map[string]tree.Path{}
	for k, pth := range pathsOf(from) {
		keys[k] = pth
	}
	for k, pth := range pathsOf(to) {
		keys[k] = pth
	}

	for key, pth := range keys {
		fn, inFrom := fromNodes[key]
		tn, inTo := toNodes[key]
		row := Row{Path: pth}
		if inFrom {
			row.FromSelf = fn.SelfWeight
			row.FromInclusive = fn.InclusiveWeight
			row.FromSelfPct = pct(fn.SelfWeight, from.TotalWeight)
			row.Symbol = fn.Frame.Symbol
		}
		if inTo {
			row.ToSelf = tn.SelfWeight
			row.ToInclusive = tn.InclusiveWeight
			row.ToSelfPct = pct(tn.SelfWeight, to.TotalWeight)
			if row.Symbol == "" {
				row.Symbol = tn.Frame.Symbol
			}
		}
		row.DeltaSelf = row.ToSelf - row.FromSelf
		row.DeltaInclusive = row.ToInclusive - row.FromInclusive
		row.DeltaSelfPct = row.ToSelfPct - row.FromSelfPct

		switch {
		case inFrom && inTo:
			r.Common++
			switch {
			case row.DeltaSelf > 0:
				row.Status = "hotter"
			case row.DeltaSelf < 0:
				row.Status = "cooler"
			default:
				row.Status = "same"
			}
			r.Rows = append(r.Rows, row)
		case inTo:
			row.Status = "added"
			r.Added = append(r.Added, row)
		default:
			row.Status = "removed"
			r.Removed = append(r.Removed, row)
		}
	}

	sortRows(r.Rows)
	sortRows(r.Added)
	sortRows(r.Removed)
	return r
}

func index(p *tree.Profile, m map[string]*tree.Node) {
	p.Walk(func(e tree.WalkEntry) {
		m[e.Path.String()] = e.Node
	})
}

func pathsOf(p *tree.Profile) map[string]tree.Path {
	m := map[string]tree.Path{}
	p.Walk(func(e tree.WalkEntry) {
		m[e.Path.String()] = e.Path
	})
	return m
}

func sortRows(rows []Row) {
	sort.Slice(rows, func(i, j int) bool {
		if abs(rows[i].DeltaSelf) != abs(rows[j].DeltaSelf) {
			return abs(rows[i].DeltaSelf) > abs(rows[j].DeltaSelf)
		}
		return rows[i].Path.String() < rows[j].Path.String()
	})
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func pct(part, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) * 100 / float64(total)
}
