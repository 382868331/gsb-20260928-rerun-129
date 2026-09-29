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
}

func (s Sample) weight() float64 {
	if s.Weight <= 0 {
		return 1
	}
	return s.Weight
}

// frameKey 返回该层在执行树中的归并键。
// 已解析符号按符号名归并（跨符号版本仍落在同一路径上，版本信息
// 记录在节点的来源明细中）；缺失符号按地址区分，避免把不同的
// 未知函数错误折叠。
func frameKey(f Frame) string {
	if f.Missing || f.Symbol == "" {
		if f.Addr != 0 {
			return unknownPrefix + addrHex(f.Addr) + ")"
		}
		return unknownName
	}
	return f.Symbol
}

const (
	unknownName   = "(unknown)"
	unknownPrefix = "(unknown@0x"
)
