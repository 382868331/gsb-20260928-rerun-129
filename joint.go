package perfsample

import (
	"math/big"
	"sort"
	"strings"
)

// SourceTrack 保留一个采样器自己的本地时钟、样本与实际覆盖范围。
// OffsetMin/OffsetMax 是把本地时间映射到联合时间线的已知偏移范围：
// global = local + offset。两者相等表示精确对齐；不等表示先后仍有不确定性。
type SourceTrack struct {
	ID      string
	Name    string
	Samples []Sample
	// CoverageStart/CoverageEnd 是该来源确实观察到的本地时间区间 [Start,End)。
	// 留空时从带 Time 的样本推断；它与样本点分开保存，避免把没有样本的
	// 覆盖区间误当成零，也避免把样本范围之外当成已观察。
	CoverageStart uint64
	CoverageEnd   uint64
	OffsetMin     int64
	OffsetMax     int64
}

// JointRun 是同一次运行中多个采样器在联合时间线上的保守合并。
type JointRun struct {
	Name    string
	Sources map[string]*SourceTrack
}

// TimeInterval 是一个闭开时间区间 [Start,End)。
type TimeInterval struct {
	Start uint64
	End   uint64
}

// AlignmentStatus 描述某来源对齐判断的确定性。
type AlignmentStatus int

const (
	AlignmentUnknown AlignmentStatus = iota
	AlignmentExact
	AlignmentBounded
)

func NewSourceTrack(id string, samples []Sample, offsetMin, offsetMax int64) *SourceTrack {
	t := &SourceTrack{ID: id, Name: id, Samples: samples, OffsetMin: offsetMin, OffsetMax: offsetMax}
	var lo, hi uint64
	for _, s := range samples {
		if s.Time == 0 {
			continue
		}
		if lo == 0 || s.Time < lo {
			lo = s.Time
		}
		if s.Time > hi {
			hi = s.Time
		}
	}
	if lo != 0 {
		t.CoverageStart = lo
		t.CoverageEnd = hi + 1
	}
	return t
}

func NewJointRun(name string, tracks ...*SourceTrack) *JointRun {
	r := &JointRun{Name: name, Sources: map[string]*SourceTrack{}}
	for _, tr := range tracks {
		if tr != nil {
			r.Sources[tr.ID] = tr
		}
	}
	return r
}

func (t *SourceTrack) alignmentStatus() AlignmentStatus {
	if t == nil {
		return AlignmentUnknown
	}
	if t.OffsetMin == t.OffsetMax {
		return AlignmentExact
	}
	return AlignmentBounded
}

// Alignment 返回该来源当前对齐状态。
func (t *SourceTrack) Alignment() AlignmentStatus { return t.alignmentStatus() }

// String 返回对齐状态的稳定名称。
func (s AlignmentStatus) String() string {
	switch s {
	case AlignmentExact:
		return "exact"
	case AlignmentBounded:
		return "bounded"
	default:
		return "unknown"
	}
}

func shiftedTime(local uint64, offset int64) uint64 {
	v := int64(local) + offset
	if v < 0 {
		return 0
	}
	return uint64(v)
}

// GlobalSampleInterval 返回样本在联合时间线上的全部可能时刻。
func (t *SourceTrack) GlobalSampleInterval(s Sample) TimeInterval {
	if t == nil || s.Time == 0 {
		return TimeInterval{}
	}
	lo := shiftedTime(s.Time, t.OffsetMin)
	hi := shiftedTime(s.Time, t.OffsetMax)
	if lo > hi {
		lo, hi = hi, lo
	}
	if hi < ^uint64(0) {
		hi++
	}
	return TimeInterval{Start: lo, End: hi}
}

// GlobalCoverage 返回该来源确实覆盖区间在联合时间线上的最宽外框。
// 偏移不确定时，外框之外一定未观察；外框内部仍按来源自己的区间保守使用。
func (t *SourceTrack) GlobalCoverage() TimeInterval {
	if t == nil || t.CoverageStart == 0 && t.CoverageEnd == 0 {
		return TimeInterval{}
	}
	lo := shiftedTime(t.CoverageStart, t.OffsetMin)
	hi := shiftedTime(t.CoverageEnd, t.OffsetMax)
	if lo > hi {
		lo, hi = hi, lo
	}
	return TimeInterval{Start: lo, End: hi}
}

func sortedTracks(m map[string]*SourceTrack) []*SourceTrack {
	out := make([]*SourceTrack, 0, len(m))
	for _, t := range m {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// JointTimedResolver 结合来源、本地时间和唯一确定的模块区间解释帧。
type JointTimedResolver interface {
	ResolveJoint(f Frame, localTime uint64, track *SourceTrack, span ModuleSpan) []FrameInfo
}

type JointResolverFunc func(Frame, uint64, *SourceTrack, ModuleSpan) []FrameInfo

func (fn JointResolverFunc) ResolveJoint(f Frame, localTime uint64, track *SourceTrack, span ModuleSpan) []FrameInfo {
	return fn(f, localTime, track, span)
}

// ReinterpretJoint 在联合时间线上解释样本。偏移范围使某地址可能跨越多个
// 模块代次或装卸边界时，该帧标记为 time-uncertain，绝不归入其中任一 span。
func ReinterpretJoint(run *JointRun, history *ModuleHistory, resolver JointTimedResolver) []Sample {
	if run == nil {
		return nil
	}
	var out []Sample
	for _, tr := range sortedTracks(run.Sources) {
		for _, s := range tr.Samples {
			rs := s
			if len(s.Stack) > 0 {
				rs.Stack = make([]Frame, 0, len(s.Stack))
				for _, f := range s.Stack {
					if f.Addr != 0 {
						span, certain := jointSpan(history, f.Addr, tr.GlobalSampleInterval(s))
						if !certain {
							rs.Stack = append(rs.Stack, uncertainFrame(f))
							continue
						}
						var infos []FrameInfo
						if resolver != nil && (history == nil || span.Module != "") {
							infos = resolver.ResolveJoint(f, s.Time, tr, span)
						}
						if len(infos) == 0 {
							nf := f
							if nf.Module == "" {
								nf.Module = span.Module
							}
							if nf.Generation == 0 {
								nf.Generation = span.Generation
							}
							rs.Stack = append(rs.Stack, nf)
							continue
						}
						for _, info := range infos {
							nf := Frame{
								Symbol:      info.Symbol,
								Addr:        f.Addr,
								Missing:     info.Missing,
								Module:      info.Module,
								InlineLevel: info.InlineLevel,
								Generation:  info.Generation,
							}
							if nf.Module == "" {
								nf.Module = span.Module
							}
							if nf.Generation == 0 {
								nf.Generation = span.Generation
							}
							rs.Stack = append(rs.Stack, nf)
						}
					} else {
						rs.Stack = append(rs.Stack, f)
					}
				}
			}
			out = append(out, rs)
		}
	}
	return out
}

func uncertainFrame(f Frame) Frame {
	return Frame{Addr: f.Addr, Missing: true, Uncertain: true, Generation: 0}
}

func jointSpan(h *ModuleHistory, addr uint64, window TimeInterval) (ModuleSpan, bool) {
	if window.End == 0 || window.Start >= window.End {
		return ModuleSpan{}, false
	}
	if h == nil {
		return ModuleSpan{}, true
	}
	if window.End == window.Start+1 {
		return h.Lookup(addr, window.Start)
	}
	type spanID struct {
		module string
		gen    int
	}
	identities := map[spanID]bool{}
	want := new(big.Int).SetUint64(window.End - window.Start)
	covered := new(big.Int)
	for _, sp := range h.spans {
		if addr < sp.Base || (sp.Limit != 0 && addr >= sp.Limit) {
			continue
		}
		ov := intersectInterval(window, TimeInterval{Start: sp.Start, End: spanEnd(sp)})
		if ov.End <= ov.Start {
			continue
		}
		identities[spanID{sp.Module, sp.Generation}] = true
		covered.Add(covered, new(big.Int).SetUint64(ov.End-ov.Start))
	}
	if len(identities) == 0 && covered.Sign() == 0 {
		return ModuleSpan{}, true
	}
	if covered.Cmp(want) != 0 || len(identities) != 1 {
		return ModuleSpan{}, false
	}
	for _, sp := range h.spans {
		if identities[spanID{sp.Module, sp.Generation}] {
			return sp, true
		}
	}
	return ModuleSpan{}, false
}

func spanEnd(sp ModuleSpan) uint64 {
	if sp.End == 0 {
		return ^uint64(0)
	}
	return sp.End
}

func intersectInterval(a, b TimeInterval) TimeInterval {
	lo := a.Start
	if b.Start > lo {
		lo = b.Start
	}
	hi := a.End
	if b.End < hi {
		hi = b.End
	}
	if hi <= lo {
		return TimeInterval{}
	}
	return TimeInterval{Start: lo, End: hi}
}

// JointHotspot 是一条路径在两次联合运行中的权重。Observed* 保留各来源
// 全部已观察权重；Comparable* 只计入两侧同一来源、同一确定覆盖区间内
// 可可靠比较的权重，缺观察不会补零。
type JointHotspot struct {
	Path             []string
	OldObserved      float64
	NewObserved      float64
	OldComparable    float64
	NewComparable    float64
	DeltaComparable  float64
	Comparable       bool
	UncertainSamples int64
	OldSources       []string
	NewSources       []string
}

func (h JointHotspot) FullName() string {
	parts := make([]string, len(h.Path))
	for i, p := range h.Path {
		parts[i] = DisplayKey(p)
	}
	return strings.Join(parts, "/")
}

// JointComparison 区分真实权重变化和采样覆盖差异。
type JointComparison struct {
	Hotspots []JointHotspot
}

type jointLeafAgg struct {
	path      []string
	observed  float64
	uncertain int64
	bySource  map[string]map[TimeInterval]float64
}

// CompareJointRuns 比较两次联合运行。每个来源独立保留时钟与覆盖；只有
// 样本的完整可能时间区间落在另一侧同来源确实覆盖区间内时才计入 delta。
func CompareJointRuns(oldRun, newRun *JointRun) *JointComparison {
	old := collectJointLeaves(oldRun)
	newL := collectJointLeaves(newRun)
	keys := map[string]bool{}
	for k := range old {
		keys[k] = true
	}
	for k := range newL {
		keys[k] = true
	}
	out := make([]JointHotspot, 0, len(keys))
	for key := range keys {
		o, n := old[key], newL[key]
		var path []string
		switch {
		case o != nil:
			path = o.path
		case n != nil:
			path = n.path
		}
		var oc, nc, oo, no float64
		var unc int64
		if o != nil {
			oo, unc = o.observed, o.uncertain
		}
		if n != nil {
			no = n.observed
			unc += n.uncertain
		}
		if o != nil && n != nil {
			for source, oldIntervals := range o.bySource {
				newIntervals := n.bySource[source]
				for iv, ow := range oldIntervals {
					if nw, ok := newIntervals[iv]; ok {
						oc += ow
						nc += nw
					}
				}
			}
		}
		var oldSources, newSources []string
		if o != nil {
			for source := range o.bySource {
				oldSources = append(oldSources, source)
			}
			sort.Strings(oldSources)
		}
		if n != nil {
			for source := range n.bySource {
				newSources = append(newSources, source)
			}
			sort.Strings(newSources)
		}
		out = append(out, JointHotspot{
			Path:             path,
			OldObserved:      oo,
			NewObserved:      no,
			OldComparable:    oc,
			NewComparable:    nc,
			DeltaComparable:  nc - oc,
			Comparable:       oc > 0 || nc > 0,
			UncertainSamples: unc,
			OldSources:       oldSources,
			NewSources:       newSources,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FullName() < out[j].FullName() })
	return &JointComparison{Hotspots: out}
}

func collectJointLeaves(run *JointRun) map[string]*jointLeafAgg {
	out := map[string]*jointLeafAgg{}
	if run == nil {
		return out
	}
	for _, tr := range sortedTracks(run.Sources) {
		for _, s := range tr.Samples {
			if len(s.Stack) == 0 {
				continue
			}
			path := make([]string, len(s.Stack))
			for i, f := range s.Stack {
				path[i] = frameKey(f)
			}
			key := strings.Join(path, pathSep)
			ag := out[key]
			if ag == nil {
				ag = &jointLeafAgg{path: path, bySource: map[string]map[TimeInterval]float64{}}
				out[key] = ag
			}
			w := s.weight()
			ag.observed += w
			if s.Stack[len(s.Stack)-1].Uncertain {
				ag.uncertain++
			}
			iv := tr.GlobalSampleInterval(s)
			if iv.End > iv.Start {
				if ag.bySource[tr.ID] == nil {
					ag.bySource[tr.ID] = map[TimeInterval]float64{}
				}
				ag.bySource[tr.ID][iv] += w
			}
		}
	}
	return out
}
