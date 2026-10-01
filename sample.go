package perfsample

// Frame 描述调用栈中的一层。栈按调用方向排列：
// Stack[0] 是最外层（如 main），最后一层是采样点所在函数。
//
// Symbol 为解析后的符号名；Addr 是该层的原始地址。
// 当符号缺失时 Symbol 为空、Missing 为 true，分析器不会丢弃该样本，
// 而是用地址生成一个稳定的占位名字。
type Frame struct {
	Symbol  string
	Addr    uint64
	Missing bool
	// Uncertain 为 true 时表示：结合采样时刻的模块装载关系后，
	// 该地址的归属仍无法唯一确认（例如采样时刻没有任何已登记模块
	// 覆盖该地址，或同一时刻有多个模块声称占有该地址）。
	// 这样的帧按原地址单独保留，绝不与其它版本同地址的函数折叠，
	// 也不会被丢弃。
	Uncertain bool
	// observedAt 内部记录采样时刻，仅用于让归属存疑帧在不同时刻分开。
	observedAt uint64
	// Module 是该帧所属模块（共享库/二进制）名。同名函数在不同模块中
	// 是不同身份，不会因为 Symbol 相同而被折叠。空字符串表示未指定模块，
	// 此时行为与第一版一致（只按符号名归并）。
	Module string
	// BuildID 区分同一模块在采样期间卸载/重新装载后的不同构建。
	// 空表示未指定，身份退化为仅按 Module 归并。
	BuildID string
	// InlineLevel 描述内联层次：0 表示有真实栈帧的物理函数，
	// >=1 表示符号资料补全后还原出的内联函数（1 为最靠近物理帧的一层，
	// 数字越大越靠近调用方）。不同内联层次是不同身份，递归 f->f
	// 仍各自保留。
	InlineLevel int
}

// Provenance 记录样本的来源与符号版本，保证结果可追溯：
// 后续接入新的采集器时，旧批次与新批次可以合并到同一棵树，
// 同时每个节点仍能按来源/符号版本拆分权重。
type Provenance struct {
	// Source 标识采集来源，例如 "manual-v1"、"cpu-prof"。
	Source string
	// SymbolVersion 标识解析这些栈所用的符号版本（构建 ID/二进制版本）。
	SymbolVersion string
	// Batch 标识该样本所属批次。
	Batch string
}

// Sample 是一条人工或采集器产出的调用栈样本。
// Weight 为样本权重（采样次数/耗时），<=0 时按 1 处理。
type Sample struct {
	ID     string
	Stack  []Frame
	Weight float64
	Prov   Provenance
	// Time 是该样本被采集到的时刻（单位由调用方约定，建议纳秒）。
	// 重新解释时按这个时刻查模块装载/卸载关系：同一个地址在不同
	// 时刻可能属于不同模块，绝不允许只按地址跨时间折叠。
	// 零值表示调用方不提供时刻，此时时间感知解析器退化为
	// “任意时刻均可命中”的开放式匹配。
	Time uint64
}

func (s Sample) weight() float64 {
	if s.Weight <= 0 {
		return 1
	}
	return s.Weight
}

// frameKey 返回该层在执行树中的归并键。身份由
// (模块, 符号/地址, 内联层次) 共同决定：
//   - 已解析符号：同名函数位于不同模块或不同内联层次时分别保留身份；
//   - 缺失符号：按地址区分，避免把不同的未知函数错误折叠；
//   - 未指定模块（空）且无内联层次时，键退化为纯符号名，与第一版兼容。
//
// 跨符号版本仍可能落在同一键上（符号名不变的情形），版本信息记录在
// 节点的来源明细中；符号改名/移动由跨版本映射负责，而不是在这里折叠。
func frameKey(f Frame) string {
	if f.Missing || f.Symbol == "" {
		if f.Addr != 0 {
			name := unknownPrefix + addrHex(f.Addr) + ")"
			if f.Uncertain {
				// 归属存疑的帧不与“确定的未知函数”折叠，也不与
				// 另一版本同地址的代码折叠。
				name += uncertainSuffix + timeSep + itoa64(f.observedAt)
			}
			return frameIdentity(moduleIdentity(f.Module, f.BuildID), name, f.InlineLevel)
		}
		name := unknownName
		if f.Uncertain {
			name += uncertainSuffix + timeSep + itoa64(f.observedAt)
		}
		return frameIdentity(moduleIdentity(f.Module, f.BuildID), name, f.InlineLevel)
	}
	return frameIdentity(moduleIdentity(f.Module, f.BuildID), f.Symbol, f.InlineLevel)
}

// frameIdentity 用与 pathSep 不同的分隔符拼出稳定的帧身份键。
// 仅在确实携带额外身份（模块/内联层次）时加限定，保证旧样本
// （Module 为空、InlineLevel 为 0）生成的键与第一版完全一致。
func frameIdentity(module, symbol string, inlineLevel int) string {
	switch {
	case module != "" && inlineLevel > 0:
		return module + moduleSep + symbol + inlineSep + itoa(inlineLevel)
	case module != "":
		return module + moduleSep + symbol
	case inlineLevel > 0:
		return symbol + inlineSep + itoa(inlineLevel)
	default:
		return symbol
	}
}

const (
	unknownName     = "(unknown)"
	unknownPrefix   = "(unknown@0x"
	uncertainSuffix = "?unowned"
	moduleSep       = "\x01:"
	buildSep        = "\x01#"
	inlineSep       = "\x01i"
	timeSep         = "\x01t"
)

func itoa64(n uint64) string { return itoa(int(n)) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
