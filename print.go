package perfsample

import (
	"fmt"
	"strings"
)

// String 以缩进树形式渲染执行树，每个节点显示
// 自身权重(self) 与包含权重(total)。
func (t *Tree) String() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("root total=%.0f samples=%d\n", t.Root.TotalWeight, t.Root.SampleCount))
	walkPrint(&b, t.Root, "")
	return b.String()
}

func walkPrint(b *strings.Builder, n *Node, indent string) {
	for _, c := range n.SortedChildren() {
		name := c.DisplayName()
		if c.InlineLevel > 0 {
			name += fmt.Sprintf(" [inline:%d]", c.InlineLevel)
		}
		module := c.Module
		if c.BuildID != "" {
			module += fmt.Sprintf("#%s", c.BuildID)
		}
		if module != "" {
			name += fmt.Sprintf(" (%s)", module)
		}
		if c.HasMissing {
			name += " [missing-symbol]"
		}
		b.WriteString(fmt.Sprintf("%s- %s self=%.0f total=%.0f samples=%d\n",
			indent, name, c.SelfWeight, c.TotalWeight, c.SampleCount))
		walkPrint(b, c, indent+"  ")
	}
}
