package perfsample

import (
	"encoding/json"
	"fmt"
	"sort"
)

// OffsetRange 描述某来源时钟相对参考时间线的已知偏移范围（含端点）。
// 多个采样器记录同一程序时，时钟之间往往不是精确对齐的，只承诺真实
// 偏移落在 [Min, Max] 内。单位与 Sample.Time 相同。
type OffsetRange struct {
	Min int64 `json:"min"`
	Max int64 `json:"max"`
}

// Contains 报告 o 是否完全落在 r 内。
func (r OffsetRange) Contains(o OffsetRange) bool {
	return o.Min >= r.Min && o.Max <= r.Max
}

// Track 是一个采样来源的原始样本与其时钟偏移范围。
// 样本永远保留来源自己的采样时刻与覆盖范围；对齐只发生在视图层，
// 原始数据不被修改。
type Track struct {
	Source  string
	Samples []Sample
	// Offset 是该来源声明的已知偏移范围：来源时钟 + Offset 才是参考
	// 时间线。对齐判断只能在这个范围内收紧，不能超出。
	Offset OffsetRange
}

// Coverage 返回该来源在自身时钟下的采样覆盖范围 [start, end]。
// ok 为 false 表示该来源没有任何样本（无覆盖）。
func (t Track) Coverage() (start, end uint64, ok bool) {
	return coverageOf(t.Samples)
}

func coverageOf(samples []Sample) (start, end uint64, ok bool) {
	first := true
	for _, s := range samples {
		if first || s.Time < start {
			start = s.Time
		}
		if first || s.Time > end {
			end = s.Time
		}
		first = false
	}
	return start, end, !first
}

// AttributionKind 是一帧在当前对齐判断下的归属结论类别。
type AttributionKind int

const (
	// AttrResolved 归属确定：在可行的整个偏移范围内，该帧地址只会
	// 属于同一个装载区间。
	AttrResolved AttributionKind = iota
	// AttrAmbiguous 归属不定：偏移范围内先后无法确定，该帧可能属于
	// 多次装载/卸载中的不止一次。必须明确标示，不能强行归到某一次
	// 模块装卸。
	AttrAmbiguous
	// AttrUnattributed 在任何可行偏移下，该帧地址都不属于已登记的
	// 模块装载区间。
	AttrUnattributed
)

// String 返回归属类别的稳定描述。
func (k AttributionKind) String() string {
	switch k {
	case AttrAmbiguous:
		return "ambiguous"
	case AttrUnattributed:
		return "unattributed"
	default:
		return "resolved"
	}
}

// FrameAttribution 是一帧在当前对齐判断下的归属结论。
type FrameAttribution struct {
	Kind AttributionKind
	// Module/Generation 仅在 Kind 为 AttrResolved 时有效。
	Module     string
	Generation int
	// Candidates 在 Kind 为 AttrAmbiguous 时列出所有可能的装载区间，
	// 供展示“无法确定先后”的具体原因。
	Candidates []ModuleSpan
}

// AlignedSample 是一条样本在联合视图中的对齐结果：原始样本（含来源
// 自己的采样时刻）原样保留，另附对齐到参考时间线的时刻范围与逐帧
// 归属结论。
type AlignedSample struct {
	Sample Sample
	// RefMin/RefMax 是样本时刻按当前对齐判断映射到参考时间线的区间。
	RefMin, RefMax int64
	// Frames 与 Sample.Stack 一一对应。
	Frames []FrameAttribution
}

// AlignedTrack 是一个来源在联合视图中的轨道：声明的偏移范围、当前
// 采用的对齐判断、自身时钟下的覆盖范围，以及逐样本的对齐结果。
type AlignedTrack struct {
	Source    string
	Declared  OffsetRange
	Alignment OffsetRange
	// CoverageStart/CoverageEnd 是该来源自身时钟下的采样覆盖范围。
	CoverageStart, CoverageEnd uint64
	HasCoverage                bool
	Samples                    []AlignedSample
}

// RefCoverage 返回该轨道覆盖范围映射到参考时间线的区间。
func (t AlignedTrack) RefCoverage() (min, max int64, ok bool) {
	if !t.HasCoverage {
		return 0, 0, false
	}
	return int64(t.CoverageStart) + t.Alignment.Min,
		int64(t.CoverageEnd) + t.Alignment.Max, true
}

// JointView 是多个采样来源在同一参考时间线下的联合分析视图。
// 各来源保留自己的采样时刻与覆盖范围；只有在确实能够对应的区间
// 才比较热点，归属不定的位置被明确标示。
type JointView struct {
	Label   string
	Tracks  []AlignedTrack
	History *ModuleHistory
	// Notes 记录构建该视图时应用的资料（如 "symbols:<label>" 符号更新）。
	Notes []string
}

// NewJointView 用各来源声明的偏移范围直接构建联合视图。
func NewJointView(label string, tracks []Track, history *ModuleHistory) *JointView {
	return newJointView(label, tracks, nil, nil, history, nil)
}

func newJointView(label string, tracks []Track, interpreted map[string][]Sample,
	align map[string]OffsetRange, history *ModuleHistory, notes []string) *JointView {
	v := &JointView{Label: label, History: history, Notes: append([]string{}, notes...)}
	for _, t := range tracks {
		at := AlignedTrack{Source: t.Source, Declared: t.Offset, Alignment: t.Offset}
		if al, ok := align[t.Source]; ok {
			at.Alignment = al
		}
		samples := t.Samples
		if re, ok := interpreted[t.Source]; ok {
			samples = re
		}
		at.CoverageStart, at.CoverageEnd, at.HasCoverage = coverageOf(samples)
		for _, s := range samples {
			as := AlignedSample{
				Sample: s,
				RefMin: int64(s.Time) + at.Alignment.Min,
				RefMax: int64(s.Time) + at.Alignment.Max,
			}
			if len(s.Stack) > 0 {
				as.Frames = make([]FrameAttribution, len(s.Stack))
				for i, f := range s.Stack {
					as.Frames[i] = attributeFrame(f, as.RefMin, as.RefMax, history)
				}
			}
			at.Samples = append(at.Samples, as)
		}
		v.Tracks = append(v.Tracks, at)
	}
	return v
}

// attributeFrame 在参考时刻区间 [tMin, tMax] 下判定一帧的模块归属。
// 没有模块历史时保持帧自身的模块/代次身份（与旧行为一致）。
func attributeFrame(f Frame, tMin, tMax int64, h *ModuleHistory) FrameAttribution {
	if h == nil {
		return FrameAttribution{Kind: AttrResolved, Module: f.Module, Generation: f.Generation}
	}
	spans, full := h.LookupRange(f.Addr, tMin, tMax)
	switch {
	case full:
		return FrameAttribution{Kind: AttrResolved, Module: spans[0].Module, Generation: spans[0].Generation}
	case len(spans) > 0:
		return FrameAttribution{Kind: AttrAmbiguous, Candidates: spans}
	default:
		return FrameAttribution{Kind: AttrUnattributed}
	}
}

// TotalWeight 返回视图中全部样本的权重之和（与归属结论无关，永不丢样本）。
func (v *JointView) TotalWeight() float64 {
	var total float64
	for _, tr := range v.Tracks {
		for _, as := range tr.Samples {
			total += as.Sample.weight()
		}
	}
	return total
}

// CoverageHull 返回视图在参考时间线下的覆盖外框（各轨道覆盖的并集）。
// ok 为 false 表示整个视图没有任何样本覆盖。
func (v *JointView) CoverageHull() (min, max int64, ok bool) {
	first := true
	for _, tr := range v.Tracks {
		lo, hi, has := tr.RefCoverage()
		if !has {
			continue
		}
		if first || lo < min {
			min = lo
		}
		if first || hi > max {
			max = hi
		}
		first = false
	}
	return min, max, !first
}

// Hotspot 是一个归属确定的热点：按叶子帧（采样点）聚合的自身权重。
type Hotspot struct {
	Key        string // 内部身份键（模块/代次/符号/内联层次）
	Name       string // 展示名
	SelfWeight float64
	Samples    int64
}

type hotAgg struct {
	w     float64
	count int64
}

// leafAttribution 返回样本叶子帧的归属结论；无栈样本返回 false。
func leafAttribution(as AlignedSample) (FrameAttribution, bool) {
	if len(as.Sample.Stack) == 0 {
		return FrameAttribution{}, false
	}
	return as.Frames[len(as.Sample.Stack)-1], true
}

// resolvedLeafKey 计算归属确定样本的叶子帧身份键。
func resolvedLeafKey(as AlignedSample, attr FrameAttribution) string {
	f := as.Sample.Stack[len(as.Sample.Stack)-1]
	f.Module = attr.Module
	f.Generation = attr.Generation
	return frameKey(f)
}

// Hotspots 返回视图中归属确定的热点，按自身权重降序。
// 归属不定（AttrAmbiguous）与无法归属（AttrUnattributed）的权重
// 不进入热点，分别由 Ambiguous/UnattributedWeight 单列。
func (v *JointView) Hotspots() []Hotspot {
	byKey := map[string]*hotAgg{}
	for _, tr := range v.Tracks {
		for _, as := range tr.Samples {
			attr, ok := leafAttribution(as)
			if !ok || attr.Kind != AttrResolved {
				continue
			}
			key := resolvedLeafKey(as, attr)
			a := byKey[key]
			if a == nil {
				a = &hotAgg{}
				byKey[key] = a
			}
			a.w += as.Sample.weight()
			a.count++
		}
	}
	out := make([]Hotspot, 0, len(byKey))
	for key, a := range byKey {
		out = append(out, Hotspot{Key: key, Name: DisplayKey(key), SelfWeight: a.w, Samples: a.count})
	}
	sort.Slice(out, func(i, j int) bool {
		if !approxEq(out[i].SelfWeight, out[j].SelfWeight) {
			return out[i].SelfWeight > out[j].SelfWeight
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// AmbiguousHotspot 是一笔归属不定的权重：无法确定先后，可能属于
// 多次模块装卸中的不止一次。它被明确标示并单列，绝不强行归到某个
// 装载代次，也不计入任何热点。
type AmbiguousHotspot struct {
	Source     string
	Name       string // 当前解释下的帧展示名
	SelfWeight float64
	Candidates []ModuleSpan
}

type ambAgg struct {
	source string
	name   string
	w      float64
	cands  []ModuleSpan
}

// Ambiguous 返回视图中全部归属不定的热点权重，按来源与帧名排序。
func (v *JointView) Ambiguous() []AmbiguousHotspot {
	byKey := map[string]*ambAgg{}
	var order []string
	for _, tr := range v.Tracks {
		for _, as := range tr.Samples {
			attr, ok := leafAttribution(as)
			if !ok || attr.Kind != AttrAmbiguous {
				continue
			}
			f := as.Sample.Stack[len(as.Sample.Stack)-1]
			key := tr.Source + "/" + frameKey(f)
			a := byKey[key]
			if a == nil {
				a = &ambAgg{source: tr.Source, name: DisplayKey(frameKey(f)), cands: attr.Candidates}
				byKey[key] = a
				order = append(order, key)
			}
			a.w += as.Sample.weight()
		}
	}
	out := make([]AmbiguousHotspot, 0, len(byKey))
	for _, key := range order {
		a := byKey[key]
		out = append(out, AmbiguousHotspot{
			Source:     a.source,
			Name:       a.name,
			SelfWeight: a.w,
			Candidates: a.cands,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// UnattributedWeight 返回在任何可行偏移下都无法归属到已知模块的权重。
func (v *JointView) UnattributedWeight() float64 {
	var w float64
	for _, tr := range v.Tracks {
		for _, as := range tr.Samples {
			attr, ok := leafAttribution(as)
			if ok && attr.Kind == AttrUnattributed {
				w += as.Sample.weight()
			}
		}
	}
	return w
}

// AmbiguousWeight 返回视图中归属不定的权重总额。
func (v *JointView) AmbiguousWeight() float64 {
	var w float64
	for _, tr := range v.Tracks {
		for _, as := range tr.Samples {
			attr, ok := leafAttribution(as)
			if ok && attr.Kind == AttrAmbiguous {
				w += as.Sample.weight()
			}
		}
	}
	return w
}

// HotspotDelta 是一个热点在两段运行之间的对比结果。
// 只有在双方都有采样覆盖的对齐区间内，权重差才是真实变化；
// 仅一侧有观察的区间单列，缺少观察绝不能当成零开销。
type HotspotDelta struct {
	Key  string
	Name string
	// WeightA/WeightB 是双方都有观察的区间内各自的自身权重。
	WeightA, WeightB float64
	// Delta 是真实权重变化（B - A），只在共同覆盖区间内统计。
	Delta float64
	// OnlyA 是 A 有观察、B 缺少观察的区间内该热点的权重；
	// OnlyB 反之。它们是采样覆盖差异，不是权重变化。
	OnlyA, OnlyB float64
}

// RunComparison 是两段运行（两个联合视图）的对比报告。
// 它把采样覆盖不同与真实权重变化分开，并同时展示模块地址复用、
// 符号更新与部分来源缺失。
type RunComparison struct {
	Hotspots []HotspotDelta

	// CoverageOnlyA/CoverageOnlyB 是仅一侧有观察的区间内的归属确定
	// 权重总额（对端缺少观察，不能当成对端零开销）。
	CoverageOnlyA, CoverageOnlyB float64
	// AmbiguousA/AmbiguousB 是归属不定（对齐先后无法确定）的权重。
	AmbiguousA, AmbiguousB float64
	// UnattributedA/UnattributedB 是无法归属到已知模块的权重。
	UnattributedA, UnattributedB float64
	TotalA, TotalB               float64

	// MissingInA/MissingInB 列出只存在于对端的来源（部分来源缺失）。
	MissingInA, MissingInB []string
	// ModuleReuse 是模块历史中的地址复用事件（同地址不同代次）。
	ModuleReuse []ReuseEvent
	// SymbolUpdates 是构建两个视图时应用过的符号更新记录。
	SymbolUpdates []string

	// Balanced 为 false 说明归类漏记：各类别权重之和不等于总权重。
	Balanced bool
}

// sideStat 是一侧视图在“对端覆盖外框”口径下的权重拆分。
type sideStat struct {
	covered      map[string]float64
	only         map[string]float64
	ambiguous    float64
	unattributed float64
	total        float64
	coveredSum   float64
	onlySum      float64
}

// splitByCoverage 把视图权重按“对端是否有观察”拆成可比较与仅本侧
// 观察两部分。对端覆盖外框之外的本侧样本属于覆盖差异，不计入差值。
func (v *JointView) splitByCoverage(oMin, oMax int64, oHas bool) *sideStat {
	st := &sideStat{covered: map[string]float64{}, only: map[string]float64{}}
	for _, tr := range v.Tracks {
		for _, as := range tr.Samples {
			w := as.Sample.weight()
			st.total += w
			attr, ok := leafAttribution(as)
			if !ok {
				continue
			}
			switch attr.Kind {
			case AttrAmbiguous:
				st.ambiguous += w
			case AttrUnattributed:
				st.unattributed += w
			default:
				key := resolvedLeafKey(as, attr)
				if oHas && as.RefMin <= oMax && as.RefMax >= oMin {
					st.covered[key] += w
					st.coveredSum += w
				} else {
					st.only[key] += w
					st.onlySum += w
				}
			}
		}
	}
	return st
}

// alignChange 记录一次对齐修改前的状态，用于精确撤回。
type alignChange struct {
	source string
	old    OffsetRange
}

// JointSession 管理多来源联合分析的当前视图：
//   - 原始样本（各来源自己的采样时刻与覆盖范围）永不被修改，始终可用；
//   - 对齐判断可按来源收紧，修改后立即看到热点归属的变化；
//   - 先前结论（上一个视图）保留，可与当前视图对照；
//   - 撤销一次对齐修改只恢复它影响的那个来源的归属；
//   - 视图状态可保存，重开后复现同一视图。
type JointSession struct {
	tracks  []Track
	history *ModuleHistory
	label   string

	align       map[string]OffsetRange
	interpreted map[string][]Sample
	notes       []string
	undoStack   []alignChange

	view *JointView
	prev *JointView
}

// NewJointSession 用各来源的原始样本与模块历史建立会话。
// 初始对齐判断等于各来源声明的已知偏移范围。
func NewJointSession(label string, tracks []Track, history *ModuleHistory) *JointSession {
	s := &JointSession{
		tracks:      tracks,
		history:     history,
		label:       label,
		align:       map[string]OffsetRange{},
		interpreted: map[string][]Sample{},
	}
	s.rebuild()
	return s
}

func (s *JointSession) rebuild() {
	v := newJointView(s.label, s.tracks, s.interpreted, s.align, s.history, s.notes)
	s.prev = s.view
	s.view = v
}

// View 返回当前联合视图。
func (s *JointSession) View() *JointView { return s.view }

// Previous 返回上一个视图（先前结论），供与当前视图对照；没有时返回 nil。
func (s *JointSession) Previous() *JointView { return s.prev }

// Tracks 返回各来源的原始轨道（原始样本与声明的偏移范围），始终可用。
func (s *JointSession) Tracks() []Track { return s.tracks }

// Alignment 返回某来源当前采用的对齐判断。
func (s *JointSession) Alignment(source string) (OffsetRange, bool) {
	for _, t := range s.tracks {
		if t.Source == source {
			if al, ok := s.align[source]; ok {
				return al, true
			}
			return t.Offset, true
		}
	}
	return OffsetRange{}, false
}

func (s *JointSession) findTrack(source string) *Track {
	for i := range s.tracks {
		if s.tracks[i].Source == source {
			return &s.tracks[i]
		}
	}
	return nil
}

// SetAlignment 收紧某来源的对齐判断（必须落在其声明的已知偏移范围内）。
// 修改后视图立即重建，可对照 Previous 查看热点归属的变化。
func (s *JointSession) SetAlignment(source string, r OffsetRange) error {
	tr := s.findTrack(source)
	if tr == nil {
		return fmt.Errorf("joint: unknown source %q", source)
	}
	if r.Min > r.Max {
		return fmt.Errorf("joint: invalid offset range [%d, %d]", r.Min, r.Max)
	}
	if !tr.Offset.Contains(r) {
		return fmt.Errorf("joint: offset range [%d, %d] outside declared [%d, %d] for %q",
			r.Min, r.Max, tr.Offset.Min, tr.Offset.Max, source)
	}
	old := tr.Offset
	if cur, ok := s.align[source]; ok {
		old = cur
	}
	s.undoStack = append(s.undoStack, alignChange{source: source, old: old})
	s.align[source] = r
	s.rebuild()
	return nil
}

// CanUndo 报告是否还有可撤回的对齐修改。
func (s *JointSession) CanUndo() bool { return len(s.undoStack) > 0 }

// Undo 撤回最近一次对齐修改，只恢复它影响的那个来源的归属；
// 其他来源的对齐判断与符号更新不受影响。没有可撤回的修改时返回 false。
func (s *JointSession) Undo() bool {
	n := len(s.undoStack)
	if n == 0 {
		return false
	}
	ch := s.undoStack[n-1]
	s.undoStack = s.undoStack[:n-1]
	tr := s.findTrack(ch.source)
	if tr != nil && ch.old == tr.Offset {
		delete(s.align, ch.source)
	} else {
		s.align[ch.source] = ch.old
	}
	s.rebuild()
	return true
}

// ApplySymbols 用新的符号资料重新解释各来源的当前样本（符号更新）。
// 原始样本不变，权重守恒由 Reinterpret/ReinterpretTimed 保证；
// 该更新记录在视图 Notes 中，随对比报告展示。
func (s *JointSession) ApplySymbols(label string, r Reinterpretation) {
	for _, t := range s.tracks {
		cur := s.interpreted[t.Source]
		if cur == nil {
			cur = t.Samples
		}
		var re []Sample
		switch {
		case r.History != nil:
			re = ReinterpretTimed(cur, r.History, r.Timed)
		case r.Resolver != nil:
			re = Reinterpret(cur, r.Resolver)
		default:
			re = cur
		}
		s.interpreted[t.Source] = re
	}
	s.notes = append(s.notes, "symbols:"+label)
	s.rebuild()
}

// jointState 是 JointSession 的可序列化状态：视图标签、各来源当前
// 对齐判断与符号更新记录。符号资料本身（解析器函数）不可序列化，
// 重开后若曾应用符号更新，需按相同顺序重新 ApplySymbols。
type jointState struct {
	Version int                    `json:"version"`
	Label   string                 `json:"label"`
	Align   map[string]OffsetRange `json:"align"`
	Notes   []string               `json:"notes"`
}

// Save 把当前会话状态序列化为 JSON，保存后重开可复现所选视图。
func (s *JointSession) Save() ([]byte, error) {
	return json.Marshal(jointState{
		Version: 1,
		Label:   s.label,
		Align:   s.align,
		Notes:   s.notes,
	})
}

// RestoreJointSession 用相同的原始轨道、模块历史与保存的状态重建会话，
// 复现保存时所选的视图（相同的对齐判断与归属结论）。
// 状态中的对齐判断若超出来源声明的已知范围则报错。
func RestoreJointSession(tracks []Track, history *ModuleHistory, data []byte) (*JointSession, error) {
	var st jointState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("joint: decode state: %w", err)
	}
	if st.Version != 1 {
		return nil, fmt.Errorf("joint: unsupported state version %d", st.Version)
	}
	s := &JointSession{
		tracks:      tracks,
		history:     history,
		label:       st.Label,
		align:       map[string]OffsetRange{},
		interpreted: map[string][]Sample{},
		notes:       append([]string{}, st.Notes...),
	}
	for src, r := range st.Align {
		tr := s.findTrack(src)
		if tr == nil {
			return nil, fmt.Errorf("joint: state references unknown source %q", src)
		}
		if r.Min > r.Max || !tr.Offset.Contains(r) {
			return nil, fmt.Errorf("joint: saved alignment for %q outside declared range", src)
		}
		s.align[src] = r
	}
	s.rebuild()
	return s, nil
}

// CompareRuns 对比两段运行。共同覆盖区间内的权重差是真实变化；
// 仅一侧覆盖的区间单列；归属不定与无法归属的权重各自单列。
func CompareRuns(a, b *JointView) *RunComparison {
	rc := &RunComparison{}
	aMin, aMax, aHas := a.CoverageHull()
	bMin, bMax, bHas := b.CoverageHull()
	sa := a.splitByCoverage(bMin, bMax, bHas)
	sb := b.splitByCoverage(aMin, aMax, aHas)

	keys := map[string]bool{}
	for k := range sa.covered {
		keys[k] = true
	}
	for k := range sa.only {
		keys[k] = true
	}
	for k := range sb.covered {
		keys[k] = true
	}
	for k := range sb.only {
		keys[k] = true
	}
	for k := range keys {
		d := HotspotDelta{
			Key:     k,
			Name:    DisplayKey(k),
			WeightA: sa.covered[k],
			WeightB: sb.covered[k],
			OnlyA:   sa.only[k],
			OnlyB:   sb.only[k],
		}
		d.Delta = d.WeightB - d.WeightA
		rc.Hotspots = append(rc.Hotspots, d)
	}
	sort.Slice(rc.Hotspots, func(i, j int) bool {
		return rc.Hotspots[i].Name < rc.Hotspots[j].Name
	})

	rc.CoverageOnlyA = sa.onlySum
	rc.CoverageOnlyB = sb.onlySum
	rc.AmbiguousA, rc.AmbiguousB = sa.ambiguous, sb.ambiguous
	rc.UnattributedA, rc.UnattributedB = sa.unattributed, sb.unattributed
	rc.TotalA, rc.TotalB = sa.total, sb.total

	srcA, srcB := map[string]bool{}, map[string]bool{}
	for _, tr := range a.Tracks {
		srcA[tr.Source] = true
	}
	for _, tr := range b.Tracks {
		srcB[tr.Source] = true
	}
	for s := range srcB {
		if !srcA[s] {
			rc.MissingInA = append(rc.MissingInA, s)
		}
	}
	for s := range srcA {
		if !srcB[s] {
			rc.MissingInB = append(rc.MissingInB, s)
		}
	}
	sort.Strings(rc.MissingInA)
	sort.Strings(rc.MissingInB)

	h := a.History
	if h == nil {
		h = b.History
	}
	rc.ModuleReuse = h.AddressReuse()

	seen := map[string]bool{}
	for _, notes := range [][]string{a.Notes, b.Notes} {
		for _, n := range notes {
			if !seen[n] {
				seen[n] = true
				rc.SymbolUpdates = append(rc.SymbolUpdates, n)
			}
		}
	}

	rc.Balanced = approxEq(sa.total, sa.coveredSum+sa.onlySum+sa.ambiguous+sa.unattributed+emptyStackWeight(a)) &&
		approxEq(sb.total, sb.coveredSum+sb.onlySum+sb.ambiguous+sb.unattributed+emptyStackWeight(b))
	return rc
}

// emptyStackWeight 统计无栈样本的权重（不参与归属与热点，但属于总权重）。
func emptyStackWeight(v *JointView) float64 {
	var w float64
	for _, tr := range v.Tracks {
		for _, as := range tr.Samples {
			if len(as.Sample.Stack) == 0 {
				w += as.Sample.weight()
			}
		}
	}
	return w
}
