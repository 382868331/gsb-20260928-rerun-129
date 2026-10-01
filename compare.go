package perfsample

import (
	"sort"
	"strings"
)

// PathChange 描述同一条调用路径在两次运行之间的权重变化。
type PathChange struct {
	Path []string
	// OldPath 是该变化在旧树中的实际路径；当存在符号移动时，
	// OldPath 与 Path（新路径）的名字可能不同。
	OldPath   []string
	OldSelf   float64
	NewSelf   float64
	NewTotal  float64
	OldTotal  float64
	DeltaSelf float64
	// DeltaTotal 仅在样本真正可比（Matched）时有意义；
	// 未匹配路径的 Delta 一律为 0，避免把“无法对应”误报成权重增减。
	DeltaTotal float64
	InOld      bool
	InNew      bool
	// Matched 表示新旧路径能够可靠对应（帧键相同或 Mapping 显式对应）。
	Matched bool
	// Kind 区分差异来源：
	// KindSame 无差异；KindSymbol 纯符号变化（锚点完全一致，权重不变）；
	// KindSample 样本权重发生变化（含仅一侧出现的路径）；KindUnmatched 未匹配。
	Kind ChangeKind
	// SymbolMove 非空时表示该路径经此映射对应（名字/模块/内联层次变化）。
	SymbolMove bool
}

// ChangeKind 说明一条路径差异来自样本变化还是符号变化。
type ChangeKind int

const (
	// KindSame 路径对应且自身/包含权重均无变化。
	KindSame ChangeKind = iota
	// KindSymbol 仅符号解释变化：同一批样本锚点、同一组权重，只是改名/移动/补全。
	KindSymbol
	// KindSample 对应路径上的样本权重发生了变化（采样点占比真的变了）。
	KindSample
	// KindUnmatched 无法在另一版本找到可靠对应路径。
	KindUnmatched
	// KindUncertain 两版都保留了该部分权重，但归属对应仍无法确认：
	// 既不能算成符号修订（同一身份），也不能报成采样权重变化。
	// 权重绝不丢弃，单列在此类别供人工核对。
	KindUncertain
)

// String 返回差异类型的稳定描述。
func (k ChangeKind) String() string {
	switch k {
	case KindSymbol:
		return "symbol"
	case KindSample:
		return "sample"
	case KindUnmatched:
		return "unmatched"
	case KindUncertain:
		return "uncertain"
	default:
		return "same"
	}
}

// FullName 用 "/" 连接调用路径，便于展示。
func (c PathChange) FullName() string {
	display := make([]string, len(c.Path))
	for i, p := range c.Path {
		display[i] = DisplayKey(p)
	}
	return strings.Join(display, "/")
}

// DisplayKey 把帧身份键转成可读形式：模块用 "module!symbol"，
// 内联层次追加 "[in]"。内部存储键使用 \x01 控制字符避免与用户符号冲突，
// 展示时统一翻译成可打印形式。
func DisplayKey(key string) string {
	rest := key
	var module string
	if i := strings.Index(rest, moduleSep); i >= 0 {
		module = rest[:i]
		rest = rest[i+len(moduleSep):]
	}
	inline := ""
	if i := strings.Index(rest, inlineSep); i >= 0 {
		inline = "[i" + rest[i+len(inlineSep):] + "]"
		rest = rest[:i]
	}
	// 装载代次限定（\x01g<n>）展示为 [g<n>]，与内联限定并列。
	generation := ""
	if i := strings.Index(rest, genSep); i >= 0 {
		generation = "[g" + rest[i+len(genSep):] + "]"
		rest = rest[:i]
	}
	if module != "" {
		return module + "!" + rest + inline + generation
	}
	return rest + inline + generation
}

// Comparison 是两次运行在全部调用路径上的对比结果。
type Comparison struct {
	Changes []PathChange
}

// Compare 对比两棵执行树（通常对应同一程序的两次运行/两个版本）。
// 节点按完整调用路径对齐，而不是只按函数名对齐。
func Compare(oldTree, newTree *Tree) *Comparison {
	return CompareMapped(oldTree, newTree, nil)
}

// CompareMapped 按跨构建版本映射对比两棵执行树。
// 对应规则：
//  1. 用 Mapping 把旧路径翻译成新路径：翻译后命中同一新节点即“符号对应”；
//  2. 旧帧含缺失符号导致无法翻译，或翻译后新树不存在该路径，则尝试用
//     样本锚点（Sample.ID）在新树中唯一回退定位；
//  3. 仍无法可靠对应的路径标为 KindUnmatched，不参与权重差值统计。
//
// 同名字函数位于不同模块或不同递归/内联层次时，键本身就不同，
// 不会被折叠；它们只有被 Mapping 显式关联才会对应。
func CompareMapped(oldTree, newTree *Tree, mapping *Mapping) *Comparison {
	old := map[string]*Node{}
	new := map[string]*Node{}
	oldPath := map[string][]string{}
	collect(oldTree.Root, nil, old, oldPath)
	collect(newTree.Root, nil, new, nil)

	// anchorLeaf: 样本ID -> 它作为采样点（叶子）落在的新节点，唯一。
	newAnchor := anchorLeafMap(newTree)

	// consumedNew 记录被旧路径占用的新键，避免一条新路径被多次对应。
	consumedNew := map[string]bool{}

	changes := make([]PathChange, 0, len(old)+len(new))

	for oldKey, o := range old {
		op := oldPath[oldKey]
		c := PathChange{Path: op, OldPath: op, InOld: true}
		c.OldSelf, c.OldTotal = o.SelfWeight, o.TotalWeight

		newKey, moved, translatable := "", false, true
		if mapping != nil {
			translated, ok := mapping.TranslatePath(op)
			translatable = ok
			if ok {
				newKey = strings.Join(translated, pathSep)
				moved = newKey != oldKey
			}
		} else {
			newKey = oldKey
		}

		n := new[newKey]
		if (n == nil || consumedNew[newKey]) && translatable {
			// 路径对应不上：用叶子样本锚点唯一回退。
			if nk, ok := anchorFallback(o, newAnchor); ok && !consumedNew[nk] {
				n = new[nk]
				newKey = nk
				moved = true
			}
		}

		if n != nil && translatable && !consumedNew[newKey] {
			consumedNew[newKey] = true
			c.InNew = true
			c.Matched = true
			c.SymbolMove = moved
			c.Path = strings.Split(newKey, pathSep)
			c.NewSelf, c.NewTotal = n.SelfWeight, n.TotalWeight
			c.DeltaSelf = c.NewSelf - c.OldSelf
			c.DeltaTotal = c.NewTotal - c.OldTotal
			switch {
			case approxEq(c.DeltaSelf, 0) && approxEq(c.DeltaTotal, 0):
				if moved {
					c.Kind = KindSymbol // 名字/位置变了，权重没变
				} else {
					c.Kind = KindSame
				}
			default:
				// 即使发生了符号移动，只要权重变了就是真实的样本变化。
				c.Kind = KindSample
			}
		} else {
			c.Matched = false
			c.Kind = KindUnmatched
		}
		changes = append(changes, c)
	}

	// 新树中未被任何旧路径占有的节点，是新增的未匹配路径。
	for newKey, n := range new {
		if consumedNew[newKey] {
			continue
		}
		p := strings.Split(newKey, pathSep)
		c := PathChange{Path: p, InNew: true, NewSelf: n.SelfWeight, NewTotal: n.TotalWeight,
			Kind: KindUnmatched}
		changes = append(changes, c)
	}

	sort.Slice(changes, func(i, j int) bool {
		return changes[i].FullName() < changes[j].FullName()
	})
	return &Comparison{Changes: changes}
}

const pathSep = "\x00"

func collect(n *Node, prefix []string, out map[string]*Node, paths map[string][]string) {
	for _, c := range n.Children {
		path := append(append([]string{}, prefix...), c.Name)
		key := strings.Join(path, pathSep)
		out[key] = c
		if paths != nil {
			paths[key] = append([]string{}, path...)
		}
		collect(c, path, out, paths)
	}
}

// anchorLeafMap 建立 样本ID -> 该样本作为采样叶子所在节点 的映射。
// 同一样本在一棵树中只有一个叶子位置；若某 ID 在多个节点都有叶子
// 权重（不应发生），该锚点不可靠，不放入映射。
func anchorLeafMap(t *Tree) map[string]string {
	out := map[string]string{}
	doubt := map[string]bool{}
	var walk func(n *Node)
	walk = func(n *Node) {
		for id := range n.selfThrough {
			if _, exists := out[id]; exists {
				doubt[id] = true
			}
			out[id] = n.keyPath
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(t.Root)
	for id := range doubt {
		delete(out, id)
	}
	return out
}

// anchorFallback 在路径无法直接对应时，用旧节点上的叶子样本锚点
// 唯一定位新节点。要求：旧节点至少有一个叶子锚点，且这些锚点在新树
// 中全部落到同一个叶子节点——这样对应才是可靠的。
func anchorFallback(oldNode *Node, newAnchor map[string]string) (string, bool) {
	if len(oldNode.selfThrough) == 0 {
		return "", false
	}
	var target string
	for id := range oldNode.selfThrough {
		n, ok := newAnchor[id]
		if !ok {
			return "", false // 有样本在新版本消失，无法可靠对应
		}
		if target == "" {
			target = n
		} else if target != n {
			return "", false // 锚点散落到多个新节点，不唯一
		}
	}
	return target, true
}

// Shared 只返回两次运行都存在的相同调用路径上的变化。
func (cmp *Comparison) Shared() []PathChange {
	var out []PathChange
	for _, c := range cmp.Changes {
		if c.Matched {
			out = append(out, c)
		}
	}
	return out
}

// ByKind 返回指定差异类型的路径变化。
func (cmp *Comparison) ByKind(kind ChangeKind) []PathChange {
	var out []PathChange
	for _, c := range cmp.Changes {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// Unmatched 返回无法可靠对应的路径（含仅一侧出现的路径）。
func (cmp *Comparison) Unmatched() []PathChange {
	return cmp.ByKind(KindUnmatched)
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
