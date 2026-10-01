package perfsample

// FrameInfo 是一个原始栈帧经符号资料解释后得到的逻辑帧信息。
// 当符号资料补全了缺失符号，或还原了内联函数时，一个原始地址可以
// 解释成多个逻辑帧。
type FrameInfo struct {
	Module      string
	BuildID     string
	Symbol      string
	InlineLevel int
	// Missing 为 true 时表示该地址仍无法解析，Symbol 可留空。
	Missing bool
	// Uncertain 为 true 时表示归属仍无法唯一确认；该帧按原地址保留，
	// 不参与跨版本折叠。
	Uncertain bool
}

// ResolveContext 携带解析一帧时所需的采样上下文。
// Time 是该样本的采集时刻，解析器据此查询模块装载/卸载关系。
type ResolveContext struct {
	Time     uint64
	SampleID string
	FrameIdx int
}

// ContextSymbolResolver 在 SymbolResolver 的基础上提供采样时刻，
// 使解析器能够按当时的模块关系解释地址。Reinterpret 会优先使用
// 该接口；只实现 SymbolResolver 的旧解析器仍然完全兼容。
type ContextSymbolResolver interface {
	ResolveFrame(ctx ResolveContext, f Frame) []FrameInfo
}

// SymbolResolver 按当前符号资料解释一个原始帧。
//
// 返回的切片按调用方向排列（外层在前、采样点在后），最后一个元素是
// 物理函数（InlineLevel=0），其前面的元素是补回的内联函数层次
// （InlineLevel>=1，最靠近物理帧的一层为 1）。
// 返回 nil 或空切片表示保持原帧不变。
//
// 解析器只能改变帧的解释，永远接触不到样本权重：原始地址与
// Weight/Provenance/ID 在 Reinterpret 中原样保留。
type SymbolResolver interface {
	Resolve(f Frame) []FrameInfo
}

// ResolverFunc 让普通函数满足 SymbolResolver。
type ResolverFunc func(Frame) []FrameInfo

// Resolve 调用自身。
func (fn ResolverFunc) Resolve(f Frame) []FrameInfo { return fn(f) }

// Reinterpret 用新的符号资料重新解释同一批原始样本，返回新批次。
//
// 不变量：
//   - 样本数量、顺序、ID、Weight、Provenance 完全不变；
//   - 每个原始帧的 Addr 保留在其解释出的所有逻辑帧上；
//   - 只改变符号缺失->已解析、函数改名、内联层次展开等“解释”；
//   - 不新增也不丢失任何权重：新批次总权重与原批次严格相等。
func Reinterpret(samples []Sample, resolver SymbolResolver) []Sample {
	if resolver == nil {
		return samples
	}
	out := make([]Sample, len(samples))
	for i, s := range samples {
		rs := s
		if len(s.Stack) > 0 {
			rs.Stack = make([]Frame, 0, len(s.Stack))
			for fi, f := range s.Stack {
				var infos []FrameInfo
				if cr, ok := resolver.(ContextSymbolResolver); ok {
					infos = cr.ResolveFrame(ResolveContext{
						Time:     s.Time,
						SampleID: s.ID,
						FrameIdx: fi,
					}, f)
				} else {
					infos = resolver.Resolve(f)
				}
				if len(infos) == 0 {
					kept := f
					kept.observedAt = s.Time
					rs.Stack = append(rs.Stack, kept)
					continue
				}
				for _, info := range infos {
					rs.Stack = append(rs.Stack, Frame{
						Symbol:      info.Symbol,
						Addr:        f.Addr, // 原始地址始终保留
						Missing:     info.Missing,
						Uncertain:   info.Uncertain,
						Module:      info.Module,
						BuildID:     info.BuildID,
						InlineLevel: info.InlineLevel,
						observedAt:  s.Time,
					})
				}
			}
		}
		out[i] = rs
	}
	return out
}
