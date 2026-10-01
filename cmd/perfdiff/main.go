// perfdiff 是一个纯演示程序：所有调用栈样本都是代码内嵌的人工样本，
// 不读取任何系统进程或真实 profile 文件。
//
// 它演示：
//  1. 把多批人工样本汇成可追溯的执行树（含递归、缺失符号）；
//  2. 符号资料补全后对同一批原始样本重新解释（权重守恒）；
//  3. 内联层次展开、同名函数按模块区分身份；
//  4. 跨构建版本按 Move 映射 + 样本锚点对应调用路径；
//  5. 比较报告区分“符号变化”“样本变化”“未匹配”。
package main

import (
	"fmt"

	"perfsample"
)

func main() {
	// ---- 第一版程序的人工样本（符号版本 build-v1，采集来源 manual）----
	v1 := []perfsample.Sample{
		stack(3, "manual", "build-v1", "b1", "main", "serve", "handle", "encode"),
		stack(2, "manual", "build-v1", "b1", "main", "serve", "handle"),
		// handle 递归调用自身：两层递归必须保留为两次调用。
		stack(4, "manual", "build-v1", "b2", "main", "serve", "handle", "handle", "encode"),
		// 该帧符号缺失，但样本不能丢。
		missing(2, "manual", "build-v1", "b2", 0x4010, "main", "serve", "handle"),
	}

	// ---- 第二版程序：encode 变快，新增 cache 路径；
	// 由一个后续新增的采集器 newtrace 产出，但分析逻辑完全复用。----
	v2 := []perfsample.Sample{
		stack(1, "newtrace", "build-v2", "b3", "main", "serve", "handle", "encode"),
		stack(2, "newtrace", "build-v2", "b3", "main", "serve", "handle"),
		stack(4, "newtrace", "build-v2", "b3", "main", "serve", "handle", "handle", "encode"),
		stack(6, "newtrace", "build-v2", "b4", "main", "serve", "handle", "cacheLookup"),
	}

	treeV1 := perfsample.NewTree()
	w1, n1 := treeV1.AddBatch(v1)
	treeV2 := perfsample.NewTree()
	w2, n2 := treeV2.AddBatch(v2)

	fmt.Println("=== 第一版执行树（人工样本合成）===")
	fmt.Print(treeV1)
	// 再模拟一次跨采集器批次合并，验证总权重对得上。
	merged := perfsample.NewTree()
	mw1, mn1 := merged.AddBatch(v1)
	mw2, mn2 := merged.AddBatch(v2)
	rep := perfsample.Reconcile(merged, mw1+mw2, mn1+mn2)
	fmt.Printf("批次对账: 批次1 %.0f/%d + 批次2 %.0f/%d => 树内 %.0f/%d balanced=%v\n\n",
		w1, n1, w2, n2, rep.TreeTotal, rep.TreeCount, rep.Balanced)

	fmt.Println("=== 第二版执行树（新增采集器复用同一分析器）===")
	fmt.Print(treeV2)

	fmt.Println("=== 两版相同调用路径上的变化（按包含权重变化排序）===")
	cmp := perfsample.Compare(treeV1, treeV2)
	fmt.Printf("%-42s %8s %8s %8s\n", "path", "oldTot", "newTot", "delta")
	for _, c := range cmp.Shared() {
		fmt.Printf("%-42s %8.0f %8.0f %+8.0f\n", c.FullName(), c.OldTotal, c.NewTotal, c.DeltaTotal)
	}
	fmt.Println("\n新增路径（仅第二版存在）:")
	for _, c := range cmp.Changes {
		if !c.InOld && c.InNew {
			fmt.Printf("  + %s total=%.0f\n", c.FullName(), c.NewTotal)
		}
	}

	demoReinterpretAndMapping()
}

// demoReinterpretAndMapping 用第二组人工样本演示符号补全、内联展开、
// 跨版本函数移动，以及比较报告如何区分符号变化与样本变化。
func demoReinterpretAndMapping() {
	fmt.Println("\n=== 符号补全：同一批原始样本重新解释（地址/权重不变）===")
	raw := []perfsample.Sample{
		{ID: "p1", Weight: 3, Stack: []perfsample.Frame{
			{Symbol: "main"},
			{Missing: true, Addr: 0x4010},
			{Symbol: "encode"},
		}, Prov: perfsample.Provenance{Source: "manual", SymbolVersion: "sym-old", Batch: "b5"}},
		{ID: "p2", Weight: 2, Stack: []perfsample.Frame{
			{Symbol: "main"},
			{Missing: true, Addr: 0x4010},
		}, Prov: perfsample.Provenance{Source: "manual", SymbolVersion: "sym-old", Batch: "b5"}},
	}
	treeRaw := perfsample.NewTree()
	rw, rn := treeRaw.AddBatch(raw)

	// 新符号资料：0x4010 解析为 libfoo.so!decode；
	// encode 物理帧其实内联了 compress（内联层次 1）。
	resolver := perfsample.ResolverFunc(func(f perfsample.Frame) []perfsample.FrameInfo {
		switch {
		case f.Missing && f.Addr == 0x4010:
			return []perfsample.FrameInfo{{Module: "libfoo.so", Symbol: "decode"}}
		case f.Symbol == "encode":
			return []perfsample.FrameInfo{
				{Symbol: "compress", InlineLevel: 1},
				{Symbol: "encode", InlineLevel: 0},
			}
		default:
			return nil
		}
	})
	re := perfsample.Reinterpret(raw, resolver)
	treeResolved := perfsample.NewTree()
	nw, nn := treeResolved.AddBatch(re)
	fmt.Print(treeResolved)
	fmt.Printf("权重守恒核对: 原始 %.0f/%d, 重新解释后 %.0f/%d\n", rw, rn, nw, nn)
	if rw != nw || rn != nn {
		fmt.Println("  !! 重新解释改变了权重，违反不变量")
	}

	fmt.Println("\n=== 跨构建版本对应：decode 从 libfoo.so 移动到 libbar.so ===")
	// 新构建的另一批样本：decode 移动了模块，但 p1/p2 采样占比不变；
	// 另外 encode 真的变快（3 -> 1），这是样本变化而非符号变化。
	build2 := perfsample.NewTree()
	w2, n2 := build2.AddBatch([]perfsample.Sample{
		{ID: "p1", Weight: 1, Stack: []perfsample.Frame{
			{Symbol: "main"},
			{Symbol: "decode", Module: "libbar.so"},
			{Symbol: "compress", InlineLevel: 1},
			{Symbol: "encode"},
		}, Prov: perfsample.Provenance{Source: "manual", SymbolVersion: "sym-new", Batch: "b6"}},
		{ID: "p2", Weight: 2, Stack: []perfsample.Frame{
			{Symbol: "main"},
			{Symbol: "decode", Module: "libbar.so"},
		}, Prov: perfsample.Provenance{Source: "manual", SymbolVersion: "sym-new", Batch: "b6"}},
	})
	_ = w2
	_ = n2
	mapping := perfsample.NewMapping(perfsample.Move{
		OldModule: "libfoo.so", OldSymbol: "decode",
		NewModule: "libbar.so", NewSymbol: "decode",
	})
	// 先看一组采样占比完全相同、只有函数搬家的样本：
	// 报告必须标成 symbol（纯符号变化），delta 为 0。
	sameOld := perfsample.NewTree()
	sameOld.AddBatch([]perfsample.Sample{{ID: "q", Weight: 4, Stack: []perfsample.Frame{
		{Symbol: "main"}, {Symbol: "decode", Module: "libfoo.so"},
	}}})
	sameNew := perfsample.NewTree()
	sameNew.AddBatch([]perfsample.Sample{{ID: "q", Weight: 4, Stack: []perfsample.Frame{
		{Symbol: "main"}, {Symbol: "decode", Module: "libbar.so"},
	}}})
	sameCmp := perfsample.CompareMapped(sameOld, sameNew, mapping)
	fmt.Println("采样占比不变、仅模块移动（应为 symbol，delta=0）:")
	for _, c := range sameCmp.Shared() {
		if c.FullName() == "main/libbar.so!decode" {
			fmt.Printf("  %s kind=%s old=%.0f new=%.0f delta=%+.0f\n",
				c.FullName(), c.Kind, c.OldTotal, c.NewTotal, c.DeltaTotal)
		}
	}
	fmt.Println("\n采样占比同时变化（符号移动 + 样本变化，应标 sample）:")
	cmp2 := perfsample.CompareMapped(treeResolved, build2, mapping)
	fmt.Printf("%-46s %-9s %8s %8s %8s\n", "path", "kind", "oldTot", "newTot", "delta")
	for _, c := range cmp2.Shared() {
		fmt.Printf("%-46s %-9s %8.0f %8.0f %+8.0f\n",
			c.FullName(), c.Kind, c.OldTotal, c.NewTotal, c.DeltaTotal)
	}
	if u := cmp2.Unmatched(); len(u) > 0 {
		fmt.Println("未匹配路径:")
		for _, c := range u {
			side := "old-only"
			if c.InNew {
				side = "new-only"
			}
			fmt.Printf("  ? %-44s (%s)\n", c.FullName(), side)
		}
	}
	demoTimedReloadAndUndo()
}

// demoTimedReloadAndUndo 演示同一地址在模块卸载/重新装载后属于不同代码：
// 重新解释按采样时刻命中对应构建；归属空档保留为存疑样本；两份资料
// 的差异按 rename/weight/uncertain 汇总；用户可以撤回一次新解释。
func demoTimedReloadAndUndo() {
	fmt.Println("\n=== 模块卸载后重新装载：同地址不同代码按采样时刻归属 ===")
	history := perfsample.NewModuleHistory(
		perfsample.ModuleSpan{Module: "plugin.so", BuildID: "v1", Base: 0x1000, End: 0x2000, UnloadAt: 100},
		perfsample.ModuleSpan{Module: "plugin.so", BuildID: "v2", Base: 0x1000, End: 0x2000, LoadAt: 100},
	)
	profile := perfsample.NewSymbolProfile(
		perfsample.ModuleSymbols{Module: "plugin.so", BuildID: "v1",
			Funcs: []perfsample.FuncSym{{Name: "alpha", Start: 0x10, End: 0x20}}},
		perfsample.ModuleSymbols{Module: "plugin.so", BuildID: "v2",
			Funcs: []perfsample.FuncSym{{Name: "beta", Start: 0x10, End: 0x20}}},
	)
	raw := []perfsample.Sample{
		{ID: "a", Weight: 3, Time: 50, Stack: []perfsample.Frame{{Missing: true, Addr: 0x1010}}},
		{ID: "b", Weight: 4, Time: 150, Stack: []perfsample.Frame{{Missing: true, Addr: 0x1010}}},
	}
	session := perfsample.NewSession(raw)
	session.Reinterpret(perfsample.NewTimedResolver(history, profile))
	fmt.Print(session.Tree())

	oldView := perfsample.Reinterpret(raw, perfsample.ResolverFunc(func(f perfsample.Frame) []perfsample.FrameInfo {
		if f.Addr == 0x1010 {
			return []perfsample.FrameInfo{{Module: "plugin.so", BuildID: "v1", Symbol: "alpha"}}
		}
		return nil
	}))
	newView := session.Samples()
	diff := perfsample.CompareInterpretations(oldView, newView)
	for _, s := range diff.Summarize() {
		fmt.Printf("diff kind=%-11s count=%d old=%.0f new=%.0f\n", s.Kind, s.Count, s.OldWeight, s.NewWeight)
	}
	session.Undo()
	fmt.Printf("撤回后总权重=%.0f，原始地址仍为 0x%x\n",
		session.Tree().Root.TotalWeight, session.Samples()[0].Stack[0].Addr)
}

func stack(weight float64, source, ver, batch string, syms ...string) perfsample.Sample {
	frames := make([]perfsample.Frame, len(syms))
	for i, s := range syms {
		frames[i] = perfsample.Frame{Symbol: s}
	}
	return perfsample.Sample{
		Weight: weight,
		Stack:  frames,
		Prov:   perfsample.Provenance{Source: source, SymbolVersion: ver, Batch: batch},
	}
}

// missing 构造一条中间包含缺失符号帧的样本；缺失帧地址为 addr，
// 其后仍可带已解析符号（这里用于 handle 下的采样叶子）。
func missing(weight float64, source, ver, batch string, addr uint64, syms ...string) perfsample.Sample {
	frames := make([]perfsample.Frame, 0, len(syms)+1)
	for _, s := range syms {
		frames = append(frames, perfsample.Frame{Symbol: s})
	}
	frames = append(frames, perfsample.Frame{Missing: true, Addr: addr})
	return perfsample.Sample{
		Weight: weight,
		Stack:  frames,
		Prov:   perfsample.Provenance{Source: source, SymbolVersion: ver, Batch: batch},
	}
}
