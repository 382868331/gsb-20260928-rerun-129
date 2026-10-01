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

## 模块重装载、时间化归属与撤回（第三版新增）

采样期间一个模块可能被卸载后重新装载，甚至重定位到不同地址。
同一地址在不同时间可以属于完全不同的代码，因此重新解释必须结合
`Sample.Time` 和当时的模块关系：

- `ModuleSpan` 描述一次装载的模块、构建 ID、地址区间与 `[LoadAt, UnloadAt)`；
- `ModuleHistory` 登记多次装载，`OwnersAt(addr, time)` 按时刻查归属；
- `NewSymbolProfile(...)` 提供各“模块 + 构建”的函数/内联符号；
- `NewTimedResolver(history, profile)` 实现时间感知解析：
  唯一命中时解析到该次装载的构建；零个或多个命中时保留原地址并标记
  存疑，绝不按地址把两个版本混成一个函数；
- `Frame.Addr` 与 `Sample.Time` 始终保留；内联展开的每个逻辑帧也携带原地址。

两份符号资料的差异用 `CompareInterpretations` / `CompareSymbolData` 比较，
每条样本恰好出现一次，差异分为：

- `same`：归属与权重均未变；
- `rename`：原始地址、时刻、模块构建相同，只是名称或内联关系修订；
- `weight`：对应同一份归属，但采样权重真的变化；
- `attribution`：权重归属到不同代码位置（含重定位/符号边界变化）；
- `uncertain`：至少一侧的归属仍无法确认；
- `only-old` / `only-new`：另一侧没有对应样本。

`SymbolDataDiff.OldTotal/NewTotal` 与 `Summarize()` 包含未匹配和存疑样本；
调用方不能只看匹配项来美化占比。模块重定位与内联关系改变同时发生时，
样本权重仍随样本完整迁移，树根总权重严格守恒。

`Session` 保存一层重新解释历史：`Reinterpret` 后可用 `Undo()` 撤回一次，
恢复旧符号视角；原始地址和时刻不会丢失。

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

// 采样期间模块重装载：按地址 + 时刻找到当时的模块/构建。
history := perfsample.NewModuleHistory(
    perfsample.ModuleSpan{Module: "plugin.so", BuildID: "v1", Base: 0x1000, End: 0x2000, UnloadAt: 100},
    perfsample.ModuleSpan{Module: "plugin.so", BuildID: "v2", Base: 0x1000, End: 0x2000, LoadAt: 100},
)
profile := perfsample.NewSymbolProfile(
    perfsample.ModuleSymbols{Module: "plugin.so", BuildID: "v1", Funcs: []perfsample.FuncSym{{Name: "alpha", Start: 0x10, End: 0x20}}},
    perfsample.ModuleSymbols{Module: "plugin.so", BuildID: "v2", Funcs: []perfsample.FuncSym{{Name: "beta", Start: 0x10, End: 0x20}}},
)
session := perfsample.NewSession(raw) // raw 中每条 Sample 带 Time
session.Reinterpret(perfsample.NewTimedResolver(history, profile))
diff := perfsample.CompareInterpretations(oldView, session.Samples())
session.Undo() // 撤回一次，恢复旧视角
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
