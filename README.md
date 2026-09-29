# perfcore — 性能采样分析核心（第一版）

把人工/任意采集器产出的加权调用栈样本汇总成**可追溯的执行树**，并支持比较两次运行。
分析核心只依赖采集器无关的数据模型，后续新增采集器只需产出相同格式的批次即可复用全部分析结果。

本项目不读取任何系统进程：所有样本均为内置的手工小样例（`demo/data.go`）。

## 目录结构

- `model/` — 采集器无关的数据模型：`Frame`、`Sample`、`Batch`、`Source`。
  - 栈按**叶在前**（index 0 为最内层/正在运行的帧）排列。
  - 每个 `Frame` 带稳定 `Key`、符号名、`Resolved` 标记、位置与 `SymbolVersion`。
  - 每个 `Batch` 记录采集器、程序版本、符号表版本等来源信息。
- `tree/` — 执行树构建与权重重核对。
- `compare/` — 按相同调用路径比较两个 profile。
- `demo/` + `main.go` — 手工样本与命令行演示，可导出 JSON。

## 关键语义

**递归不折叠。** 节点身份 = 父节点 + 帧 `Key`，因此 `main -> fib -> fib -> fib`
是三个不同节点。只有最内层（叶）发生点获得自身权重：

```
- fib        self=0 incl=4   # 经过它的所有样本（包含子调用的耗时）
  - fib      self=0 incl=4
    - fib    self=4 incl=4   # 叶帧：自身耗时
```

- `SelfWeight`（自身耗时）：叶恰好是该节点的样本权重之和。
- `InclusiveWeight`（包含耗时）：栈经过该节点的所有样本权重之和。
- `FlatByFrame()` 在帧维度聚合时才会把同名递归出现合并（树中永不合并）。
- 遍历辅助 `Walk` 在每条路径上单独计算 `Recurrence`（该 key 在路径上第几次出现）。

**缺失符号不丢样本。** 无法符号化的帧以 `Resolved=false` + 稳定 key（如原始 PC）
照常入树；`Profile.UnresolvedSelf` 单独统计其叶权重。

**合并批次后总权重必须对上。** `Build` 结束时 `reconcile()` 用多条独立路径交叉核对：
各批次权重之和 == 全树根层包含权重之和 == 所有节点 `SelfWeight` 之和 == 总权重；
叶归属记录数 == 样本数。任一不一致即返回错误。

**可追溯。** 每个节点保存 `Contributions`（批次 ID、样本 ID、权重、是否作为叶帧），
任何聚合数字都能回溯到具体样本；`Profile.Sources` 保留采集器与符号版本。

## 两次运行比较

`compare.Diff` 用帧 key 路径匹配两个 profile，对每条共享路径给出：
自身/包含权重的前后值、差值、各自占总权重百分比、百分点变化，并标注
`hotter`/`cooler`/`same`；仅出现在一侧的路径进入 `added`/`removed`。

演示场景（`demo/data.go`）：v1 → v2 中 `parseQuery` 变便宜（6→1）、
`fetchUser` 占比升高、`validate` 路径消失、新增 `cacheLookup` 路径。

## 运行

```sh
go test ./...              # 手算小样例的直接统计核对
go run .                   # 打印两棵树、权重核对、两版对比
go run . --json out.json   # 额外导出可追溯的 JSON 结果
```

## 为新采集器接入

新增采集器不需要改动分析核心：把观测结果编码为 `model.Batch`
（填好采集器名、程序/符号版本与叶在前的栈），交给 `tree.Build` 即可得到
执行树、重核对结果与 `compare.Diff` 能力。

