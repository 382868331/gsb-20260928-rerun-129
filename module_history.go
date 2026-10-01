package perfsample

// ModuleSpan 描述一个模块（共享库/可执行文件）在采样时间轴上的一次装载。
// 同一个模块可以被卸载后重新装载：每次装载都是一条独立的 Span，
// 可以落在不同的基地址（重定位），也可以对应不同的构建（BuildID）。
//
// 地址区间为半开区间 [Base, End)；时间区间为半开区间
// [LoadAt, UnloadAt)，UnloadAt 为 0 表示采样结束时仍未卸载。
type ModuleSpan struct {
	Module   string
	BuildID  string
	Base     uint64
	End      uint64
	LoadAt   uint64
	UnloadAt uint64
}

// Contains 报告地址 addr 在时刻 t 是否落在本次装载区间内。
// t 为 0（调用方未提供采样时刻）时只校验地址区间，时间不参与判断。
func (s ModuleSpan) Contains(addr, t uint64) bool {
	if addr < s.Base || addr >= s.End {
		return false
	}
	if t == 0 {
		return true
	}
	if t < s.LoadAt {
		return false
	}
	if s.UnloadAt != 0 && t >= s.UnloadAt {
		return false
	}
	return true
}

// identity 返回“模块 + 构建”的身份标签：同一模块重新装载后即便
// 基地址相同，只要构建不同，也不会被折叠成一个函数。
func (s ModuleSpan) identity() string {
	return moduleIdentity(s.Module, s.BuildID)
}

// moduleIdentity 拼接模块名与构建标识。构建标识为空时退化为纯模块名，
// 与第二版行为兼容。
func moduleIdentity(module, buildID string) string {
	if buildID == "" {
		return module
	}
	return module + buildSep + buildID
}

// ModuleHistory 记录采样期间全部模块的装载/卸载/重新装载关系。
// 重新解释时必须按“地址 + 采样时刻”查询归属，禁止只按地址把
// 不同时间段、不同版本的代码混成一个函数。
type ModuleHistory struct {
	spans []ModuleSpan
}

// NewModuleHistory 创建模块装载时间线，并登记初始的若干次装载。
func NewModuleHistory(spans ...ModuleSpan) *ModuleHistory {
	h := &ModuleHistory{spans: make([]ModuleSpan, 0, len(spans))}
	h.spans = append(h.spans, spans...)
	return h
}

// Load 登记一次模块装载，返回该时间线本身便于链式调用。
func (h *ModuleHistory) Load(span ModuleSpan) *ModuleHistory {
	h.spans = append(h.spans, span)
	return h
}

// OwnersAt 返回采样时刻 t、地址 addr 同时所属的全部装载记录。
// 返回 0 条表示该时刻没有任何已登记模块覆盖此地址；返回多条表示
// 同一时刻存在归属冲突（例如装载区间登记重叠）。两种情况都意味着
// 归属无法唯一确认，调用方必须保留原地址并标注存疑。
func (h *ModuleHistory) OwnersAt(addr, t uint64) []ModuleSpan {
	if h == nil {
		return nil
	}
	var out []ModuleSpan
	for _, s := range h.spans {
		if s.Contains(addr, t) {
			out = append(out, s)
		}
	}
	return out
}

// SpanCount 返回已登记的装载次数（含同一模块的多次重新装载）。
func (h *ModuleHistory) SpanCount() int { return len(h.spans) }
