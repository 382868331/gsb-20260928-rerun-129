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
	ID          FrameID
	IDPath      []FrameID
	SelfWeight  float64
	TotalWeight float64
	SampleCount int64
	Children    map[string]*Node

	// HasMissing 表示本节点是否聚合过符号缺失的栈层。
	HasMissing bool

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
	Root       *Node
	rawSamples []Sample
}

// NewTree 创建空执行树。
func NewTree() *Tree {
	return &Tree{Root: newNode("")}
}

func newNode(name string) *Node {
	return &Node{
		Name:     name,
		Children: map[string]*Node{},
		prov:     map[provKey]*provStat{},
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
	s = cloneSample(s)
	t.rawSamples = append(t.rawSamples, s)

	w := s.weight()
	k := provKey{source: s.Prov.Source, symbolVersion: s.Prov.SymbolVersion}

	cur := t.Root
	cur.TotalWeight += w
	cur.SampleCount++
	cur.addProv(k, 0, w)

	var idPath []FrameID
	for i, f := range s.Stack {
		key := frameKey(f)
		child := cur.Children[key]
		if child == nil {
			child = newNode(key)
			cur.Children[key] = child
		}
		if f.Missing || f.Symbol == "" {
			child.HasMissing = true
		}
		child.ID = f.Identity()
		idPath = append(append([]FrameID{}, idPath...), child.ID)
		child.IDPath = append([]FrameID{}, idPath...)
		child.TotalWeight += w
		child.SampleCount++

		isLeaf := i == len(s.Stack)-1
		if isLeaf {
			child.SelfWeight += w
		}
		child.addProv(k, leafSelf(isLeaf, w), w)
		cur = child
	}
}

func cloneSample(s Sample) Sample {
	clone := s
	if s.Stack != nil {
		clone.Stack = append([]Frame{}, s.Stack...)
	}
	return clone
}

// RawSamples 返回树保留的原始样本副本。符号补全只重新解释这些样本，
// 不会修改原始地址或权重。
func (t *Tree) RawSamples() []Sample {
	out := make([]Sample, len(t.rawSamples))
	for i, s := range t.rawSamples {
		out[i] = cloneSample(s)
	}
	return out
}

// FrameResolver 用补全后的符号资料重新解释一个原始帧。
// 返回多个帧时表示一个原始地址展开成一条内联调用链，调用方向为
// 调用者到被内联的采样点；返回空切片表示保持原帧。
type FrameResolver func(Frame) []Frame

// Reinterpret 使用补全后的符号资料重新解释全部原始样本并生成新树。
// 样本 ID、原始地址、权重和来源均保持不变；树中权重不增不减。
func (t *Tree) Reinterpret(resolver FrameResolver) *Tree {
	out := NewTree()
	for _, original := range t.rawSamples {
		s := cloneSample(original)
		if resolver != nil {
			var stack []Frame
			for _, f := range s.Stack {
				replaced := resolver(f)
				if len(replaced) == 0 {
					stack = append(stack, f)
					continue
				}
				for _, rf := range replaced {
					// 补符号/内联层次只能改变解释；原始采样地址必须保留。
					rf.Addr = f.Addr
					stack = append(stack, rf)
				}
			}
			s.Stack = stack
		}
		out.AddSample(s)
		// 新树展示重新解释后的路径，但留存样本必须仍是解释前的原始帧。
		out.rawSamples[len(out.rawSamples)-1] = original
	}
	return out
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

// FindID 按完整函数身份路径定位节点，用于区分不同模块或内联层次的同名函数。
func (t *Tree) FindID(path ...FrameID) *Node {
	cur := t.Root
	for _, want := range path {
		var next *Node
		for _, c := range cur.Children {
			if c.ID == want {
				next = c
				break
			}
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

// DisplayName 返回适合展示的函数名，内部归并键不会直接暴露给报告。
func (n *Node) DisplayName() string {
	if n.ID.Missing || n.ID.Symbol == "" {
		if n.ID.Addr != 0 {
			return unknownPrefix + addrHex(n.ID.Addr) + ")"
		}
		return unknownName
	}
	return n.ID.Symbol
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
