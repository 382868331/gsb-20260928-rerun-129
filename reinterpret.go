package perfsample

// FrameInfo 是一个原始栈帧经符号资料解释后得到的逻辑帧信息。
// 当符号资料补全了缺失符号，或还原了内联函数时，一个原始地址可以
// 解释成多个逻辑帧。
type FrameInfo struct {
	Module      string
	Symbol      string
	InlineLevel int
	// Generation 为该帧所属模块的装载代次；留空（0）时由时间感知的
	// 重解释根据采样时刻自动补上。
	Generation int
	// Missing 为 true 时表示该地址仍无法解析，Symbol 可留空。
	Missing bool
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
//   - 样本数量、顺序、ID、Weight、Provenance、Time 完全不变；
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
			for _, f := range s.Stack {
				infos := resolver.Resolve(f)
				if len(infos) == 0 {
					rs.Stack = append(rs.Stack, f)
					continue
				}
				for _, info := range infos {
					rs.Stack = append(rs.Stack, Frame{
						Symbol:      info.Symbol,
						Addr:        f.Addr, // 原始地址始终保留
						Missing:     info.Missing,
						Module:      info.Module,
						InlineLevel: info.InlineLevel,
						Generation:  info.Generation,
					})
				}
			}
		}
		out[i] = rs
	}
	return out
}
