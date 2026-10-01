package perfsample

import "sort"

// ProfileDiff 是两份符号资料（两次解释）之间的差异报告。
//
// 它回答三个问题：
//   - 采样权重真的变了多少（KindSample）；
//   - 哪些只是名称/模块/内联归属的修订，权重没变（KindSymbol）；
//   - 哪些部分两版都保留了权重，但归属对应仍无法确认（KindUncertain）。
//
// 未匹配样本绝不丢弃：两侧全部叶子自身权重之和恒等于 OldTotal/NewTotal，
// 且各类别权重之和也必须对上。Balanced 为 false 即说明归类漏记，不能靠
// 删掉未匹配样本让占比变好看。
type ProfileDiff struct {
	Changes []PathChange

	OldTotal float64
	NewTotal float64

	// 按叶子（采样点）自身权重归类，每份权重恰好落到一个类别。
	OldSymbolWeight    float64 // 仅名称/模块/内联修订，两侧权重相同
	NewSymbolWeight    float64
	OldSampleWeight    float64 // 对应路径上权重真的不同（含仅一侧出现）
	NewSampleWeight    float64
	OldUncertainWeight float64 // 两版都保留，但归属对应无法确认
	NewUncertainWeight float64

	Balanced bool
}

// ByKind 返回指定差异类型的路径变化。
func (d *ProfileDiff) ByKind(kind ChangeKind) []PathChange {
	var out []PathChange
	for _, c := range d.Changes {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// Uncertain 返回归属仍无法确认的差异（两侧权重都保留，不做搬运）。
func (d *ProfileDiff) Uncertain() []PathChange { return d.ByKind(KindUncertain) }

// profLeaf 是聚合时使用的叶子视图：自身权重拆成“带 ID 的逐样本切片”
// 与“无 ID 的匿名余量”。每个切片只带一个来源 key，方便精确归类。
type profLeaf struct {
	keyPath string
	path    []string
	self    float64
	// idW 为样本 ID -> 该叶子上的自身权重；anon 为无 ID 样本的剩余权重。
	idW  map[string]float64
	anon float64
}

func collectProfLeaves(t *Tree) map[string]*profLeaf {
	out := map[string]*profLeaf{}
	var walk func(n *Node)
	walk = func(n *Node) {
		for _, c := range n.Children {
			if c.SelfWeight > 0 {
				var idSum float64
				idw := map[string]float64{}
				for id, w := range c.selfThrough {
					idw[id] = w
					idSum += w
				}
				out[c.keyPath] = &profLeaf{
					keyPath: c.keyPath,
					path:    splitKeyPath(c.keyPath),
					self:    c.SelfWeight,
					idW:     idw,
					anon:    c.SelfWeight - idSum,
				}
			}
			walk(c)
		}
	}
	walk(t.Root)
	return out
}

// DiffProfiles 比较同一批样本在两份符号资料解释下的两棵树。
//
// 归类在叶子（采样点）自身权重的粒度上进行，每个样本权重恰好进入一个类别：
//   - 带 ID 样本：身份明确，默认两侧是同一份采样。旧叶子路径经 Mapping
//     翻译后等于新叶子路径（或无映射且路径相同）时身份一致：两侧权重相等
//     为 KindSymbol（纯修订），不等为 KindSample；翻译失败（如缺符号占位）
//     时为 KindUncertain，两侧权重各自单列、delta 置 0；仅一侧出现该 ID
//     为 KindSample。
//   - 无 ID 样本：只能按叶子路径对应，翻译后路径一致才算身份一致；
//     对应不上的匿名权重两侧都进 KindUncertain，不凭空搬运。
//
// 这样同地址被混进同一占位函数的两个版本会被拆成 uncertain，而不是被
// 合并成一个函数或直接丢掉。
func DiffProfiles(oldTree, newTree *Tree, mapping *Mapping) *ProfileDiff {
	d := &ProfileDiff{OldTotal: oldTree.Root.TotalWeight, NewTotal: newTree.Root.TotalWeight}
	old := collectProfLeaves(oldTree)
	newL := collectProfLeaves(newTree)

	// 样本 ID -> 它在每侧作为采样点落到的叶子路径（歧义时不存在于映射）。
	oldAnchor := anchorLeafMap(oldTree)
	newAnchor := anchorLeafMap(newTree)

	// 聚合器：同 (新路径, 类别) 的变化合并为一条 PathChange，权重累计。
	type aggKey struct {
		newPath string
		oldPath string
		kind    ChangeKind
		moved   bool
	}
	type aggVal struct {
		newPath, oldPath []string
		oldSelf, newSelf float64
		inOld, inNew     bool
	}
	agg := map[aggKey]*aggVal{}
	add := func(newKeyPath, oldKeyPath string, kind ChangeKind, moved bool,
		oldW, newW float64, inOld, inNew bool) {
		k := aggKey{newPath: newKeyPath, oldPath: oldKeyPath, kind: kind, moved: moved}
		v := agg[k]
		if v == nil {
			v = &aggVal{
				newPath: splitKeyPath(newKeyPath),
				oldPath: splitKeyPath(oldKeyPath),
			}
			agg[k] = v
		}
		v.oldSelf += oldW
		v.newSelf += newW
		v.inOld = v.inOld || inOld
		v.inNew = v.inNew || inNew
		switch kind {
		case KindSample:
			d.OldSampleWeight += oldW
			d.NewSampleWeight += newW
		case KindUncertain:
			d.OldUncertainWeight += oldW
			d.NewUncertainWeight += newW
		default: // same/symbol：纯归属修订，权重相同
			d.OldSymbolWeight += oldW
			d.NewSymbolWeight += newW
		}
	}

	translatedLeaf := func(op []string) (newPath string, ok bool) {
		if mapping == nil {
			return joinKeyPath(op), true
		}
		tr, ok := mapping.TranslatePath(op)
		if !ok {
			return "", false
		}
		return joinKeyPath(tr), true
	}

	// ---- 旧侧逐叶子归类 ----
	consumedNewAnon := map[string]float64{} // 新叶子已被旧匿名权重占用的量
	oldKeys := make([]string, 0, len(old))
	for k := range old {
		oldKeys = append(oldKeys, k)
	}
	sort.Strings(oldKeys)
	for _, okp := range oldKeys {
		ol := old[okp]
		nk, translatable := translatedLeaf(ol.path)
		nl, newHasPath := newL[nk]

		// 带 ID 样本逐切片归类。
		idList := make([]string, 0, len(ol.idW))
		for id := range ol.idW {
			idList = append(idList, id)
		}
		sort.Strings(idList)
		for _, id := range idList {
			w := ol.idW[id]
			nkID, idInNew := newAnchor[id]
			switch {
			case !idInNew:
				// 该样本在新解释中没有叶子落点：旧侧独有，样本变化。
				add("", okp, KindSample, false, w, 0, true, false)
			case translatable && newHasPath && nl != nil && nkID == nk:
				nw := nl.idW[id]
				kind := KindSymbol
				if !approxEq(w, nw) {
					kind = KindSample
				}
				add(nk, okp, kind, nk != okp, w, nw, true, true)
			default:
				// 两版都有该样本，但路径翻译对不上（缺符号/散落）：归属存疑。
				add(nkID, okp, KindUncertain, nkID != okp, w, newL[nkID].idW[id], true, true)
			}
		}

		// 匿名余量：翻译后路径一致则归属一致，否则两侧存疑。
		if ol.anon > 0 {
			switch {
			case translatable && newHasPath && newL[nk].anon-consumedNewAnon[nk] >= ol.anon-1e-9:
				nw := ol.anon
				consumedNewAnon[nk] += nw
				kind := KindSymbol
				if !approxEq(ol.anon, nw) {
					kind = KindSample
				}
				add(nk, okp, kind, nk != okp, ol.anon, nw, true, true)
			default:
				add("", okp, KindUncertain, false, ol.anon, 0, true, false)
			}
		}
	}

	// ---- 新侧剩余权重归类 ----
	nkKeys := make([]string, 0, len(newL))
	for k := range newL {
		nkKeys = append(nkKeys, k)
	}
	sort.Strings(nkKeys)
	for _, nkp := range nkKeys {
		nl := newL[nkp]

		idList := make([]string, 0, len(nl.idW))
		for id := range nl.idW {
			idList = append(idList, id)
		}
		sort.Strings(idList)
		for _, id := range idList {
			nw := nl.idW[id]
			if _, idInOld := oldAnchor[id]; !idInOld {
				// 新解释中独有的样本：样本权重增加。
				add(nkp, "", KindSample, false, 0, nw, false, true)
			}
			// 两侧都有的 ID 已在旧侧循环一次性归好类（含新侧权重），不重复计。
		}

		remaining := nl.anon - consumedNewAnon[nkp]
		if remaining > 1e-9 {
			// 新侧未被旧匿名余量对上的匿名权重：若它的路径在旧树就以同一身份
			// 存在则属于真实增减，否则归属存疑。这里没有样本锚点，统一保守地
			// 标为 uncertain（不凭空搬运），权重仍完整保留。
			add(nkp, "", KindUncertain, false, 0, remaining, false, true)
		}
	}

	// ---- 输出聚合结果 ----
	keys := make([]aggKey, 0, len(agg))
	for k := range agg {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].newPath != keys[j].newPath {
			return keys[i].newPath < keys[j].newPath
		}
		if keys[i].oldPath != keys[j].oldPath {
			return keys[i].oldPath < keys[j].oldPath
		}
		return keys[i].kind < keys[j].kind
	})
	for _, k := range keys {
		v := agg[k]
		newPath := v.newPath
		oldPath := v.oldPath
		if len(newPath) == 0 {
			newPath = oldPath // 旧侧独有时展示旧路径
		}
		if len(oldPath) == 0 {
			oldPath = newPath
		}
		c := PathChange{
			Path: newPath, OldPath: oldPath,
			OldSelf: v.oldSelf, NewSelf: v.newSelf,
			DeltaSelf: v.newSelf - v.oldSelf,
			InOld:     v.inOld, InNew: v.inNew,
			Kind: k.kind, SymbolMove: k.moved,
		}
		if k.kind != KindUncertain {
			c.Matched = true
		}
		if k.kind == KindUncertain {
			c.DeltaSelf = 0
		}
		d.Changes = append(d.Changes, c)
	}

	oldSum := d.OldSymbolWeight + d.OldSampleWeight + d.OldUncertainWeight
	newSum := d.NewSymbolWeight + d.NewSampleWeight + d.NewUncertainWeight
	d.Balanced = approxEq(oldSum, d.OldTotal) && approxEq(newSum, d.NewTotal)
	return d
}

func splitKeyPath(key string) []string {
	if key == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(key); i++ {
		if key[i] == pathSep[0] {
			out = append(out, key[start:i])
			start = i + len(pathSep)
			i += len(pathSep) - 1
		}
	}
	return append(out, key[start:])
}

func joinKeyPath(p []string) string {
	if len(p) == 0 {
		return ""
	}
	out := p[0]
	for _, s := range p[1:] {
		out += pathSep + s
	}
	return out
}
