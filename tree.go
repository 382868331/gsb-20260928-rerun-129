package perfsample

import "sort"

// Node 是执行树上的一个函数节点。
//
// SelfWeight 只统计采样点恰好落在本函数（栈顶叶子）的权重；
// TotalWeight 统计经过本节点的全部样本权重，即自身耗时 + 所有子调用。
// 递归函数的每一层是栈中独立的一层，因此 f -> f 会在父子两个
// 位置各计一次包含权重，不会被折叠成一次调用。
type Node struct {
	Name        string
	SelfWeight  float64
	TotalWeight float64
	SampleCount int64
	Children    map[string]*Node

	// HasMissing 表示本节点是否聚合过符号缺失的栈层。
	HasMissing bool
	// Module 为该节点帧所属模块；空表示未指定（与第一版样本兼容）。
	Module string
	// Symbol 为该节点帧的符号名（去掉模块/内联限定后的展示名）。
	Symbol string
	// InlineLevel 为该节点帧的内联层次，0 表示物理函数。
	InlineLevel int
	// Generation 为该节点帧所属模块的装载代次，0 表示未跟踪。
	Generation int

	// keyPath 记录该节点相对虚拟根的完整键路径，仅供内部跨版本回退定位。
	keyPath string

	// through 记录经过本节点的每条样本（按 Sample.ID）贡献的包含权重；
	// selfThrough 只记录采样点恰好落在本节点（叶子）的自身权重。
	// 空 ID 的样本不登记，改由地址/名字回退。重新解释（补符号、
	// 内联展开、函数移动）只改变样本落在哪些节点上，不改变样本本身，
	// 因此可通过这些锚点核对权重守恒、跨版本对应路径。
	through     map[string]float64
	selfThrough map[string]float64

	prov map[provKey]*provStat
}

type provKey struct {
	source        string
	symbolVersion string
}

type provStat struct {
	self  float64
	total float64
	count int64
}

// Tree 是带虚拟根的执行树。根不对应任何函数，其
// TotalWeight/SampleCount 等于已合并全部样本的总权重/总数，
// 可用于合并批次后的权重对账。
type Tree struct {
	Root *Node
}

// NewTree 创建空执行树。
func NewTree() *Tree {
	return &Tree{Root: newNode("")}
}

func newNode(name string) *Node {
	return &Node{
		Name:        name,
		Children:    map[string]*Node{},
		through:     map[string]float64{},
		selfThrough: map[string]float64{},
		prov:        map[provKey]*provStat{},
	}
}

// AddBatch 把一批样本合并进树，返回该批次计入的总权重与样本数。
func (t *Tree) AddBatch(samples []Sample) (batchWeight float64, batchCount int64) {
	for _, s := range samples {
		t.AddSample(s)
		batchWeight += s.weight()
		batchCount++
	}
	return batchWeight, batchCount
}

// AddSample 把单条样本沿栈路径累加进树。
// 路径上的每个节点都累加包含权重；只有栈顶节点累加自身权重。
func (t *Tree) AddSample(s Sample) {
	w := s.weight()
	k := provKey{source: s.Prov.Source, symbolVersion: s.Prov.SymbolVersion}

	cur := t.Root
	cur.TotalWeight += w
	cur.SampleCount++
	cur.addProv(k, 0, w)

	for i, f := range s.Stack {
		isLeaf := i == len(s.Stack)-1
		key := frameKey(f)
		child := cur.Children[key]
		if child == nil {
			child = newNode(key)
			if cur == t.Root {
				child.keyPath = key
			} else {
				child.keyPath = cur.keyPath + pathSep + key
			}
			cur.Children[key] = child
		}
		if f.Missing || f.Symbol == "" {
			child.HasMissing = true
			child.Generation = f.Generation
		} else {
			child.Module = f.Module
			child.InlineLevel = f.InlineLevel
			child.Symbol = f.Symbol
			child.Generation = f.Generation
		}
		child.TotalWeight += w
		child.SampleCount++
		if s.ID != "" {
			child.through[s.ID] += w
			if isLeaf {
				child.selfThrough[s.ID] += w
			}
		}

		if isLeaf {
			child.SelfWeight += w
		}
		child.addProv(k, leafSelf(isLeaf, w), w)
		cur = child
	}
}

func leafSelf(isLeaf bool, w float64) float64 {
	if isLeaf {
		return w
	}
	return 0
}

func (n *Node) addProv(k provKey, self, total float64) {
	st := n.prov[k]
	if st == nil {
		st = &provStat{}
		n.prov[k] = st
	}
	st.self += self
	st.total += total
	st.count++
}

// ProvBreakdown 是一个节点在某个来源/符号版本上的权重明细。
type ProvBreakdown struct {
	Source        string
	SymbolVersion string
	SelfWeight    float64
	TotalWeight   float64
	SampleCount   int64
}

// Provenance 返回本节点按来源与符号版本拆分的权重明细。
func (n *Node) Provenance() []ProvBreakdown {
	out := make([]ProvBreakdown, 0, len(n.prov))
	for k, st := range n.prov {
		out = append(out, ProvBreakdown{
			Source:        k.source,
			SymbolVersion: k.symbolVersion,
			SelfWeight:    st.self,
			TotalWeight:   st.total,
			SampleCount:   st.count,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].SymbolVersion < out[j].SymbolVersion
	})
	return out
}

// Find 按调用路径（函数名序列）定位节点，不存在返回 nil。
func (t *Tree) Find(path ...string) *Node {
	cur := t.Root
	for _, name := range path {
		cur = cur.Children[name]
		if cur == nil {
			return nil
		}
	}
	return cur
}

// SortedChildren 返回按名字排序的子节点，保证输出稳定。
func (n *Node) SortedChildren() []*Node {
	out := make([]*Node, 0, len(n.Children))
	for _, c := range n.Children {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Anchors 返回经过本节点的样本 ID -> 权重 的拷贝。
// 对于叶子节点，这些权重同时构成本节点的自身权重；对于内部节点，
// 它们是经过该节点（包含子调用）的权重。
// 重新解释前后同一组锚点的权重总和必须保持不变（权重守恒）。
func (n *Node) Anchors() map[string]float64 {
	out := make(map[string]float64, len(n.through))
	for id, w := range n.through {
		out[id] = w
	}
	return out
}

// LeafAnchors 返回采样点落在本节点的样本 ID -> 自身权重 的拷贝。
// 每个带 ID 的样本在一棵树中恰好有一个叶子位置，因此叶子锚点可以
// 在重新解释/跨版本对应中无歧义地回退定位。
func (n *Node) LeafAnchors() map[string]float64 {
	out := make(map[string]float64, len(n.selfThrough))
	for id, w := range n.selfThrough {
		out[id] = w
	}
	return out
}

// AnchorWeight 返回指定样本 ID 经过本节点的权重，不存在返回 0。
func (n *Node) AnchorWeight(id string) float64 {
	return n.through[id]
}

// DisplayName 返回适合展示的函数名（不含模块/内联限定）。
func (n *Node) DisplayName() string {
	if n.Symbol != "" {
		return n.Symbol
	}
	return n.Name
}
