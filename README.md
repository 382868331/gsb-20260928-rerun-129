# perfsample：性能采样分析核心（第一版）

把人工/采集器产出的调用栈样本聚合成可追溯的执行树，并支持比较同一程序
两次运行在相同调用路径上的变化。核心不读取任何系统进程，只消费调用方
传入的 `Sample`。

## 模型

- `Frame`：栈的一层。`Stack[0]` 为最外层，最后一层为采样点。符号缺失时
  置 `Missing=true` 并带原始 `Addr`。`Module` 区分同名函数所在模块，
  `InlineLevel` 区分内联层次（0 为物理函数，>=1 为补回的内联帧）。
- `Sample`：一条栈样本 + `Weight`（默认 1）+ `Provenance`。
- `Provenance`：`Source`（采集来源）、`SymbolVersion`（符号版本）、
  `Batch`（批次）。合并后仍可在每个节点上按来源/版本拆分权重，因此新增
  采集器不需要改动分析结果结构。
- `Tree`/`Node`：带虚拟根的执行树。
  - `SelfWeight`：采样点落在本函数的权重（自身耗时）。
  - `TotalWeight`：经过本节点的全部权重（自身 + 子调用）。

## 关键语义

- **递归不折叠**：栈中每一层都是独立节点，`f -> f` 形成父子两个 `f`，
  各自分别统计自身/包含权重。
- **缺失符号不丢样本**：缺失帧按地址归并为 `(unknown@0x<addr>)`，
  并标记 `HasMissing`；该样本的权重照常计入。
- **批次对账**：虚拟根的 `TotalWeight/SampleCount` 等于全部已合并样本的
  权重/数量，可用 `Reconcile` 与各批次声明值核对。
- **两版对比**：`Compare` 按完整调用路径（而非函数名）对齐两次运行，
  `Shared()` 只返回两版都存在的路径及其权重差。

## 符号补全与重新解释（第二版新增）

拿到新符号资料后，用 `Reinterpret(samples, resolver)` 对**同一批原始样本**
重新解释，再重新建树。解析器只能改解释，碰不到权重：

- `Sample.ID/Weight/Provenance` 与样本数量、顺序完全不变；
- 每个原始 `Addr` 保留在解释出的所有逻辑帧上（含内联展开的各层）；
- 缺符号 -> 已解析、函数改名、一个物理帧展开成多个内联层，都只改变
  样本落在哪些节点，**不新增也不丢失任何权重**；
- 可用节点的 `Anchors()`/`LeafAnchors()`（样本 ID -> 权重）核对
  自身权重与包含权重在重新解释前后守恒。

身份键由 `(模块, 符号/地址, 内联层次)` 共同决定：同名函数在不同模块、
递归 `f->f` 的不同层次、不同内联层都各自保留身份，不会互相折叠。
未指定模块/内联层次的旧样本键退化为纯符号名，旧行为与旧报告完全兼容。

## 跨构建版本路径对应

`NewMapping(Move{...})` 描述函数在两个构建之间的改名/移动/内联变化，
`CompareMapped(oldTree, newTree, mapping)` 按“先映射翻译路径、再用样本
锚点唯一回退”的顺序对应：

- 映射显式命中：旧路径翻译成新路径，即使名字/模块变了也能对上；
- 路径对不上但带 ID 的叶子样本在新树唯一落到同一节点：锚点回退对应；
- 缺符号占位无法翻译、锚点缺失或散落到多个节点：标为 `KindUnmatched`，
  不产生虚假权重差，`Shared()` 也不会包含它。

比较报告用 `PathChange.Kind` 说明差异来源：

- `same`：路径对应且权重不变；
- `symbol`：纯符号变化（改名/移动/补全），同一批样本同一组权重，delta 为 0；
- `sample`：对应路径上样本权重真的变了（即使同时发生符号移动）；
- `unmatched`：无法可靠对应，只能单列，不能凭空搬运权重。

## 采样期间的模块装载/卸载/重载（第三版新增）

一个进程在采样期间可能加载、卸载并在同一地址重新加载模块，相同地址在
不同时间可能属于不同代码。样本新增 `Time`（采样时刻），帧新增
`Generation`（模块装载代次）。`NewModuleHistory(ModuleSpan{...})` 描述
模块在 `[Start,End)` 内占据的地址区间，`ReinterpretTimed` 解释时先按
`(地址, 时刻)` 查“采样当时”的装载关系，再交给时间感知解析器：

- 身份键是 `(模块, 装载代次, 符号/地址, 内联层次)`：同地址不同代次的
  代码是两个独立节点，不会只按地址混成一个函数；
- 同一时刻多个区间重叠声称拥有该地址、或无时刻样本的地址在历史上落入
  多个区间时，归属被判定为有歧义，不强行归属；
- 原始 `Addr` 与 `Time` 在所有解释帧上原样保留，样本数量、顺序、
  `Weight/Provenance` 不变，内联展开与模块重定位同时发生时总权重仍守恒。

## 两份符号资料的差异比较与撤回

`DiffProfiles(oldTree, newTree, mapping)` 比较**同一批样本**在两份符号
资料下的两棵树，在叶子（采样点）粒度按样本 ID 锚点归类，三种类别并列：

- `symbol`：纯名称/模块/内联归属修订，两侧权重相等，delta 为 0；
- `sample`：对应路径上采样权重真的变化（含仅一侧出现的样本）；
- `uncertain`：两版都保留了权重，但归属对应仍无法确认（缺符号占位、
  锚点散落等），两侧权重各自单列、delta 置 0，**绝不靠丢弃未匹配样本
  美化占比**。报告的 `Old/NewSymbol/Sample/UncertainWeight` 之和必须等于
  `OldTotal/NewTotal`，`Balanced` 为 false 即说明归类漏记。

`NewSession(raw, label)` 管理解释视角：`Apply(Reinterpretation{...})`
用新资料（可带 `History` 做时间感知解释）重建树并保留唯一撤回点，
`Undo()` 撤回一次重新解释、恢复旧视角（旧树）；只能撤回一次，再次
`Undo()` 返回 false。

## 多采样器联合分析与对齐判断（第四版新增）

多个采样器记录同一程序时，各自时钟与参考时间线之间只有**已知偏移
范围**（`OffsetRange{Min, Max}`），而不是精确对齐。`Track` 把每个来源
的原始样本与其声明的偏移范围放在一起，`NewJointSession`/`NewJointView`
构建联合视图：

- 各来源保留自己的采样时刻与覆盖范围（`AlignedTrack.CoverageStart/End`、
  `RefCoverage`），对齐只发生在视图层，原始样本永不被修改；
- 每帧归属按整个可行偏移区间判定（`ModuleHistory.LookupRange`）：
  区间内只会属于同一个装载区间才是 `AttrResolved`；先后无法确定、
  可能属于多次装卸的是 `AttrAmbiguous`——明确标示候选，**不强行归到
  某次模块装卸**，也不计入任何热点；任何偏移下都不在已知模块内的是
  `AttrUnattributed`；
- `JointSession.SetAlignment(source, range)` 在声明范围内收紧某来源的
  对齐判断，视图立即重建，`Previous()` 保留先前结论可对照归属变化；
  `Undo()` 撤回一次对齐修改，**只恢复它影响的来源**，其他来源的对齐
  与符号更新不受影响；
- `Save()`/`RestoreJointSession` 把视图选择（标签、各来源对齐判断、
  符号更新记录）序列化为 JSON，重开后用相同原始轨道复现同一视图。

`CompareRuns(a, b)` 比较两段运行，把采样覆盖不同与真实权重变化分开：

- 只有双方都有采样覆盖的对齐区间内的权重差才计入 `Delta`；
- 仅一侧有观察的区间单列在 `OnlyA/OnlyB` 与 `CoverageOnlyA/B`，
  **缺少观察的区间不能当成零开销**；
- 归属不定与无法归属的权重各自单列，绝不丢样本，`Balanced` 对账；
- 报告同时展示模块地址复用（`ModuleReuse`，同地址不同代次）、
  符号更新（`SymbolUpdates`）与部分来源缺失（`MissingInA/B`）。

```go
h := perfsample.NewModuleHistory(
    perfsample.ModuleSpan{Module: "libv1.so", Base: 0x1000, Limit: 0x2000, Start: 0, End: 100, Generation: 1},
    perfsample.ModuleSpan{Module: "libv2.so", Base: 0x1000, Limit: 0x2000, Start: 100, Generation: 2},
)
tracks := []perfsample.Track{
    {Source: "cpu-prof", Samples: cpuSamples, Offset: perfsample.OffsetRange{Min: -10, Max: 10}},
    {Source: "gpu-prof", Samples: gpuSamples, Offset: perfsample.OffsetRange{Min: -5, Max: 5}},
}
s := perfsample.NewJointSession("run-1", tracks, h)
for _, a := range s.View().Ambiguous() {
    fmt.Println(a.Source, a.Name, a.SelfWeight, len(a.Candidates)) // 先后不定，单列
}
_ = s.SetAlignment("cpu-prof", perfsample.OffsetRange{Min: 0, Max: 0}) // 收紧对齐判断
_ = s.Previous() // 先前结论仍可对照
_ = s.Undo()     // 只撤回 cpu-prof 的对齐修改

rc := perfsample.CompareRuns(runA.View(), runB.View())
for _, d := range rc.Hotspots {
    fmt.Println(d.Name, d.Delta, d.OnlyA, d.OnlyB) // 覆盖差异与真实变化分开
}
state, _ := s.Save()
reopened, _ := perfsample.RestoreJointSession(tracks, h, state) // 复现同一视图
_ = reopened
```


## 用法

```go
tree := perfsample.NewTree()
w, n := tree.AddBatch(samples)        // 合并一批样本
report := perfsample.Reconcile(tree, w, n)

node := tree.Find("main", "serve", "handle")
for _, b := range node.Provenance() { // 按来源/符号版本追溯
    _ = b
}

cmp := perfsample.Compare(oldTree, newTree)
for _, c := range cmp.Shared() {      // 相同路径上的变化
    fmt.Println(c.FullName(), c.DeltaTotal)
}

// 符号补全后重新解释：原始地址/权重不变，只改解释
resolved := perfsample.Reinterpret(samples, perfsample.ResolverFunc(func(f perfsample.Frame) []perfsample.FrameInfo {
    if f.Missing && f.Addr == 0x4010 {
        return []perfsample.FrameInfo{{Module: "libfoo.so", Symbol: "decode"}}
    }
    return nil
}))

// 跨构建版本：函数从旧模块移动到新模块，按映射 + 样本锚点对应
m := perfsample.NewMapping(perfsample.Move{
    OldModule: "libfoo.so", OldSymbol: "decode",
    NewModule: "libbar.so", NewSymbol: "decode",
})
c2 := perfsample.CompareMapped(oldTree, newTree, m)
for _, c := range c2.Shared() {
    fmt.Println(c.Kind, c.FullName(), c.DeltaTotal) // symbol / sample / unmatched
}

// 模块卸载后在同地址重载：按 (地址, 时刻) 取当时的装载关系，区分代次
h := perfsample.NewModuleHistory(
    perfsample.ModuleSpan{Module: "libv1.so", Base: 0x1000, Limit: 0x2000, Start: 0, End: 100, Generation: 1},
    perfsample.ModuleSpan{Module: "libv2.so", Base: 0x1000, Limit: 0x2000, Start: 100, Generation: 2},
)
session := perfsample.NewSession(samples, "raw")
session.Apply(perfsample.Reinterpretation{
    Label: "symbols", History: h,
    Timed: perfsample.TimedResolverFunc(func(f perfsample.Frame, t uint64, span perfsample.ModuleSpan) []perfsample.FrameInfo {
        return []perfsample.FrameInfo{{Symbol: "fn"}} // 模块/代次自动按 span 补上
    }),
})
_ = session.Undo() // 撤回一次，恢复 raw 旧视角

// 两份符号资料：三分类且未匹配样本不丢，两侧权重对得上账
d := perfsample.DiffProfiles(oldTree, newTree, m)
fmt.Println(d.Balanced, // symbol + sample + uncertain 三类权重之和 == 总权重
    d.OldSymbolWeight, d.OldSampleWeight, d.OldUncertainWeight)
```

## 验证与演示

```
go test ./...          # 直接统计的小样例校验
go run ./cmd/perfdiff  # 仅使用内嵌人工样本，展示两版对比
```

测试覆盖：递归两层不被折叠、自身权重之和等于根总权重、缺失符号保留、
跨采集器合并批次的权重对账、两版同路径对比与新增路径识别；以及符号
补全后的权重守恒、内联层次展开、同名函数按模块区分、跨版本函数移动、
样本锚点回退、未匹配路径标注，和 symbol/sample 差异分类；以及多采样器
联合视图中的归属判定（确定/不定/无法归属）、对齐判断的收紧与精确撤回、
覆盖差异与真实权重变化的分离、保存重开复现视图、地址复用与来源缺失展示。
