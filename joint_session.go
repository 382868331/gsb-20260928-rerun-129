package perfsample

import (
	"encoding/json"
	"errors"
	"sort"
)

// JointView 是联合分析在某个对齐/符号视角下的不可变快照。
type JointView struct {
	Label   string
	Run     *JointRun
	Samples []Sample
	Tree    *Tree
}

// AlignmentEdit 记录一次人工对齐判断。Old 字段让撤销只回滚本次修改。
type AlignmentEdit struct {
	Source       string
	OldOffsetMin int64
	OldOffsetMax int64
	NewOffsetMin int64
	NewOffsetMax int64
}

type jointUndo struct {
	edit AlignmentEdit
}

// JointSession 保存各来源原始样本，并把符号更新与时间对齐分开管理。
type JointSession struct {
	label    string
	raw      *JointRun
	current  *JointRun
	history  *ModuleHistory
	resolver JointTimedResolver
	view     *JointView
	undos    []jointUndo
	saved    map[string]*JointView
}

func cloneJointRun(run *JointRun) *JointRun {
	if run == nil {
		return nil
	}
	out := &JointRun{Name: run.Name, Sources: map[string]*SourceTrack{}}
	for id, tr := range run.Sources {
		cp := *tr
		cp.Samples = append([]Sample(nil), tr.Samples...)
		out.Sources[id] = &cp
	}
	return out
}

func NewJointSession(run *JointRun, label string) *JointSession {
	raw := cloneJointRun(run)
	s := &JointSession{label: label, raw: raw, current: cloneJointRun(raw), saved: map[string]*JointView{}}
	s.rebuild()
	return s
}

// SetSymbols 更新模块装卸历史和符号解释，但不改变任何来源的对齐判断。
func (s *JointSession) SetSymbols(history *ModuleHistory, resolver JointTimedResolver, label string) *JointView {
	s.history = history
	s.resolver = resolver
	if label != "" {
		s.label = label
	}
	s.rebuild()
	return s.view
}

func (s *JointSession) rebuild() {
	samples := ReinterpretJoint(s.current, s.history, s.resolver)
	tree := NewTree()
	tree.AddBatch(samples)
	s.view = &JointView{Label: s.label, Run: cloneJointRun(s.current), Samples: samples, Tree: tree}
}

func (s *JointSession) Current() *JointView { return s.view }
func (s *JointSession) Raw() *JointRun      { return cloneJointRun(s.raw) }

// AdjustAlignment 修改单个来源的偏移范围，并只重算该视角下的热点归属。
func (s *JointSession) AdjustAlignment(source string, offsetMin, offsetMax int64) (*JointView, error) {
	tr := s.current.Sources[source]
	if tr == nil {
		return nil, errors.New("unknown joint source: " + source)
	}
	if offsetMin > offsetMax {
		offsetMin, offsetMax = offsetMax, offsetMin
	}
	edit := AlignmentEdit{
		Source:       source,
		OldOffsetMin: tr.OffsetMin, OldOffsetMax: tr.OffsetMax,
		NewOffsetMin: offsetMin, NewOffsetMax: offsetMax,
	}
	tr.OffsetMin, tr.OffsetMax = offsetMin, offsetMax
	s.undos = append(s.undos, jointUndo{edit: edit})
	s.rebuild()
	return s.view, nil
}

// CanUndoAlignment 报告是否存在可撤回的对齐修改。
func (s *JointSession) CanUndoAlignment() bool { return len(s.undos) > 0 }

func (s *JointSession) undoAlignment() (AlignmentEdit, bool) {
	if len(s.undos) == 0 {
		return AlignmentEdit{}, false
	}
	u := s.undos[len(s.undos)-1]
	s.undos = s.undos[:len(s.undos)-1]
	if tr := s.current.Sources[u.edit.Source]; tr != nil {
		tr.OffsetMin = u.edit.OldOffsetMin
		tr.OffsetMax = u.edit.OldOffsetMax
	}
	s.rebuild()
	return u.edit, true
}

// UndoLastAlignment 撤回最近一次对齐修改。
func (s *JointSession) UndoLastAlignment() (AlignmentEdit, bool) { return s.undoAlignment() }

// SaveView 保存当前结论；后续对齐或符号更新不会改写它。
func (s *JointSession) SaveView(label string) *JointView {
	if label == "" {
		label = s.label
	}
	v := &JointView{Label: label, Run: cloneJointRun(s.current), Samples: append([]Sample(nil), s.view.Samples...), Tree: NewTree()}
	v.Tree.AddBatch(v.Samples)
	s.saved[label] = v
	return v
}

func (s *JointSession) View(label string) (*JointView, bool) {
	v, ok := s.saved[label]
	return v, ok
}

func (s *JointSession) Views() []string {
	out := make([]string, 0, len(s.saved))
	for label := range s.saved {
		out = append(out, label)
	}
	sort.Strings(out)
	return out
}

// PersistentTrack 是可保存的单个来源状态。
type PersistentTrack struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	RawSamples    []Sample `json:"raw_samples"`
	CoverageStart uint64   `json:"coverage_start"`
	CoverageEnd   uint64   `json:"coverage_end"`
	RawOffsetMin  int64    `json:"raw_offset_min"`
	RawOffsetMax  int64    `json:"raw_offset_max"`
	OffsetMin     int64    `json:"offset_min"`
	OffsetMax     int64    `json:"offset_max"`
}

type persistentView struct {
	Label   string   `json:"label"`
	Samples []Sample `json:"samples"`
}

// JointSessionState 是可 JSON 保存/重开的联合视图状态。
type JointSessionState struct {
	Label   string            `json:"label"`
	Tracks  []PersistentTrack `json:"tracks"`
	History []ModuleSpan      `json:"history,omitempty"`
	Edits   []AlignmentEdit   `json:"edits,omitempty"`
	Views   []persistentView  `json:"views,omitempty"`
}

// SaveState 导出当前来源、对齐修改、模块历史和已保存结论。
func (s *JointSession) SaveState() (*JointSessionState, error) {
	state := &JointSessionState{Label: s.label}
	for _, tr := range sortedTracks(s.raw.Sources) {
		cur := s.current.Sources[tr.ID]
		pt := PersistentTrack{
			ID: tr.ID, Name: tr.Name, RawSamples: append([]Sample(nil), tr.Samples...),
			CoverageStart: tr.CoverageStart, CoverageEnd: tr.CoverageEnd,
			RawOffsetMin: tr.OffsetMin, RawOffsetMax: tr.OffsetMax,
			OffsetMin: cur.OffsetMin, OffsetMax: cur.OffsetMax,
		}
		state.Tracks = append(state.Tracks, pt)
	}
	if s.history != nil {
		state.History = append(state.History, s.history.spans...)
	}
	for _, u := range s.undos {
		state.Edits = append(state.Edits, u.edit)
	}
	for _, label := range s.Views() {
		v := s.saved[label]
		state.Views = append(state.Views, persistentView{Label: label, Samples: append([]Sample(nil), v.Samples...)})
	}
	return state, nil
}

func (s *JointSession) MarshalJSON() ([]byte, error) {
	state, err := s.SaveState()
	if err != nil {
		return nil, err
	}
	return json.Marshal(state)
}

// RestoreJointSession 从状态重建会话；resolver 必须由调用方重新提供相同规则。
func RestoreJointSession(state *JointSessionState, resolver JointTimedResolver) *JointSession {
	tracks := make([]*SourceTrack, 0, len(state.Tracks))
	for _, pt := range state.Tracks {
		tr := &SourceTrack{
			ID: pt.ID, Name: pt.Name, Samples: pt.RawSamples,
			CoverageStart: pt.CoverageStart, CoverageEnd: pt.CoverageEnd,
			OffsetMin: pt.RawOffsetMin, OffsetMax: pt.RawOffsetMax,
		}
		tracks = append(tracks, tr)
	}
	sess := NewJointSession(NewJointRun(state.Label, tracks...), state.Label)
	for _, pt := range state.Tracks {
		if cur := sess.current.Sources[pt.ID]; cur != nil {
			cur.OffsetMin, cur.OffsetMax = pt.OffsetMin, pt.OffsetMax
		}
	}
	var history *ModuleHistory
	if len(state.History) > 0 {
		history = NewModuleHistory(state.History...)
	}
	sess.SetSymbols(history, resolver, state.Label)
	sess.undos = make([]jointUndo, 0, len(state.Edits))
	for _, edit := range state.Edits {
		sess.undos = append(sess.undos, jointUndo{edit: edit})
	}
	for _, pv := range state.Views {
		t := NewTree()
		t.AddBatch(pv.Samples)
		sess.saved[pv.Label] = &JointView{Label: pv.Label, Samples: pv.Samples, Tree: t}
	}
	return sess
}
