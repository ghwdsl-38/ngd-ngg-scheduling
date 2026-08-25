# NGD/NGG 两层调度 Demo v3

本工程验证“任务级节点组划分 + Pod 级调度”的完整闭环，并同时支持 VolcanoJob 和 Kubernetes Job。

- 第一层：Go PRC 读取 NGD、Node、Pod 和三层拓扑 Label，调用 Go Algorithm API Server；Go 负责 HTTP、静态/Prometheus 缓存和编排，单一 Python Worker 只执行算法函数，生成最多 3 个有序候选组的 NGG；
- 第二层：Volcano 插件或自定义 kube-scheduler 插件只允许当前 `activeGroupRef` 中的 Node；
- 当前组超时且零 Pod 绑定时，PRC 按 Algorithm 原顺序切到下一组；
- 任意任务 Pod 首次绑定后立即锁组，同一 NGD generation 不再跨组；
- 缺少有效 NGG 时两个插件都 Fail Closed，受管 Pod 保持 Pending。

## 整体结构

```mermaid
flowchart TB
    STATIC[Leaf到Border到Core静态JSON] -->|ConfigMap挂载| LLDP[Go LLDP Agent DaemonSet]
    NODE[9个Kind Worker] -->|模拟种子或真实LLDP帧<br/>取得Node到Leaf| LLDP
    LLDP -->|持久化Leaf/Border/Core| LABEL[Node Labels]
    LABEL --> KAPI[Kubernetes API Server]
    KAPI -->|Watch NGD / Node / Pod| PRC[Go PRC<br/>Kubebuilder + controller-runtime]
    NGD[NodeGroupDemand] --> KAPI
    PRC -->|内容Hash标识的静态快照| ALG[Go Algorithm API Server<br/>HTTP + 缓存 + 编排]
    PRC -->|每任务Node使用状态，不在Algorithm缓存| ALG
    PROM[Prometheus] -->|Go后台周期查询| CACHE[Go进程内指标缓存]
    CACHE --> ALG
    ALG -->|完整计算上下文/本地JSON-RPC| PY[单一Python算法Worker]
    PY -->|groupScore降序稳定Top-3| ALG
    ALG -->|保持算法原顺序| PRC
    PRC -->|写入候选组和唯一activeGroupRef| NGG[NodeGroupGrant CR]
    NGG --> V[Volcano NGG插件]
    NGG --> K[kube-scheduler NGG插件]
    V -->|当前组内Filter + Volcano原生流程| TARGET[目标Node]
    K -->|当前组内Filter + kube原生Score/Bind| TARGET
```

Kind 拓扑为 1 个 Control Plane 和 9 个 Worker：

```text
                            core-0
                    /          |          \
             switch-a       switch-b       switch-c
             /  |  \          /  \        /  |  |  \
       worker-01 02 03   worker-04 05  worker-06 07 08 09
```

| 交换机 | Worker 数 | 模拟带宽 | 模拟时延 |
|---|---:|---:|---:|
| switch-a | 3 | 20 Gbps | 1.5 ms |
| switch-b | 2 | 10 Gbps | 5 ms |
| switch-c | 4 | 25 Gbps | 1 ms |

## 关键目录

- `prc/`：正式 Go PRC，包含 controller-runtime Manager、Watch、Snapshot、Algorithm Client 和 NGG 状态机；
- `algorithm_server/`：Algorithm 完整实现目录；`go/` 负责 HTTP、缓存、Prometheus 与进程管理，`python/algorithm_worker/` 负责 Python 算法；
- `topology_agent/`：Go LLDP 采集、静态三层拓扑解析、Node Watch 和 Label 持久化；
- `src/ngd_ngg_demo/`：保留的 Python legacy PRC/LLDP 对照实现和公共领域逻辑，不作为当前镜像入口；
- `plugin/nodegroupgrant/`：Volcano NGG 插件；
- `plugin/kubescheduler/`：kube-scheduler NGG 插件及自定义 scheduler 注册入口；
- `ngg_consumer/formalgrant/`：正式扁平 NGG 的公共解析、时效/生命周期校验和授权合并核心；
- `test_suites/`：3000 Node 三大统一测试组、固定 Fixture、Mock Prometheus、envtest runner 和结果格式；
- `go_test_suites/`：1000 Node 四组标准`go test`，支持独立Expected/Actual/Results和VS Code/Delve本地Debug；
- `config/crd/`：NGD、NGG、NNT CRD；
- `config/manager/`：PRC、Algorithm 和 LLDP Agent 部署；
- `config/kubescheduler/`：第二个 scheduler profile 和 Deployment；
- `manifests/monitoring/`：Prometheus、node-exporter 和 kube-state-metrics；
- `manifests/topology-*`：VolcanoJob 演示；
- `manifests/kubernetes-*`：Kubernetes Job 演示；
- `scripts/08-run-demo.sh`：Volcano Top-3、超时切组和锁组验收；
- `scripts/09-run-kubernetes-demo.sh`：kube-scheduler Fail Closed 和绑定验收；
- `results/generated-ngg-v2/`：实跑导出的 NGD、NGG、三层拓扑 Node YAML 和旧 NNT 对照 YAML。

## 从零运行

所有工程文件、Go 缓存、镜像归档和 Docker 数据都位于 `/mnt/data0`。

使用已有镜像归档：

```bash
cd /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo
make demo-prebuilt
```

从源码重新构建：

```bash
make demo
```

只运行某一条演示：

```bash
make run             # VolcanoJob
make run-kubernetes  # Kubernetes Job + ngg-scheduler
make monitoring      # 单独安装或重新部署监控组件
make monitoring-check # 检查 Prometheus 查询和 Algorithm 指标缓存
```


Algorithm 1000 Node 独立演示：

```bash
make algorithm-1000-demo
```

3000 Node 三组演示采用“计时一遍、证据一遍”：

```bash
make demo-group1       # Algorithm Cold/Warm及Go↔Python原始协议
make demo-group2       # envtest + 真实PRC + 单一normal_create
make demo-group3       # 真实PRC/Algorithm + NGG消费/Binding模拟
make demo-all-groups   # 依次执行以上三组
make demo-show-latest  # 展示最新结果
```

每组结果保存在`test_suites/<组>/runs/<时间戳>/`：`timing-run/`只保存性能结果，`evidence-run/`按原始格式保存输入、中间过程和输出。详细命令和文件含义见`test_suites/README.md`。运行数据由`.gitignore`排除。

新的四组标准Go Test：

```bash
make go-test-group1  # Go调用真实Python Worker
make go-test-group2  # PRC调用真实Algorithm和Mock Prometheus
make go-test-group3  # envtest + PRC Watch + Mock Algorithm
make go-test-group4  # envtest + PRC + 真实Algorithm完整组件链路
make go-test-all     # 串行运行四组
```

每组结果分别保存在`go_test_suites/<组>/results/<run-id>/`，详细输入、输出和计时边界见[`go_test_suites/README.md`](go_test_suites/README.md)。

3000 Node规模性能矩阵（选择1000/800/500/300/100/10个Node，每个规模30次）：

```bash
make benchmark-3000-all
```

详细设计、运行方法、原始样本和正式P50/P95结果见[`go_test_suites/scale_benchmark_3000/README.md`](go_test_suites/scale_benchmark_3000/README.md)。
单独构建或部署：

```bash
make prc-image
make algorithm-image
make lldp-agent-image
make plugin-image
make kube-scheduler-image

make prc
make algorithm
make lldp-agent
make plugin
make kube-scheduler
```

## 镜像归档

```text
images/
├── ngd-ngg-prc-v0.3.0.tar
├── ngd-ngg-algorithm-v0.4.0.tar
├── ngd-ngg-lldp-agent-v0.2.0.tar
├── volcano-ngg-scheduler-v1.15.0.tar
└── ngg-kube-scheduler-v1.35.3.tar
```

PRC 和自定义 kube-scheduler 都使用 Go 1.25 构建，Kubernetes 依赖锁定在 v1.35.3/v0.35.3；Volcano 插件锁定 v1.15.0。

## 当前验证结果

- `make test-acceptance-v2` 三组统一入口通过，规模为 3000 Node、14 类 Prometheus 指标和 42000 个指标样本；
- Algorithm Cold：PRC静态同步至最终响应约1.44秒，Algorithm内部约0.82秒；Warm正式采样30次：PRC发送至接收P95约0.58秒、Algorithm内部P95约0.57秒；两个Core各返回1000个具体Node；
- Mock Prometheus 正确覆盖 Bearer Header 有效、缺失 401、错误 403 和查询超时；
- envtest 中真实 PRC Watch 正式 Cluster-scoped NGD，正常生成 1000 Node 扁平 NGG；Patch、非法 Algorithm 结果、无可行组和 HTTP 503 用例通过；
- 全链路模拟中真实 PRC、Go Algorithm、Python Worker 和公共 formal NGG consumer core 连通；20 个 cold、20 个 warm 和10个 generation 更新 Binding 均未越界；无有效 NGG 时 Binding 数为0；
- 上述全链路是 envtest 和 Binding 子资源模拟，不是实际 Volcano/kube-scheduler、kubelet、CNI 或容器 Running 性能结论；
- Go LLDP Agent DaemonSet `9/9 Ready`，9 个 Worker 均写入 Leaf/Border/Core、带宽、时延、来源和拓扑版本 Label；
- Prometheus、kube-state-metrics 和 9 个 Worker 上的 node-exporter 正常运行；Algorithm 指标缓存包含 9 个 Node，`degraded=false`；
- Volcano 路径返回 `switch-c → switch-a → switch-b`，阻塞 switch-c 后切到 switch-a，4 个 Pod 绑定并锁组；
- Kubernetes Job 在没有 NGG 时 4 个 Pod 全部 Pending；创建 NGD/NGG 后 4 个 Pod 只落在 activeGroup 并进入 Running；
- Algorithm 重启后由 PRC 使用内容 Hash 重新同步静态 Node 快照；
- Kubernetes Job 和 VolcanoJob 均通过直接 Owner UID 与 NGD/NGG 关联。

主要证据：

- `results/generated-ngg-v2/ngg-topology.yaml`；
- `results/generated-ngg-v2/ngg-kubernetes.yaml`；
- `results/topology-placement.txt`；
- `results/kubernetes-fail-closed.txt`；
- `results/kubernetes-placement.txt`；
- `results/algorithm-calculation-result.txt`、`results/algorithm-metrics-cache-status.json`；
- `results/prometheus-query-checks.txt`、`results/prometheus-node-cpu.json`、`results/prometheus-node-memory.json`；
- `results/prc-v2.log`、`results/kube-scheduler.log`、`results/algorithm.log`。

## Prometheus 和 LLDP 边界

Kind 集群已部署 Prometheus、kube-state-metrics 和 node-exporter。node-exporter 只运行在 9 个 Worker 上；Prometheus 同时采集这 9 个 Worker 的主机指标、10 个 Kubernetes Node 的对象状态以及 kubelet/cAdvisor 指标。Go Algorithm 主进程通过集群内地址 `http://prometheus.monitoring.svc.cluster.local:9090` 每 15 秒查询 CPU、内存利用率并保存当前/上一份内存快照，与PRC Reconcile周期一致；不使用 Redis、Kafka或跨 Pod 共享内存。调度时 Go 将选定的指标快照连同静态节点和当次动态状态发给 Python Worker；Prometheus 暂时不可用时保留最后一次有效快照并标记 degraded。

`make demo` 和 `make demo-prebuilt` 已包含监控安装与端到端检查。也可执行 `make monitoring` 重新部署，再执行 `make monitoring-check` 验证 PromQL 返回值以及 Algorithm 缓存的 `enabled=true`、`ready=true`、`degraded=false` 和 `nodeCount=9`。最新 Volcano 实跑生成了非空的 `sha256:...` 指标快照身份，具体值见 `results/algorithm-calculation-result.txt`。

新接口使用 `nodeUsageStates[].inUse`。当前 Go PRC 尚未切换该字段，因此 Algorithm 暂时保留 `/api/v1/node-groups/calculate` 和旧 `schedulerState` 请求适配；这条兼容路径只用于项目平滑衔接，后续修改 PRC 后可删除。

Kind 使用 Go LLDP Agent 的 `Simulated` 模式；物理集群可用 `config/manager/lldp-agent-real-patch.yaml` 切为真实 `0x88CC` 监听。Agent 使用 Cobra CLI，启动时优先从 Node Label 恢复内存状态；仅在 Label 不完整时采集 Node→Leaf，再与 ConfigMap 中 Leaf→Border→Core 静态 JSON 合并并 Patch Node。PRC 优先读取 Label，旧 NNT 仅作为迁移回退。真实网卡、交换机命名和 NET_RAW 权限仍需在目标物理网络验证。
