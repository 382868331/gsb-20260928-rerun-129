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

## 多采样器联合时间线（第四版新增）

多个采样器记录同一次运行时，`SourceTrack` 保留每个来源自己的本地采样时间、
实际覆盖范围和已知时钟偏移范围（`global = local + offset`）。偏移上下限不
相等时，样本在联合时间线上是一个可能区间，而不是一个被强行选定的点：

- `ReinterpretJoint` 只在整个可能区间都属于同一模块代次时归属热点；可能
  跨越模块装卸、地址复用或符号更新边界的帧聚合为 `(time-uncertain@0x...)`，
  节点以 `TimeUncertain` 明确标示，不会归入任意一次装载；
- `JointSession.AdjustAlignment` 可单独调整某个来源的对齐判断并立即查看
  归属变化；原始样本、当前视角和 `SaveView` 保存的先前结论互不覆盖；
- `UndoLastAlignment` 只恢复最近一次对齐修改影响到的归属，符号资料更新
  不属于对齐撤回，也不会被回滚；
- `CompareJointRuns` 按同一来源比较热点：`Observed*` 是该侧实际观察到的
  全部权重，`Comparable*` 只累计两侧具有相同可能时间区间的观测，
  `DeltaComparable` 才表示真实权重变化；覆盖不同导致的不可比权重仍保留，
  缺少观察绝不补成 0；
- 部分来源缺失时，只在两侧都存在的来源内配对；缺失来源的样本保留在
  已观察权重中，不参与差值；
- `JointSessionState`/JSON 可保存原始样本、来源覆盖、当前偏移、模块历史、
  撤销元数据和已保存视角；符号 resolver 是代码规则，重开时由调用方重新
  提供相同实例，所选视图可从状态复现。

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

// 多采样器：本地时钟允许 [OffsetMin,OffsetMax] 的已知偏移范围
run := perfsample.NewJointRun("run",
    perfsample.NewSourceTrack("cpu", samples, 0, 50),
)
session := perfsample.NewJointSession(run, "bounded-alignment")
session.SetSymbols(history, perfsample.JointResolverFunc(
    func(f perfsample.Frame, local uint64, tr *perfsample.SourceTrack, span perfsample.ModuleSpan) []perfsample.FrameInfo {
        symbol := "oldfn"
        if span.Generation > 1 { symbol = "newfn" }
        return []perfsample.FrameInfo{{Symbol: symbol}}
    },
), "symbols-v3")
session.SaveView("before-manual-alignment")
session.AdjustAlignment("cpu", 0, 0) // 用户把偏移范围收窄为精确对齐
session.UndoLastAlignment()          // 只撤回这次对齐，不回滚符号更新

state, _ := session.SaveState()
data, _ := json.Marshal(state)       // 保存；重开后 RestoreJointSession(state, resolver)
jc := perfsample.CompareJointRuns(oldJointRun, newJointRun)
for _, h := range jc.Hotspots {
    // h.OldObserved/h.NewObserved 保留覆盖差异；h.DeltaComparable 才是真实权重变化
    _ = h
}
```

## 验证与演示

```
go test ./...          # 直接统计的小样例校验
go run ./cmd/perfdiff  # 仅使用内嵌人工样本，展示两版对比
```

测试覆盖：递归两层不被折叠、自身权重之和等于根总权重、缺失符号保留、
跨采集器合并批次的权重对账、两版同路径对比与新增路径识别；以及符号
补全后的权重守恒、内联层次展开、同名函数按模块区分、跨版本函数移动、
样本锚点回退、未匹配路径标注，和 symbol/sample 差异分类。

联合时间线测试覆盖偏移范围跨模块边界时的 time-uncertain 归属、人工调整
对齐后的归属变化与单步撤销、部分来源缺失、覆盖不同与真实权重变化分离，
以及 JSON 保存重开后所选视图和撤销元数据复现。
