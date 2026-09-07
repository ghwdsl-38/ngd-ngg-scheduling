# Group5：PRC 周期刷新、修改与删除测试

## 测试目标

本组在真实 envtest API Server/etcd、真实 Algorithm Go Server、真实 Python Worker 和带 Bearer Token 的 Mock Prometheus 之间运行，验证：

1. NGD 创建后触发第一次计算并创建 Active NGG；
2. NGD spec 不变时，由 Refresh Scheduler 触发第二次周期计算；
3. 周期刷新不改变 NGD `metadata.generation`；
4. 消费方写入的 `status.consumer`不会被 PRC SSA 刷新覆盖；
5. 修改 NGD `spec.maxNodes`后，generation 增加并立即重新计算；
6. PRC 更新原 NGG，而不是删除重建；
7. 删除 NGD 后取消周期任务并删除 NGG；
8. 删除完成后 Algorithm 调用次数不再增加；
9. NGG 包含指向正式 NGD 的 OwnerReference；
10. NGG managedFields 中包含 PRC spec/status 两个 SSA Field Manager。

测试将生产默认15秒周期缩短为250毫秒，以验证同一套周期机制而不让自动化测试真实等待多个15秒。生产默认值仍是15秒。

周期完成以第二次 Algorithm 调用和 NGD `status.lastUpdated`变化为准。由于测试周期短于 Algorithm 的15秒 Prometheus采集周期，指标快照未变化时 SSA 得到的 NGG spec 可能完全相同，此时 Kubernetes 不增加 NGG generation，属于正常行为。

## 执行流程

```text
准备1000 Node、Pod、静态拓扑和Prometheus指标
        ↓
启动Mock Prometheus并预热真实Algorithm Server
        ↓
启动envtest API Server/etcd
        ↓
启动完整PRC Application
        ↓
创建包含五级topologyLabels的NGD(minResources对应10个Node，maxNodes=18)
        ↓
等待第一次Algorithm调用和NGG Active(10 Node)
        ↓
模拟消费方SSA写入status.consumer
        ↓
等待250ms周期刷新和第二次Algorithm调用
        ↓
确认NGD generation不变、consumer仍保留
        ↓
修改NGD资源需求为6个Node
        ↓
等待generation=2、重新计算、原NGG更新为6 Node
        ↓
删除NGD
        ↓
确认NGG删除且后续不再周期调用Algorithm
```

## 如何运行

在项目根目录运行：

```bash
make go-test-group5
```

或直接运行：

```bash
. ./scripts/go-test-env.sh
go test -p=1 ./go_test_suites/group5_prc_refresh_lifecycle \
  -run '^TestGroup5_' -v -count=1 -timeout=10m
```

## 输出结果

每次运行在以下目录创建带时间戳的结果：

```text
go_test_suites/group5_prc_refresh_lifecycle/results/<timestamp>/actual/
├── 01-created-ngd.yaml
├── 02-initial-ngg.yaml
├── 03-periodic-ngg.yaml
├── 04-updated-ngd.yaml
├── 05-updated-ngg.yaml
├── 06-consumer-status.yaml
├── 07-algorithm-exchanges.json
├── 08-lifecycle-summary.json
└── python-worker/
```

其中 `08-lifecycle-summary.json`汇总初次计算、周期刷新、修改和删除耗时；结果文件写入不包含在这些业务阶段的测量边界中。

## 本次回归结果

2026-09-04 使用1000 Node Fixture执行新版`topologyLabels`回归，Group5结果如下：

| 阶段 | 结果 | 耗时 |
|---|---|---:|
| 首次创建 | NGD generation=1，NGG Active，10 Node | 336.986 ms |
| 无修改周期刷新 | NGD generation仍为1，consumer保持不变 | 455.210 ms |
| 修改需求 | NGD generation=2，原NGG更新为6 Node | 208.807 ms |
| 删除需求 | NGG删除，后续无新增Algorithm调用 | 14.358 ms |

周期耗时包含配置的250毫秒等待；其他阶段均从对应 Kubernetes 操作开始，到目标 Kubernetes 对象状态可读取为止。不同机器上数值会有波动，正确性以测试断言为准。
