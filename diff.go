package perfsample

import "sort"

// SymbolDataDiff 描述同一批（或两批带对应关系的）样本在两份符号资料下
// 重新解释后的差异。关键约束：任何样本都只会出现在一条 DiffEntry 中，
// 未匹配/归属存疑的样本同样计入两侧权重合计，调用方不能通过只看
// Matched 来美化占比。
type SymbolDataDiff struct {
	Entries []DiffEntry

	OldTotal float64
	NewTotal float64
	OldCount int64
	NewCount int64
	// Conserved 表示两侧所有条目（含未匹配、存疑）权重合计严格相等。
	Conserved bool
}

// DiffKind 细分两份符号资料/两次解释之间的差异来源。
type DiffKind int

const (
	// DiffSame：归属函数和权重都未变化。
	DiffSame DiffKind = iota
	// DiffRename：原地址/采样时刻与模块构建相同，仅名称或内联关系修订。
	DiffRename
	// DiffWeight：对应到同一函数，但样本权重发生变化。
	DiffWeight
	// DiffAttribution：同一样本在两份资料下归到不同代码位置
	// （非单纯改名，例如符号边界/重定位后的归属变化），权重随样本
	// 完整迁移，不被丢弃。
	DiffAttribution
	// DiffUncertain：至少一侧结合时间化模块关系后仍无法确认归属。
	DiffUncertain
	// DiffOnlyOld / DiffOnlyNew：另一侧找不到对应样本。
	DiffOnlyOld
	DiffOnlyNew
)

// String 返回稳定的机器可读差异类型名。
func (k DiffKind) String() string {
	switch k {
	case DiffRename:
		return "rename"
	case DiffWeight:
		return "weight"
	case DiffAttribution:
		return "attribution"
	case DiffUncertain:
		return "uncertain"
	case DiffOnlyOld:
		return "only-old"
	case DiffOnlyNew:
		return "only-new"
	default:
		return "same"
	}
}

// DiffEntry 是一条样本（按 ID，缺 ID 时按批次内序号）在两份解释间的对应。
// OldLeaf/NewLeaf 与 Time 保存原始地址和采样时刻，保证后续仍可审计。
type DiffEntry struct {
	ID string

	OldPath []string
	NewPath []string
	OldLeaf Frame
	NewLeaf Frame

	OldWeight float64
	NewWeight float64
	Time      uint64
	Kind      DiffKind
}

// FullName 返回新视角路径；没有新视角时返回旧视角路径。
func (e DiffEntry) FullName() string {
	p := e.NewPath
	if len(p) == 0 {
		p = e.OldPath
	}
	display := make([]string, len(p))
	for i, key := range p {
		display[i] = DisplayKey(key)
	}
	return joinDisplay(display)
}

// CompareSymbolData 用两个解析器分别重新解释同一批原始样本，再比较
// 两份符号资料造成的差异。原始样本（地址、时刻、ID、权重）不被修改。
func CompareSymbolData(raw []Sample, oldResolver, newResolver SymbolResolver) *SymbolDataDiff {
	oldSamples := Reinterpret(raw, oldResolver)
	newSamples := Reinterpret(raw, newResolver)
	return CompareInterpretations(oldSamples, newSamples)
}

// CompareInterpretations 比较两批已解释样本。优先按非空 Sample.ID 对应；
// 若两侧都没有 ID，则按批次内序号一一对应。两侧序号数量不同时，
// 多余样本标为 only-old/only-new，权重仍完整计入。
func CompareInterpretations(oldSamples, newSamples []Sample) *SymbolDataDiff {
	newIndex := indexSamples(newSamples)

	entries := make([]DiffEntry, 0, len(oldSamples)+len(newSamples))
	consumedNew := map[int]bool{}

	for oi := range oldSamples {
		o := oldSamples[oi]
		ni, found := pairSample(o, oi, newSamples, newIndex)
		entry := DiffEntry{
			ID:        pairID(o, oi, found, newSamples, ni),
			OldPath:   pathKeys(o.Stack),
			OldLeaf:   leafFrame(o.Stack),
			OldWeight: o.weight(),
			Time:      o.Time,
		}
		if !found {
			entry.Kind = DiffOnlyOld
			entries = append(entries, entry)
			continue
		}
		consumedNew[ni] = true
		n := newSamples[ni]
		entry.NewPath = pathKeys(n.Stack)
		entry.NewLeaf = leafFrame(n.Stack)
		entry.NewWeight = n.weight()
		if n.Time != 0 {
			entry.Time = n.Time
		}
		entry.Kind = classifyEntry(o, n)
		entries = append(entries, entry)
	}

	for ni := range newSamples {
		if consumedNew[ni] {
			continue
		}
		n := newSamples[ni]
		entries = append(entries, DiffEntry{
			ID:        n.ID,
			NewPath:   pathKeys(n.Stack),
			NewLeaf:   leafFrame(n.Stack),
			NewWeight: n.weight(),
			Time:      n.Time,
			Kind:      DiffOnlyNew,
		})
	}

	diff := &SymbolDataDiff{Entries: entries, OldCount: int64(len(oldSamples)), NewCount: int64(len(newSamples))}
	for _, e := range entries {
		diff.OldTotal += e.OldWeight
		diff.NewTotal += e.NewWeight
	}
	diff.Conserved = diff.OldCount == diff.NewCount && approxEq(diff.OldTotal, diff.NewTotal)
	sort.SliceStable(diff.Entries, func(i, j int) bool {
		if diff.Entries[i].Kind != diff.Entries[j].Kind {
			return diff.Entries[i].Kind < diff.Entries[j].Kind
		}
		return diff.Entries[i].FullName() < diff.Entries[j].FullName()
	})
	return diff
}

func classifyEntry(oldSample, newSample Sample) DiffKind {
	o, n := leafFrame(oldSample.Stack), leafFrame(newSample.Stack)
	if o.Uncertain || n.Uncertain {
		return DiffUncertain
	}
	oldKey, newKey := frameKey(o), frameKey(n)
	switch {
	case !approxEq(oldSample.weight(), newSample.weight()):
		// 即使同时发生改名/移动，只要同一锚点权重变了，就是真实采样变化。
		return DiffWeight
	case oldKey == newKey:
		return DiffSame
	case sameRawOwnership(o, n):
		// 地址、时刻对应的模块构建一致：名称/内联层次变化是符号修订，
		// 而不是采样占比变化。
		return DiffRename
	default:
		return DiffAttribution
	}
}

// sameRawOwnership 判断两个叶子是否为“同一处机器代码的不同符号解释”：
// 原始地址与采样时刻相同。两侧都已确定模块构建时还要求构建身份相同；
// 一侧缺符号时模块信息未知，地址/时刻相同即视为符号补全或名称修订。
// 模块重定位后地址不同，或两侧解析到不同构建时，不作为改名。
func sameRawOwnership(a, b Frame) bool {
	if a.Addr != b.Addr || a.observedAt != b.observedAt {
		return false
	}
	if !a.Missing && !b.Missing && (a.Module != b.Module || a.BuildID != b.BuildID) {
		return false
	}
	return true
}

func pathKeys(frames []Frame) []string {
	out := make([]string, len(frames))
	for i, f := range frames {
		// 路径键只使用稳定身份；归属存疑帧的时刻身份在叶子分类
		// 中通过 observedAt 单独判断，避免展示路径依赖内部后缀。
		f.observedAt = 0
		out[i] = frameKey(f)
	}
	return out
}

func leafFrame(frames []Frame) Frame {
	if len(frames) == 0 {
		return Frame{Missing: true, Uncertain: true}
	}
	return frames[len(frames)-1]
}

func indexSamples(samples []Sample) map[string]int {
	ids := map[string]int{}
	for i, s := range samples {
		if s.ID != "" {
			ids[s.ID] = i
		}
	}
	return ids
}

func pairSample(old Sample, oldIndex int, newer []Sample, newByID map[string]int) (int, bool) {
	if old.ID != "" {
		i, ok := newByID[old.ID]
		return i, ok
	}
	if !anyID(newer) && oldIndex < len(newer) {
		return oldIndex, true
	}
	return 0, false
}

func pairID(old Sample, oldIndex int, found bool, newer []Sample, newIndex int) string {
	if old.ID != "" {
		return old.ID
	}
	if found {
		if id := newer[newIndex].ID; id != "" {
			return id
		}
		return ordinalID(newIndex)
	}
	return ordinalID(oldIndex)
}

func ordinalID(i int) string { return "#" + itoa(i) }

func anyID(samples []Sample) bool {
	for _, s := range samples {
		if s.ID != "" {
			return true
		}
	}
	return false
}

func joinDisplay(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += "/" + p
	}
	return out
}

// ByKind 返回指定差异类型的条目。
func (d *SymbolDataDiff) ByKind(kind DiffKind) []DiffEntry {
	var out []DiffEntry
	for _, e := range d.Entries {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// WeightSummary 是某类差异在两侧计入的权重。所有类型汇总后应满足
// old/new 总权重守恒（仅 only-old/only-new 可能只贡献一侧）。
type WeightSummary struct {
	Kind      DiffKind
	OldWeight float64
	NewWeight float64
	Count     int
}

// Summarize 按差异类型汇总权重；未匹配和存疑条目也在其中。
func (d *SymbolDataDiff) Summarize() []WeightSummary {
	m := map[DiffKind]*WeightSummary{}
	for _, e := range d.Entries {
		s := m[e.Kind]
		if s == nil {
			s = &WeightSummary{Kind: e.Kind}
			m[e.Kind] = s
		}
		s.OldWeight += e.OldWeight
		s.NewWeight += e.NewWeight
		s.Count++
	}
	out := make([]WeightSummary, 0, len(m))
	for _, s := range m {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// TreeFor 按指定一侧的解释样本建树，便于继续使用节点/路径视图。
func TreeFor(samples []Sample) *Tree {
	t := NewTree()
	t.AddBatch(samples)
	return t
}
