package perfsample

import "sort"

// FuncSym 是一份符号资料中某个物理函数占用的地址区间（相对模块基址）。
// 区间为半开区间 [Start, End)。
type FuncSym struct {
	Name  string
	Start uint64
	End   uint64
}

// Contains 判断相对地址 off 是否落在函数区间内。
func (s FuncSym) Contains(off uint64) bool {
	return off >= s.Start && (s.End == 0 || off < s.End)
}

// InlineSite 描述采样相对地址上的一条内联调用记录。InlineLevel 与
// Frame.InlineLevel 口径一致：最靠近物理帧的一层为 1，数字越大越靠外。
type InlineSite struct {
	Name        string
	InlineLevel int
}

// ModuleSymbols 是某个“模块 + 构建”的一份符号资料。模块的重新装载若
// 使用不同构建，必须用不同的 ModuleSymbols，避免同地址跨版本混函数。
type ModuleSymbols struct {
	Module  string
	BuildID string
	Funcs   []FuncSym
	// InlineAt 把相对地址映射到该地址补回的内联层次（不含物理函数）。
	InlineAt map[uint64][]InlineSite
}

// SymbolProfile 是一整套可用于重新解释的符号资料（两份这样的资料
// 即可做差异比较）。键是 moduleIdentity(Module, BuildID)。
type SymbolProfile struct {
	modules map[string]*ModuleSymbols
}

// NewSymbolProfile 用若干份模块符号资料构建一份完整符号资料。
func NewSymbolProfile(mods ...ModuleSymbols) *SymbolProfile {
	p := &SymbolProfile{modules: map[string]*ModuleSymbols{}}
	for i := range mods {
		m := mods[i]
		p.modules[moduleIdentity(m.Module, m.BuildID)] = &m
	}
	return p
}

// Module 按模块名与构建标识取出对应符号资料。
func (p *SymbolProfile) Module(module, buildID string) (*ModuleSymbols, bool) {
	if p == nil {
		return nil, false
	}
	m, ok := p.modules[moduleIdentity(module, buildID)]
	return m, ok
}

// Lookup 按模块身份与相对地址解释物理函数。
func (p *SymbolProfile) Lookup(module, buildID string, off uint64) (FuncSym, bool) {
	m, ok := p.Module(module, buildID)
	if !ok {
		return FuncSym{}, false
	}
	for _, fn := range m.Funcs {
		if fn.Contains(off) {
			return fn, true
		}
	}
	return FuncSym{}, false
}

// Inlines 返回某模块相对地址上的内联层次，按 InlineLevel 从大到小
// （外层到靠近物理帧）排序；物理函数由 Lookup 单独给出。
func (p *SymbolProfile) Inlines(module, buildID string, off uint64) []InlineSite {
	m, ok := p.Module(module, buildID)
	if !ok {
		return nil
	}
	sites := append([]InlineSite(nil), m.InlineAt[off]...)
	sort.Slice(sites, func(i, j int) bool { return sites[i].InlineLevel > sites[j].InlineLevel })
	return sites
}

// TimedResolver 按“原始地址 + 采样时刻 + 当时的模块装载关系”解释帧。
// 它是 ContextSymbolResolver：即使两个时刻的地址完全相同，也只会命中
// 各自时刻装载的模块/构建，绝不跨时间折叠。
type TimedResolver struct {
	History *ModuleHistory
	Profile *SymbolProfile
}

// Resolve 兼容只接受 SymbolResolver 的旧入口；没有采样时刻时，
// 时间区间判断退化为只按地址匹配。优先使用 Reinterpret 自动识别到的
// ResolveFrame（带时刻）。
func (r *TimedResolver) Resolve(f Frame) []FrameInfo {
	return r.ResolveFrame(ResolveContext{}, f)
}

// NewTimedResolver 把模块时间线与一份符号资料绑定成时间感知解析器。
func NewTimedResolver(history *ModuleHistory, profile *SymbolProfile) *TimedResolver {
	return &TimedResolver{History: history, Profile: profile}
}

// ResolveFrame 实现 ContextSymbolResolver。
//
// 返回规则：
//   - 时刻与地址唯一命中某次装载：按该次装载的模块/构建查符号，
//     必要时展开内联层；模块已知但该构建没有符号，保留原地址且标 Missing；
//   - 零个或多个装载同时命中：归属无法确认，保留原地址并标 Uncertain；
//   - 无时间线信息且原帧已解析：保持原解释，避免破坏没有模块时间线的调用方。
func (r *TimedResolver) ResolveFrame(ctx ResolveContext, f Frame) []FrameInfo {
	if r == nil || r.History == nil {
		return nil
	}
	owners := r.History.OwnersAt(f.Addr, ctx.Time)
	switch len(owners) {
	case 0:
		if !f.Missing && f.Symbol != "" {
			return nil
		}
		return []FrameInfo{{Module: f.Module, BuildID: f.BuildID, Missing: true, Uncertain: true}}
	case 1:
		return r.resolveOwner(owners[0], f)
	default:
		// 同一时刻多个模块/构建声称占有该地址：归属仍无法确认。
		return []FrameInfo{{Module: f.Module, BuildID: f.BuildID, Missing: true, Uncertain: true}}
	}
}

func (r *TimedResolver) resolveOwner(owner ModuleSpan, f Frame) []FrameInfo {
	off := f.Addr - owner.Base
	fn, ok := r.Profile.Lookup(owner.Module, owner.BuildID, off)
	if !ok {
		// 归属（哪次装载）确认了，但这份符号资料没有该地址的名字。
		return []FrameInfo{{
			Module:  owner.Module,
			BuildID: owner.BuildID,
			Missing: true,
		}}
	}
	sites := r.Profile.Inlines(owner.Module, owner.BuildID, off)
	out := make([]FrameInfo, 0, len(sites)+1)
	for _, site := range sites {
		out = append(out, FrameInfo{
			Module:      owner.Module,
			BuildID:     owner.BuildID,
			Symbol:      site.Name,
			InlineLevel: site.InlineLevel,
		})
	}
	out = append(out, FrameInfo{
		Module:      owner.Module,
		BuildID:     owner.BuildID,
		Symbol:      fn.Name,
		InlineLevel: 0,
	})
	return out
}
