package perfsample

import (
	"sort"
	"strings"
)

// PathChange 描述同一条调用路径在两次运行之间的权重变化。
type PathChange struct {
	Path       []string
	OldSelf    float64
	NewSelf    float64
	NewTotal   float64
	OldTotal   float64
	DeltaSelf  float64
	DeltaTotal float64
	InOld      bool
	InNew      bool
}

// FullName 用 "/" 连接调用路径，便于展示。
func (c PathChange) FullName() string { return strings.Join(c.Path, "/") }

// Comparison 是两次运行在全部调用路径上的对比结果。
type Comparison struct {
	Changes []PathChange
}

// Compare 对比两棵执行树（通常对应同一程序的两次运行/两个版本）。
// 节点按完整调用路径对齐，而不是只按函数名对齐。
func Compare(oldTree, newTree *Tree) *Comparison {
	old := map[string]*Node{}
	new := map[string]*Node{}
	collect(oldTree.Root, nil, old)
	collect(newTree.Root, nil, new)

	keys := map[string]bool{}
	for k := range old {
		keys[k] = true
	}
	for k := range new {
		keys[k] = true
	}

	changes := make([]PathChange, 0, len(keys))
	for k := range keys {
		if k == "" {
			continue // 虚拟根不参与逐路径对比
		}
		path := strings.Split(k, pathSep)
		c := PathChange{Path: path, InOld: old[k] != nil, InNew: new[k] != nil}
		if o := old[k]; o != nil {
			c.OldSelf, c.OldTotal = o.SelfWeight, o.TotalWeight
		}
		if n := new[k]; n != nil {
			c.NewSelf, c.NewTotal = n.SelfWeight, n.TotalWeight
		}
		c.DeltaSelf = c.NewSelf - c.OldSelf
		c.DeltaTotal = c.NewTotal - c.OldTotal
		changes = append(changes, c)
	}
	sort.Slice(changes, func(i, j int) bool {
		return changes[i].FullName() < changes[j].FullName()
	})
	return &Comparison{Changes: changes}
}

const pathSep = "\x00"

func collect(n *Node, prefix []string, out map[string]*Node) {
	for _, c := range n.Children {
		path := append(append([]string{}, prefix...), c.Name)
		out[strings.Join(path, pathSep)] = c
		collect(c, path, out)
	}
}

// Shared 只返回两次运行都存在的相同调用路径上的变化。
func (cmp *Comparison) Shared() []PathChange {
	var out []PathChange
	for _, c := range cmp.Changes {
		if c.InOld && c.InNew {
			out = append(out, c)
		}
	}
	return out
}

// TopChanges 返回包含权重变化绝对值最大的 n 条路径。
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

// WeightReport 是合并批次后的权重对账结果。
type WeightReport struct {
	TreeTotal  float64
	GivenTotal float64
	TreeCount  int64
	GivenCount int64
	Balanced   bool
}

// Reconcile 校验树中计入的总权重/样本数是否与各批次声明的一致。
func Reconcile(t *Tree, givenWeight float64, givenCount int64) WeightReport {
	r := WeightReport{
		TreeTotal:  t.Root.TotalWeight,
		GivenTotal: givenWeight,
		TreeCount:  t.Root.SampleCount,
		GivenCount: givenCount,
	}
	r.Balanced = r.TreeCount == r.GivenCount && absFloat(r.TreeTotal-r.GivenTotal) < 1e-9
	return r
}
