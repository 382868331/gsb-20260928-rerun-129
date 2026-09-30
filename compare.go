package perfsample

import (
	"sort"
	"strings"
)

type MatchStatus int

const (
	// Matched 表示新旧路径可以可靠一一对应。
	Matched MatchStatus = iota
	// Unmatched 表示无法可靠对应。
	Unmatched
)

func (s MatchStatus) String() string {
	if s == Matched {
		return "matched"
	}
	return "unmatched"
}

type PathChange struct {
	Path          []string    `json:"path"`
	OldIDPath     []FrameID   `json:"old_id_path,omitempty"`
	NewIDPath     []FrameID   `json:"new_id_path,omitempty"`
	OldSelf       float64     `json:"old_self,omitempty"`
	NewSelf       float64     `json:"new_self,omitempty"`
	OldTotal      float64     `json:"old_total,omitempty"`
	NewTotal      float64     `json:"new_total,omitempty"`
	DeltaSelf     float64     `json:"delta_self"`
	DeltaTotal    float64     `json:"delta_total"`
	InOld         bool        `json:"in_old"`
	InNew         bool        `json:"in_new"`
	Status        MatchStatus `json:"status"`
	SampleChanged bool        `json:"sample_changed,omitempty"`
	SymbolChanged bool        `json:"symbol_changed,omitempty"`
}

func (c PathChange) FullName() string { return strings.Join(c.Path, "/") }
func (c PathChange) Matched() bool    { return c.Status == Matched }

type Comparison struct {
	Changes []PathChange `json:"changes"`
}

func Compare(oldTree, newTree *Tree) *Comparison {
	return CompareBuilds(oldTree, newTree, nil)
}

// FrameMapping 声明一个函数帧在两个构建间的可靠对应关系。
type FrameMapping struct {
	From FrameID
	To   FrameID
}

func CompareBuilds(oldTree, newTree *Tree, mappings []FrameMapping) *Comparison {
	oldNodes := map[string]*nodeEntry{}
	newNodes := map[string]*nodeEntry{}
	collectID(oldTree.Root, nil, oldNodes)
	collectID(newTree.Root, nil, newNodes)

	forward := map[FrameID]FrameID{}
	reverse := map[FrameID]FrameID{}
	for _, m := range mappings {
		if existing, conflict := forward[m.From]; conflict && existing != m.To {
			continue
		}
		if existing, conflict := reverse[m.To]; conflict && existing != m.From {
			continue
		}
		forward[m.From] = m.To
		reverse[m.To] = m.From
	}

	matchedOld := map[string]string{}
	matchedNew := map[string]string{}
	for ok, oldEntry := range oldNodes {
		nk := idPathKey(mapIDPath(oldEntry.idPath, forward))
		if newNodes[nk] != nil {
			matchedOld[ok] = nk
			matchedNew[nk] = ok
		}
	}
	for nk := range newNodes {
		if oldNodes[nk] != nil {
			matchedOld[nk] = nk
			matchedNew[nk] = nk
		}
	}

	seenNew := map[string]bool{}
	changes := make([]PathChange, 0, len(oldNodes)+len(newNodes))
	for ok, oldEntry := range oldNodes {
		if newEntry := newNodes[matchedOld[ok]]; newEntry != nil {
			seenNew[matchedOld[ok]] = true
			changes = append(changes, matchedChange(oldEntry, newEntry))
		} else {
			changes = append(changes, unmatchedChange(oldEntry, nil, true))
		}
	}
	for nk, newEntry := range newNodes {
		if seenNew[nk] {
			continue
		}
		if oldEntry := oldNodes[matchedNew[nk]]; oldEntry != nil {
			changes = append(changes, matchedChange(oldEntry, newEntry))
		} else {
			changes = append(changes, unmatchedChange(nil, newEntry, false))
		}
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].FullName() < changes[j].FullName() })
	return &Comparison{Changes: changes}
}

type nodeEntry struct {
	node   *Node
	idPath []FrameID
}

func collectID(n *Node, ids []FrameID, out map[string]*nodeEntry) {
	for _, c := range n.Children {
		childIDs := append(append([]FrameID{}, ids...), c.ID)
		out[idPathKey(childIDs)] = &nodeEntry{node: c, idPath: childIDs}
		collectID(c, childIDs, out)
	}
}

func idPathKey(ids []FrameID) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = frameIDKey(id)
	}
	return strings.Join(parts, pathSep)
}

func frameIDKey(id FrameID) string {
	if id.Missing || id.Symbol == "" {
		return "m" + pathSep + id.Module + pathSep + addrHex(id.Addr) + pathSep + addrHex(uint64(id.InlineDepth))
	}
	return "s" + pathSep + id.Module + pathSep + id.Symbol + pathSep + addrHex(uint64(id.InlineDepth))
}

func mapIDPath(ids []FrameID, m map[FrameID]FrameID) []FrameID {
	out := make([]FrameID, len(ids))
	for i, id := range ids {
		if mapped, ok := m[id]; ok {
			out[i] = mapped
		} else {
			out[i] = id
		}
	}
	return out
}

func displayPath(ids []FrameID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		if id.Missing || id.Symbol == "" {
			if id.Addr != 0 {
				out[i] = unknownPrefix + addrHex(id.Addr) + ")"
			} else {
				out[i] = unknownName
			}
		} else {
			out[i] = id.Symbol
		}
	}
	return out
}

func matchedChange(oldEntry, newEntry *nodeEntry) PathChange {
	oldNode, newNode := oldEntry.node, newEntry.node
	c := PathChange{
		OldIDPath: append([]FrameID{}, oldEntry.idPath...),
		NewIDPath: append([]FrameID{}, newEntry.idPath...),
		OldSelf:   oldNode.SelfWeight,
		NewSelf:   newNode.SelfWeight,
		OldTotal:  oldNode.TotalWeight,
		NewTotal:  newNode.TotalWeight,
		InOld:     true,
		InNew:     true,
		Status:    Matched,
	}
	c.DeltaSelf = c.NewSelf - c.OldSelf
	c.DeltaTotal = c.NewTotal - c.OldTotal
	c.SampleChanged = absFloat(c.DeltaSelf) > 1e-9 || absFloat(c.DeltaTotal) > 1e-9
	c.SymbolChanged = idPathKey(c.OldIDPath) != idPathKey(c.NewIDPath)
	c.Path = displayPath(newEntry.idPath)
	return c
}

func unmatchedChange(oldEntry, newEntry *nodeEntry, inOld bool) PathChange {
	c := PathChange{Status: Unmatched, InOld: inOld, InNew: !inOld}
	if inOld {
		c.Path = displayPath(oldEntry.idPath)
		c.OldIDPath = append([]FrameID{}, oldEntry.idPath...)
		c.OldSelf, c.OldTotal = oldEntry.node.SelfWeight, oldEntry.node.TotalWeight
		c.DeltaSelf, c.DeltaTotal = -c.OldSelf, -c.OldTotal
	} else {
		c.Path = displayPath(newEntry.idPath)
		c.NewIDPath = append([]FrameID{}, newEntry.idPath...)
		c.NewSelf, c.NewTotal = newEntry.node.SelfWeight, newEntry.node.TotalWeight
		c.DeltaSelf, c.DeltaTotal = c.NewSelf, c.NewTotal
	}
	return c
}

const pathSep = "\x00"

func (cmp *Comparison) Shared() []PathChange {
	out := []PathChange{}
	for _, c := range cmp.Changes {
		if c.Status == Matched {
			out = append(out, c)
		}
	}
	return out
}

func (cmp *Comparison) Unmatched() []PathChange {
	out := []PathChange{}
	for _, c := range cmp.Changes {
		if c.Status == Unmatched {
			out = append(out, c)
		}
	}
	return out
}

func (cmp *Comparison) TopChanges(n int) []PathChange {
	all := make([]PathChange, len(cmp.Changes))
	copy(all, cmp.Changes)
	sort.SliceStable(all, func(i, j int) bool {
		return absFloat(all[i].DeltaTotal) > absFloat(all[j].DeltaTotal)
	})
	if n > len(all) {
		n = len(all)
	}
	return all[:n]
}

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

type WeightReport struct {
	TreeTotal  float64
	GivenTotal float64
	TreeCount  int64
	GivenCount int64
	Balanced   bool
}

func Reconcile(t *Tree, givenWeight float64, givenCount int64) WeightReport {
	r := WeightReport{TreeTotal: t.Root.TotalWeight, GivenTotal: givenWeight, TreeCount: t.Root.SampleCount, GivenCount: givenCount}
	r.Balanced = r.TreeCount == r.GivenCount && absFloat(r.TreeTotal-r.GivenTotal) < 1e-9
	return r
}
