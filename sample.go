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
	// Module 是该帧所属模块（共享库/二进制）名。同名函数在不同模块中
	// 是不同身份，不会因为 Symbol 相同而被折叠。空字符串表示未指定模块，
	// 此时行为与第一版一致（只按符号名归并）。
	Module string
	// InlineLevel 描述内联层次：0 表示有真实栈帧的物理函数，
	// >=1 表示符号资料补全后还原出的内联函数（1 为最靠近物理帧的一层，
	// 数字越大越靠近调用方）。不同内联层次是不同身份，递归 f->f
	// 仍各自保留。
	InlineLevel int
	// Generation 是该帧所属模块在进程内的装载代次：模块第一次加载为 1，
	// 卸载后在同一（或不同）地址重新加载则代次递增。同一地址在不同代次
	// 属于不同代码，必须按 (模块, 代次, 符号/地址, 内联层次) 区分身份，
	// 不能只按地址把两个版本折叠成一个函数。0 表示未跟踪代次（旧行为）。
	Generation int
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
	// Time 是该样本被采集到的时刻（单调时钟单位由采集方定义，通常为纳秒）。
	// 0 表示时刻未知。重新解释只改帧解释，时刻与原始地址、权重一样原样保留，
	// 因此“相同地址在不同时间属于不同模块”的样本可以按采样当时的装载关系区分。
	Time uint64
}

func (s Sample) weight() float64 {
	if s.Weight <= 0 {
		return 1
	}
	return s.Weight
}

// frameKey 返回该层在执行树中的归并键。身份由
// (模块, 装载代次, 符号/地址, 内联层次) 共同决定：
//   - 已解析符号：同名函数位于不同模块、不同代次或不同内联层次时分别保留身份；
//   - 缺失符号：按 (代次, 地址) 区分，避免把卸载后重新加载的不同代码
//     或不同的未知函数错误折叠；
//   - 未指定模块/代次/内联层次的旧样本键退化为纯符号名，与第一版兼容。
//
// 跨符号版本仍可能落在同一键上（符号名不变的情形），版本信息记录在
// 节点的来源明细中；符号改名/移动由跨版本映射负责，而不是在这里折叠。
func frameKey(f Frame) string {
	if f.Missing || f.Symbol == "" {
		if f.Addr != 0 {
			return frameIdentityGen(f.Module, f.Generation, unknownPrefix+addrHex(f.Addr)+")", f.InlineLevel)
		}
		return frameIdentityGen(f.Module, f.Generation, unknownName, f.InlineLevel)
	}
	return frameIdentityGen(f.Module, f.Generation, f.Symbol, f.InlineLevel)
}

// frameIdentity 用与 pathSep 不同的分隔符拼出稳定的帧身份键。
// 仅在确实携带额外身份（模块/内联层次）时加限定，保证旧样本
// （Module 为空、InlineLevel 为 0、Generation 为 0）生成的键与第一版完全一致。
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

// frameIdentityGen 在帧身份键中追加装载代次限定。仅当代次 > 0 时追加，
// 保证未跟踪代次的旧样本键与之前版本完全一致。
func frameIdentityGen(module string, generation int, symbol string, inlineLevel int) string {
	base := frameIdentity(module, symbol, inlineLevel)
	if generation > 0 {
		base += genSep + itoa(generation)
	}
	return base
}

const (
	unknownName   = "(unknown)"
	unknownPrefix = "(unknown@0x"
	moduleSep     = "\x01:"
	inlineSep     = "\x01i"
	genSep        = "\x01g"
)

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
