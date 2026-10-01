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
