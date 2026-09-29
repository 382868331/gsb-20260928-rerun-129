// Package tree builds traceable execution trees from weighted stack samples.
//
// Design notes:
//   - A node is identified by its parent node plus the frame key. Two frames
//     with the same key at different positions in a stack (recursion) therefore
//     become distinct nodes: a recursive f->f->f chain is never folded into a
//     single call.
//   - SelfWeight counts weight whose leaf is exactly this node. InclusiveWeight
//     counts weight of every sample whose stack passes through this node.
//   - Unresolved frames are kept as normal nodes (Resolved=false) so that no
//     sample is lost.
//   - Every node records the individual sample contributions behind it, which
//     makes the numbers traceable back to the source batches.
package tree

import (
	"fmt"
	"perfcore/model"
	"strings"
)

// Contribution is one sample's weight attributed to a node.
type Contribution struct {
	BatchID  string `json:"batch_id"`
	SampleID string `json:"sample_id"`
	Weight   int64  `json:"weight"`
	AsLeaf   bool   `json:"as_leaf"`
}

// Node is one call site occurrence in the execution tree.
type Node struct {
	Frame           model.Frame    `json:"frame"`
	Depth           int            `json:"depth"`
	SelfWeight      int64          `json:"self_weight"`
	InclusiveWeight int64          `json:"inclusive_weight"`
	Children        []*Node        `json:"children,omitempty"`
	Contributions   []Contribution `json:"contributions,omitempty"`

	children map[string]*Node
}

// Profile is one merged execution tree with reconciliation data.
type Profile struct {
	Label          string         `json:"label"`
	Root           *Node          `json:"root"`
	Sources        []model.Source `json:"sources"`
	SampleCount    int            `json:"sample_count"`
	TotalWeight    int64          `json:"total_weight"`
	UnresolvedSelf int64          `json:"unresolved_self_weight"`
}

const rootKey = "<root>"

// Build constructs a profile from one or more batches.
// It returns an error if any invariant (empty stack, non-positive weight,
// weight reconciliation failure) is violated.
func Build(label string, batches ...model.Batch) (*Profile, error) {
	root := &Node{
		Frame:    model.Frame{Key: rootKey, Symbol: rootKey, Resolved: true},
		children: map[string]*Node{},
	}
	p := &Profile{Label: label, Root: root}
	sourceIndex := map[string]int{}

	for _, batch := range batches {
		si, ok := sourceIndex[batch.ID]
		if !ok {
			si = len(p.Sources)
			sourceIndex[batch.ID] = si
			p.Sources = append(p.Sources, model.Source{
				BatchID: batch.ID, Collector: batch.Collector,
				CollectorVersion: batch.CollectorVersion,
				ProgramVersion:   batch.ProgramVersion,
				SymbolVersion:    batch.SymbolVersion,
			})
		}
		for i := range batch.Samples {
			s := &batch.Samples[i]
			if err := addSample(root, s, batch, p); err != nil {
				return nil, fmt.Errorf("batch %q sample %q: %w", batch.ID, s.ID, err)
			}
			p.Sources[si].Samples++
			p.Sources[si].Weight += s.Weight
		}
	}

	if err := p.reconcile(); err != nil {
		return nil, err
	}
	return p, nil
}

func addSample(root *Node, s *model.Sample, batch model.Batch, p *Profile) error {
	if len(s.Stack) == 0 {
		return fmt.Errorf("empty stack")
	}
	if s.Weight <= 0 {
		return fmt.Errorf("non-positive weight %d", s.Weight)
	}

	p.SampleCount++
	p.TotalWeight += s.Weight

	// Stacks are leaf-first; walk from the outermost frame to the leaf.
	current := root
	for i := len(s.Stack) - 1; i >= 0; i-- {
		f := s.Stack[i]
		if f.Key == "" {
			return fmt.Errorf("empty frame key at depth %d", i)
		}
		if f.SymbolVersion == "" {
			f.SymbolVersion = batch.SymbolVersion
		}
		n := current.children[f.Key]
		if n == nil {
			n = &Node{
				Frame: f, Depth: current.Depth + 1, children: map[string]*Node{},
			}
			current.children[f.Key] = n
			current.Children = append(current.Children, n)
		}
		n.InclusiveWeight += s.Weight
		n.Contributions = append(n.Contributions, Contribution{
			BatchID: batch.ID, SampleID: s.ID, Weight: s.Weight, AsLeaf: i == 0,
		})
		current = n
	}

	// The innermost frame (index 0) is where the sample was actually taken.
	leaf := current
	leaf.SelfWeight += s.Weight
	if !leaf.Frame.Resolved {
		p.UnresolvedSelf += s.Weight
	}
	return nil
}

// reconcile verifies that merged totals line up across independent computations.
func (p *Profile) reconcile() error {
	var flatSelf, leafWeight int64
	var leafCount int
	var walk func(n *Node)
	walk = func(n *Node) {
		if n != p.Root {
			flatSelf += n.SelfWeight
			for _, c := range n.Contributions {
				if c.AsLeaf {
					leafCount++
					leafWeight += c.Weight
				}
			}
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(p.Root)

	var srcWeight int64
	for _, src := range p.Sources {
		srcWeight += src.Weight
	}
	if p.TotalWeight != srcWeight {
		return fmt.Errorf("weight mismatch: merged %d != batches %d", p.TotalWeight, srcWeight)
	}
	if flatSelf != p.TotalWeight {
		return fmt.Errorf("weight mismatch: sum of self %d != total %d (lost sample?)", flatSelf, p.TotalWeight)
	}
	rootIncl := int64(0)
	for _, c := range p.Root.Children {
		rootIncl += c.InclusiveWeight
	}
	if rootIncl != p.TotalWeight {
		return fmt.Errorf("weight mismatch: tree inclusive %d != total %d", rootIncl, p.TotalWeight)
	}
	if leafCount != p.SampleCount {
		return fmt.Errorf("sample count mismatch: leaf attributions %d != samples %d", leafCount, p.SampleCount)
	}
	if leafWeight != p.TotalWeight {
		return fmt.Errorf("weight mismatch: leaf contributions %d != total %d", leafWeight, p.TotalWeight)
	}
	return nil
}

// FrameStat aggregates a frame across all of its tree occurrences
// (recursive occurrences collapse together here, but never in the tree).
type FrameStat struct {
	Frame           model.Frame
	SelfWeight      int64
	InclusiveWeight int64
	Occurrences     int
}

// FlatByFrame returns per-frame aggregated statistics keyed by frame key.
func (p *Profile) FlatByFrame() map[string]*FrameStat {
	m := map[string]*FrameStat{}
	var walk func(n *Node)
	walk = func(n *Node) {
		if n != p.Root {
			st := m[n.Frame.Key]
			if st == nil {
				st = &FrameStat{Frame: n.Frame}
				m[n.Frame.Key] = st
			}
			st.SelfWeight += n.SelfWeight
			st.InclusiveWeight += n.InclusiveWeight
			st.Occurrences++
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(p.Root)
	return m
}

// Path is one root-to-leaf (or root-to-node) frame-key path.
type Path []string

func (pth Path) String() string { return strings.Join(pth, " -> ") }

// Find returns the node at the given frame-key path, or nil.
func (p *Profile) Find(pth Path) *Node {
	n := p.Root
	for _, key := range pth {
		n = n.children[key]
		if n == nil {
			return nil
		}
	}
	return n
}

// AllPaths enumerates every node path in the tree.
func (p *Profile) AllPaths() []Path {
	var out []Path
	var walk func(n *Node, prefix Path)
	walk = func(n *Node, prefix Path) {
		for _, c := range n.Children {
			pth := append(append(Path{}, prefix...), c.Frame.Key)
			out = append(out, pth)
			walk(c, pth)
		}
	}
	walk(p.Root, nil)
	return out
}

// WalkEntry visits one node during a depth-first traversal.
type WalkEntry struct {
	Node       *Node
	Path       Path
	Recurrence int // how many times this node's key already occurs on Path
}

// Walk traverses the tree depth-first, synthetic root excluded.
func (p *Profile) Walk(fn func(WalkEntry)) {
	var walk func(n *Node, prefix Path)
	walk = func(n *Node, prefix Path) {
		for _, c := range n.Children {
			pth := append(append(Path{}, prefix...), c.Frame.Key)
			rec := 0
			for _, k := range pth {
				if k == c.Frame.Key {
					rec++
				}
			}
			fn(WalkEntry{Node: c, Path: pth, Recurrence: rec})
			walk(c, pth)
		}
	}
	walk(p.Root, nil)
}
