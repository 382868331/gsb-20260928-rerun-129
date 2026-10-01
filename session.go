package perfsample

// Reinterpretation 描述一次“重新解释”所使用的资料，供会话管理与
// 撤回后展示旧视角。它只承载解释信息，绝不搬运或修改样本权重。
type Reinterpretation struct {
	// Label 是这次解释的可读名字（如 "symbols-build-3"）。
	Label string
	// Resolver 与 History 二选一或同时使用：
	// History 非空时走时间感知重解释（结合采样时刻的模块关系），
	// 否则用不带时间维度的 Resolver。两者都为空等于不改变解释。
	Resolver SymbolResolver
	History  *ModuleHistory
	Timed    TimedSymbolResolver
}

// View 是某一解释视角下的样本与其执行树。
// Samples 始终保留原始 Addr 与 Time，树只是这些样本在当前解释下的聚合。
type View struct {
	Label   string
	Samples []Sample
	Tree    *Tree
}

// Session 持有同一批原始样本的当前解释视角，并支持撤回一次重新解释、
// 恢复上一个视角。
//
// 只保留一层历史：再次 Undo 或没有可撤回的视角时返回 false，不会
// 无限制地堆叠快照。原始样本（含地址与时刻）永不被修改。
type Session struct {
	current *View
	prev    *View
}

// NewSession 用原始样本建立初始视角（label 仅用于展示）。
func NewSession(raw []Sample, label string) *Session {
	tree := NewTree()
	tree.AddBatch(raw)
	return &Session{current: &View{Label: label, Samples: raw, Tree: tree}}
}

// Current 返回当前视角。
func (s *Session) Current() *View { return s.current }

// CanUndo 报告是否还能撤回一次。
func (s *Session) CanUndo() bool { return s.prev != nil }

// Apply 用新资料重新解释当前样本，切换到新视角。
// 旧视角被保存为唯一的撤回点；连续 Apply 后只能撤回最近一次。
func (s *Session) Apply(r Reinterpretation) *View {
	var re []Sample
	switch {
	case r.History != nil:
		re = ReinterpretTimed(s.current.Samples, r.History, r.Timed)
	case r.Resolver != nil:
		re = Reinterpret(s.current.Samples, r.Resolver)
	default:
		re = s.current.Samples
	}
	tree := NewTree()
	tree.AddBatch(re)
	s.prev = s.current
	s.current = &View{Label: r.Label, Samples: re, Tree: tree}
	return s.current
}

// Undo 撤回最近一次重新解释，恢复旧视角（旧树）。
// 返回是否真的发生了撤回；没有可撤回视角时返回 false。
// 撤回后撤回点清空：只允许撤回一次，不能反复横跳。
func (s *Session) Undo() bool {
	if s.prev == nil {
		return false
	}
	s.current = s.prev
	s.prev = nil
	return true
}
