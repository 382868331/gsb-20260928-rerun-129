// perfdiff 是一个纯演示程序：所有调用栈样本都是代码内嵌的人工样本，
// 不读取任何系统进程或真实 profile 文件。
//
// 它演示：
//  1. 把多批人工样本汇成可追溯的执行树（含递归、缺失符号）；
//  2. 自身耗时与包含耗时分开统计；
//  3. 合并批次后用总权重对账；
//  4. 比较同一程序两个版本在相同调用路径上的变化。
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

	fmt.Println("\n=== 符号资料补全后重新解释（地址和权重不变）===")
	resolved := treeV1.Reinterpret(func(f perfsample.Frame) []perfsample.Frame {
		if f.Missing && f.Addr == 0x4010 {
			return []perfsample.Frame{
				{Module: "app", Symbol: "serialize", Addr: f.Addr, InlineDepth: 1},
				{Module: "app", Symbol: "writeFast", Addr: f.Addr, InlineDepth: 2},
			}
		}
		return nil
	})
	if resolved.Root.TotalWeight != treeV1.Root.TotalWeight || resolved.Root.SampleCount != treeV1.Root.SampleCount {
		panic("symbol reinterpretation changed sample weights")
	}
	inner := resolved.FindID(
		perfsample.FrameID{Symbol: "main"},
		perfsample.FrameID{Symbol: "serve"},
		perfsample.FrameID{Symbol: "handle"},
		perfsample.FrameID{Module: "app", Symbol: "serialize", Addr: 0x4010, InlineDepth: 1},
		perfsample.FrameID{Module: "app", Symbol: "writeFast", Addr: 0x4010, InlineDepth: 2},
	)
	if inner == nil {
		panic("resolved inline path not found")
	}
	fmt.Printf("main/serve/handle/serialize/writeFast self=%.0f total=%.0f addr=0x%x\n",
		inner.SelfWeight, inner.TotalWeight, inner.ID.Addr)

	fmt.Println("\n=== 显式函数移动映射（符号变化但样本权重不变）===")
	oldFunc := perfsample.NewTree()
	oldFunc.AddSample(perfsample.Sample{Weight: 5, Stack: []perfsample.Frame{{Module: "oldlib", Symbol: "work"}}})
	newFunc := perfsample.NewTree()
	newFunc.AddSample(perfsample.Sample{Weight: 5, Stack: []perfsample.Frame{{Module: "newlib", Symbol: "workV2"}}})
	mapped := perfsample.CompareBuilds(oldFunc, newFunc, []perfsample.FrameMapping{{
		From: perfsample.FrameID{Module: "oldlib", Symbol: "work"},
		To:   perfsample.FrameID{Module: "newlib", Symbol: "workV2"},
	}})
	for _, c := range mapped.Shared() {
		kind := "sample"
		if c.SymbolChanged && !c.SampleChanged {
			kind = "symbol"
		}
		fmt.Printf("%s: old=%.0f new=%.0f delta=%+.0f change=%s\n", c.FullName(), c.OldTotal, c.NewTotal, c.DeltaTotal, kind)
	}
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
