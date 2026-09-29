# perfsample：性能采样分析核心（第一版）

把人工/采集器产出的调用栈样本聚合成可追溯的执行树，并支持比较同一程序
两次运行在相同调用路径上的变化。核心不读取任何系统进程，只消费调用方
传入的 `Sample`。

## 模型

- `Frame`：栈的一层。`Stack[0]` 为最外层，最后一层为采样点。符号缺失时
  置 `Missing=true` 并带原始 `Addr`。
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
```

## 验证与演示

```
go test ./...          # 直接统计的小样例校验
go run ./cmd/perfdiff  # 仅使用内嵌人工样本，展示两版对比
```

测试覆盖：递归两层不被折叠、自身权重之和等于根总权重、缺失符号保留、
跨采集器合并批次的权重对账、两版同路径对比与新增路径识别。
