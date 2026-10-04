package perfsample

import "sort"

// ModuleSpan 描述一次模块装载：模块 Module 在进程时刻 [Start, End) 内
// 占据地址区间 [Base, Limit)。End 为 0 表示采样结束时仍处于装载状态。
//
// 同一模块卸载后重新加载（无论是否回到同一地址）用多个 Span 表示，
// Generation 区分不同代次：填 0 时按该模块在传入序列中的出现顺序
// 自动编号（第一次装载为 1）。显式填写的代次原样使用。
type ModuleSpan struct {
	Module     string
	Base       uint64
	Limit      uint64
	Start      uint64
	End        uint64
	Generation int
}

// ModuleHistory 是采样期间的模块装载/卸载关系。解释一个原始地址时必须
// 结合样本时刻落在哪个 Span，而不能只按当前（或任意一个）模块布局：
// 同一地址在不同时间可能属于不同模块的不同代码。
type ModuleHistory struct {
	spans []ModuleSpan
}

// NewModuleHistory 用一组装载区间构建历史。会为留空代次的区间按模块
// 自动编号，并按起始时刻排序，便于稳定查找。
func NewModuleHistory(spans ...ModuleSpan) *ModuleHistory {
	out := make([]ModuleSpan, len(spans))
	copy(out, spans)
	for i := range out {
		if out[i].Generation == 0 {
			// 未显式给代次时，按该模块在传入序列中的出现顺序编号：
			// 同一模块第一次加载为 1，卸载后重新加载递增。
			out[i].Generation = countSeen(out[:i], out[i].Module) + 1
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return &ModuleHistory{spans: out}
}

func countSeen(spans []ModuleSpan, module string) int {
	n := 0
	for i := range spans {
		if spans[i].Module == module {
			n++
		}
	}
	return n
}

// Lookup 返回采样时刻 t、地址 addr 当时所属的装载区间。
// ok 为 false 表示该时刻该地址不属于任何已登记模块（归属无法确认）；
// 同一时刻有多个区间重叠声称拥有该地址时同样返回 ok=false，
// 避免在有歧义的模块关系上强行归属。
func (h *ModuleHistory) Lookup(addr, t uint64) (span ModuleSpan, ok bool) {
	if h == nil {
		return ModuleSpan{}, false
	}
	var found *ModuleSpan
	for i := range h.spans {
		s := &h.spans[i]
		if addr < s.Base || (s.Limit != 0 && addr >= s.Limit) {
			continue
		}
		if t != 0 {
			if t < s.Start || (s.End != 0 && t >= s.End) {
				continue
			}
		}
		if found != nil {
			return ModuleSpan{}, false // 同一时刻多个候选，归属有歧义
		}
		found = s
	}
	if found == nil {
		return ModuleSpan{}, false
	}
	// 无时刻样本的额外保护：地址可能在不同时间落入多个区间时拒绝归属。
	if t == 0 {
		var hits int
		for i := range h.spans {
			s := &h.spans[i]
			if addr >= s.Base && (s.Limit == 0 || addr < s.Limit) {
				hits++
			}
		}
		if hits > 1 {
			return ModuleSpan{}, false
		}
	}
	return *found, true
}

// LookupRange 回答“采样时刻只已知落在参考时间线区间 [tMin, tMax] 内时，
// 地址 addr 可能属于哪些装载区间”。这用于多采样器联合分析：来源时钟
// 与参考时钟之间只有已知偏移范围，而不是精确对齐，因此一个样本的参考
// 时刻本身就是一个区间。
//
// 返回所有与该时刻区间相交、且地址落在其范围内的候选 Span（按起始时刻
// 排序）。full 为 true 当且仅当恰好只有一个候选、且它完整覆盖整个时刻
// 区间——只有此时归属才是确定的；多个候选（先后无法确定）或唯一候选
// 不能覆盖整个区间（区间内存在未登记的空隙）时 full 为 false，调用方
// 必须明确标示为归属不定，不能强行归到某一次装载。
func (h *ModuleHistory) LookupRange(addr uint64, tMin, tMax int64) (spans []ModuleSpan, full bool) {
	if h == nil {
		return nil, false
	}
	for i := range h.spans {
		s := &h.spans[i]
		if addr < s.Base || (s.Limit != 0 && addr >= s.Limit) {
			continue
		}
		start := int64(s.Start)
		end := int64(s.End)
		if s.End == 0 {
			end = int64(^uint64(0) >> 1) // 仍处装载状态：视为延伸到无穷远
		}
		if end < tMin || start > tMax {
			continue
		}
		spans = append(spans, *s)
	}
	if len(spans) == 1 {
		s := spans[0]
		start := int64(s.Start)
		end := int64(s.End)
		if s.End == 0 {
			end = int64(^uint64(0) >> 1)
		}
		full = start <= tMin && end >= tMax
	}
	return spans, full
}

// ReuseEvent 描述一段被多次装载复用的地址区间：同一地址范围在不同
// 时间属于不同模块/不同代次的代码。联合分析视图必须把这种复用展示
// 出来，否则同地址的样本会被误读成同一个函数。
type ReuseEvent struct {
	Base   uint64
	Limit  uint64
	Owners []ModuleSpan // 复用该地址区间的各次装载（不同模块或不同代次）
}

// AddressReuse 返回历史中所有被多次装载复用的地址区间。
// 按地址区间相交的传递闭包分组，组内存在不同 (模块, 代次) 身份时
// 记为一次复用事件；输出按基址排序，保证展示稳定。
func (h *ModuleHistory) AddressReuse() []ReuseEvent {
	if h == nil {
		return nil
	}
	spans := make([]ModuleSpan, len(h.spans))
	copy(spans, h.spans)
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].Base < spans[j].Base })

	var events []ReuseEvent
	for i := 0; i < len(spans); {
		// 以 spans[i] 为起点扩张地址相交的传递闭包。
		j := i + 1
		limit := spans[i].Limit
		for j < len(spans) && spans[j].Base < limit {
			if spans[j].Limit > limit {
				limit = spans[j].Limit
			}
			j++
		}
		group := spans[i:j]
		identities := map[string]bool{}
		for _, s := range group {
			identities[frameIdentityGen(s.Module, s.Generation, "", 0)] = true
		}
		if len(identities) > 1 {
			owners := make([]ModuleSpan, len(group))
			copy(owners, group)
			events = append(events, ReuseEvent{Base: group[0].Base, Limit: limit, Owners: owners})
		}
		i = j
	}
	return events
}

// TimedSymbolResolver 结合采样时刻与当时的模块装载关系解释原始帧。
// span 为该帧地址在时刻 t 命中的装载区间；返回约定与 SymbolResolver 相同：
// nil/空切片表示保持原帧（但仍会补上模块名与装载代次）。
type TimedSymbolResolver interface {
	ResolveAt(f Frame, t uint64, span ModuleSpan) []FrameInfo
}

// TimedResolverFunc 让普通函数满足 TimedSymbolResolver。
type TimedResolverFunc func(Frame, uint64, ModuleSpan) []FrameInfo

// ResolveAt 调用自身。
func (fn TimedResolverFunc) ResolveAt(f Frame, t uint64, span ModuleSpan) []FrameInfo {
	return fn(f, t, span)
}

// ReinterpretTimed 用“采样当时”的模块关系与符号资料重新解释样本。
//
// 与 Reinterpret 相同的不变量仍然成立：样本数量、顺序、ID、Weight、
// Provenance、Time 不变，每个原始 Addr 保留在解释出的全部逻辑帧上，
// 不新增也不丢失任何权重。区别在于：解析前先按 (地址, 时刻) 查模块
// 历史，只有当时确实装载在该地址的模块才参与解释；卸载/重载造成的
// 同地址不同代码通过 Generation 保持不同身份，绝不按地址混为一个函数。
// 解析器返回的 FrameInfo 若没填 Module/Generation，则自动补上当时区间的
// 模块名与代次；解析器为 nil 时只补模块关系、不改符号。
func ReinterpretTimed(samples []Sample, history *ModuleHistory, resolver TimedSymbolResolver) []Sample {
	out := make([]Sample, len(samples))
	for i, s := range samples {
		rs := s
		if len(s.Stack) > 0 {
			rs.Stack = make([]Frame, 0, len(s.Stack))
			for _, f := range s.Stack {
				span, hit := history.Lookup(f.Addr, s.Time)
				var infos []FrameInfo
				if hit && resolver != nil {
					infos = resolver.ResolveAt(f, s.Time, span)
				}
				if len(infos) == 0 {
					k := f
					// 命中装载区间时补全归属身份；未命中则原样保留
					// （占位键仍含原有代次/地址），归属问题留给差异报告。
					if hit {
						if k.Module == "" {
							k.Module = span.Module
						}
						if k.Generation == 0 {
							k.Generation = span.Generation
						}
					}
					rs.Stack = append(rs.Stack, k)
					continue
				}
				for _, info := range infos {
					nf := Frame{
						Symbol:      info.Symbol,
						Addr:        f.Addr, // 原始地址始终保留
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
			}
		}
		out[i] = rs
	}
	return out
}
