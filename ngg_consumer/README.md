# 正式 NGG 消费核心

该 Go 包实现与调度框架无关的正式 `scheduling.platform.example.io/v1alpha1 NodeGroupGrant` 解析和授权集合逻辑：校验 scheduler、契约版本、生命周期、source 和时效，解析扁平 Node/score，并在合并多个 NGG 时保留同名 Node 的最高分。

`test_suites/group3_full_chain` 的消费与 Binding 模拟器使用此包。后续正式 Volcano 和 kube-scheduler 插件应复用该包，避免分别实现不一致的 CR 规则。

