package perfsample

// Session 在重新解释流程中保存“上一个视角”，允许用户撤回一次，
// 恢复到最近一次 Reinterpret 之前的样本/树视图。
//
// 会话不复制原始样本的地址和时刻：它们在所有视角中始终相同；
// 撤回只是把当前解释切片换回旧切片。只保留一层历史：撤回之后再次
// Reinterpret 会建立新的撤回点，撤回状态不会无限堆积。
type Session struct {
	current  []Sample
	previous []Sample
	canUndo  bool
}

// NewSession 用原始样本创建视角会话。
func NewSession(raw []Sample) *Session {
	return &Session{current: raw}
}

// Samples 返回当前视角下的样本。
func (s *Session) Samples() []Sample {
	if s == nil {
		return nil
	}
	return s.current
}

// Tree 用当前视角构建一棵新的执行树。
func (s *Session) Tree() *Tree {
	t := NewTree()
	t.AddBatch(s.Samples())
	return t
}

// Reinterpret 应用一份新的符号资料，并把应用前的视角压入一层撤回栈。
func (s *Session) Reinterpret(resolver SymbolResolver) []Sample {
	s.previous = s.current
	s.current = Reinterpret(s.current, resolver)
	s.canUndo = true
	return s.current
}

// CanUndo 报告当前是否存在可撤回的重新解释。
func (s *Session) CanUndo() bool { return s != nil && s.canUndo }

// Undo 撤回最近一次重新解释并恢复旧视角。没有可撤回视角时返回 false。
func (s *Session) Undo() bool {
	if s == nil || !s.canUndo {
		return false
	}
	s.current = s.previous
	s.previous = nil
	s.canUndo = false
	return true
}

// ReinterpretTimed 是 Reinterpret 的时间感知便捷入口；resolver 通常是
// NewTimedResolver 产出的 TimedResolver。
func (s *Session) ReinterpretTimed(resolver ContextSymbolResolver) []Sample {
	return s.Reinterpret(contextAdapter{resolver})
}

// contextAdapter 让 ContextSymbolResolver 也能满足只接受
// SymbolResolver 的旧入口。
type contextAdapter struct {
	r ContextSymbolResolver
}

func (a contextAdapter) Resolve(Frame) []FrameInfo { return nil }

func (a contextAdapter) ResolveFrame(ctx ResolveContext, f Frame) []FrameInfo {
	return a.r.ResolveFrame(ctx, f)
}
