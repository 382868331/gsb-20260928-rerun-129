package perfsample

import "strings"

// Move 描述一个函数在两个构建版本之间的位置/解释变化。
// 它只建立“解释”之间的对应，绝不搬运任何样本权重：旧构建中由
// (OldModule, OldSymbol) 标识的帧，在新构建里对应 (NewModule, NewSymbol)。
// 内联层次单独对应；同名函数位于不同模块时必须给出模块名才能匹配。
type Move struct {
	OldModule string
	OldSymbol string
	OldInline int

	NewModule string
	NewSymbol string
	NewInline int
}

// Mapping 是一组跨构建版本的帧对应关系。
// 没有出现在映射里、且帧键也不相同的路径将被标为未匹配。
type Mapping struct {
	moves map[string]string
}

// NewMapping 用一组 Move 构建映射。重复的旧身份以最后一条为准。
func NewMapping(moves ...Move) *Mapping {
	m := &Mapping{moves: map[string]string{}}
	for _, mv := range moves {
		old := frameIdentity(mv.OldModule, mv.OldSymbol, mv.OldInline)
		newK := frameIdentity(mv.NewModule, mv.NewSymbol, mv.NewInline)
		m.moves[old] = newK
	}
	return m
}

// MapFrame 返回某帧键在新版本中的对应帧键。
// ok 为 false 表示该帧没有显式对应（调用方应回退为“帧键相同即对应”）。
func (m *Mapping) MapFrame(frameKey string) (mapped string, ok bool) {
	if m == nil {
		return "", false
	}
	mapped, ok = m.moves[frameKey]
	return mapped, ok
}

// TranslatePath 把一条旧调用路径翻译成新调用路径。
// 对每一帧：先查显式 Move，查不到再尝试解析其模块/符号/内联身份；
// 若旧帧是缺失符号占位（unknown），则无法可靠对应，返回 ok=false。
func (m *Mapping) TranslatePath(oldPath []string) (newPath []string, ok bool) {
	newPath = make([]string, 0, len(oldPath))
	for _, key := range oldPath {
		if strings.HasPrefix(key, unknownPrefix) || key == unknownName {
			return nil, false
		}
		if mapped, found := m.MapFrame(key); found {
			newPath = append(newPath, mapped)
			continue
		}
		// 没有显式移动：跨版本保持同一身份。
		newPath = append(newPath, key)
	}
	return newPath, true
}
