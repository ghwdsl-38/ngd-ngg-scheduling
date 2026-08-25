# Kubernetes/Volcano 两层调度中的 Prometheus 指标读取与缓存说明 v2.0（2026-08-19 修订）

> 2026-08-19 更新：指标从 CPU/内存扩展为配置化 CPU、内存和 Node 网络指标；增加 Bearer/TLS；NGG 输出改为联通正式扁平契约。额外 Exporter 和测试执行不在本轮范围内。

## 1. 结论

当前正式实现采用：

```text
Kubernetes硬状态由PRC按任务发送
Node静态信息由Algorithm进程内缓存
Prometheus软指标由Algorithm后台采集并在进程内缓存
不使用Redis、Kafka或跨Pod共享内存
```

Prometheus 不参与资源是否足够的最终判断，只影响候选节点组的软评分。即使 Prometheus 暂时不可用，Kubernetes 的资源、Ready 和 Cordon 等硬约束仍然有效。

## 2. 整体结构

```mermaid
%%{init: {"themeVariables": {"fontSize": "19px"}, "flowchart": {"nodeSpacing": 55, "rankSpacing": 70}}}%%
flowchart TB
    KAPI[Kubernetes API Server]
    PROM[Prometheus]
    NGD[任务NGD]
    PRC[Go PRC]
    STATIC[Algorithm进程内<br/>Node静态快照 current / previous]
    METRIC[Algorithm进程内<br/>Prometheus指标 current / previous]
    ALG[Algorithm API Server]
    NGG[联通正式Cluster-scoped NGG<br/>扁平spec.nodes]
    SCHED[Volcano或kube-scheduler<br/>NGG插件]
    NODE[目标Node]

    KAPI -->|1. Watch Node、Pod、NGD、NNT| PRC
    NGD -->|2. 触发任务级计算| PRC
    PRC -->|3. PUT内容Hash静态快照| STATIC
    PROM -->|4. 后台每15秒批量查询| METRIC
    PRC -->|5. 任务需求 + 当次动态状态| ALG
    STATIC -->|6. 提供指定Hash版本| ALG
    METRIC -->|7. 提供最新可用软指标| ALG
    ALG -->|8. 返回有序Top-3和版本ID| PRC
    PRC -->|9. 取Top-1组，保序写正式NGG| NGG
    NGG -->|10. Informer Watch| SCHED
    SCHED -->|11. Filter、原生Score、Bind| NODE
```

关键点是：任务请求不会同步查询 Prometheus。Prometheus 慢或短暂不可用，不会直接卡住 PRC 的一次 reconcile。

## 3. 三类数据的分工

| 数据 | 来源 | 传输或缓存方式 | 用途 |
|---|---|---|---|
| Node 静态信息 | Node + NNT | PRC 生成内容 Hash，Algorithm 保存 current/previous | 身份、容量、Label、交换机拓扑 |
| Node 动态状态 | Node + Pod | PRC 每次任务计算时随请求发送 | Ready、Cordon、已绑定 Pod Requests |
| 实时利用率 | Prometheus | Algorithm Go 协程周期采集并保存 current/previous | CPU/内存、吞吐、利用率、丢包、错误、重传、链路和可用带宽软评分 |

静态快照包含 Node UID，Node 删除后即使使用同名重建，也不会继承旧授权。

动态资源按下面方式计算：

```text
账面可用资源
= Node.status.allocatable
- 已绑定且未结束Pod的Resource Requests
```

Prometheus 指标不能覆盖这个结果。例如 CPU 实际利用率只有 10%，但 Requests 已占满时，第一层仍然不能把该 Node 当作资源充足。

## 4. Algorithm 指标缓存

实现文件为：

```text
algorithm_server/go/metrics.go
```

缓存行为：

1. Algorithm Go 主进程启动时创建后台刷新任务；
2. 每隔 `PROMETHEUS_REFRESH_SECONDS` 按 `prometheus_metrics.json` 执行 CPU、内存和网络 instant query；
3. 使用 `PROMETHEUS_NODE_LABEL` 把结果映射到 Node name；
4. 生成内容 Hash 形式的 `metricSnapshotId`；
5. 保存当前版本和上一个不同版本；
6. 算法请求只读内存副本，不调用 Prometheus；
7. 新一轮查询失败时保留最后一次成功快照；
8. CPU/内存必需查询失败时保留旧快照并降级；可选网络查询失败时只记录 coverage/warning；
9. 快照超过 `PROMETHEUS_STALE_SECONDS` 后继续可读，但响应标记 `degraded=true`。

环境变量：

| 变量 | 默认值 | 含义 |
|---|---|---|
| `PROMETHEUS_URL` | 空 | 空表示关闭指标采集 |
| `PROMETHEUS_REFRESH_SECONDS` | `15` | 后台刷新周期，与PRC Reconcile周期一致 |
| `PROMETHEUS_STALE_SECONDS` | `90` | 超过该时间视为陈旧 |
| `PROMETHEUS_NODE_LABEL` | `node` | Prometheus 结果中的节点名 Label |
| `PROMETHEUS_METRICS_CONFIG_FILE` | 内嵌 `prometheus_metrics.json` | 覆盖指标目录 |
| `PROMETHEUS_BEARER_TOKEN_FILE` | 空 | 推荐：从 Secret 文件读取 Token |
| `PROMETHEUS_BEARER_TOKEN` | 空 | 直接 Token，与文件方式互斥 |
| `PROMETHEUS_CA_FILE` | 空 | Prometheus HTTPS 自定义 CA |
| `PROMETHEUS_TLS_SERVER_NAME` | 空 | TLS 名称校验 |

部署示例：

```yaml
env:
- name: PROMETHEUS_URL
  value: http://prometheus-server.monitoring.svc:80
- name: PROMETHEUS_REFRESH_SECONDS
  value: "30"
- name: PROMETHEUS_STALE_SECONDS
  value: "90"
- name: PROMETHEUS_NODE_LABEL
  value: node
- name: PROMETHEUS_BEARER_TOKEN_FILE
  value: /var/run/secrets/prometheus/token
```

现场 Prometheus 的指标名和 Label 往往不同，接入时必须先验证查询结果是否确实包含 Kubernetes Node name，不能直接照搬默认 PromQL。

## 5. 评分方式

第一层先执行硬过滤并按三层拓扑分组：

- Node Ready；
- Node 未 Cordon；
- Node Selector 满足；
- `allocatable - requests` 能容纳任务最小 Pod 集合；
- 按 `leafSwitch → borderSwitch → coreSwitch` 使用 NarrowestFit，选择能容纳整个任务的最窄层级。

之后计算软评分：

```text
nodeScore = 15% × 资源基础分 + 70% × 实时综合指标分
groupScore = 85% × 组内平均nodeScore + 15% × 静态拓扑质量分
```

实时综合指标分按 profile 对 CPU、内存、网络利用率、收发丢包率、收发错误率、TCP 重传率、链路 Up 比例和可用带宽加权；只有当前 Node 实际存在的指标参与归一化，缺失可选项不伪造为 0。

没有有效 Prometheus 指标时，`nodeScore` 只使用账面空闲率，同时响应带：

```json
{
  "metricSnapshotId": "metrics-disabled",
  "degraded": true,
  "warnings": ["Prometheus metrics cache is disabled"]
}
```

Algorithm 仍返回最多 3 个候选组。PRC 校验后选排名第一的组，将组内 Node 按原顺序写入联通正式 NGG 的 `spec.nodes`。`source=normal` 时不改 Node score；`source=degraded/disabled` 时按正式契约写中性分 50。

## 6. 故障处理

| 情况 | 行为 |
|---|---|
| `PROMETHEUS_URL` 未配置 | 关闭指标评分，使用 Kubernetes 硬状态，标记 degraded |
| 第一次采集尚未完成 | 使用硬状态，标记 metric snapshot not ready |
| 刷新失败但有旧快照 | 保留最后一次成功快照，不阻塞调度 |
| 快照超过 stale 时间 | 使用旧快照并明确标记 stale/degraded |
| 某个 Node 缺少可选网络指标 | 保留该 Node，只用实际存在的指标重新归一化权重 |
| Kubernetes 硬状态不足 | 无论 Prometheus 多空闲都必须过滤 |

如果 Algorithm 整体不可用，PRC 将 NGD 标记为 `Degraded` 并重试；两个第二层插件在没有有效 NGG 时统一 Fail Closed。

## 7. 当前 Demo 状态

当前 Kind 集群没有部署 Prometheus，因此运行状态为：

```text
metricsEnabled=false
metricsReady=false
metricSnapshotId=metrics-disabled
metricsDegraded=true
```

但缓存实现和以下测试已经完成：

- Prometheus 指标能够改变软评分；
- 指标不会改变 Ready/资源等硬过滤；
- 刷新失败后保留最后一次有效快照；
- 无 Prometheus 时返回明确的降级信息。

要在联通目标集群启用，必须确认 PromQL、Node Label、Bearer/TLS 和 Network Interface 排除规则。当前 PRC 已输出联通正式 NGG；两个调度插件读取正式扁平 NGG 的改造和全链路测试留到下一阶段。

## 8. 当前方案为什么不使用 Redis

正式第一版固定 Algorithm 单副本，并把静态快照和指标快照都保存在同一进程内，因此没有跨副本共享状态需求。这样可以减少：

- Redis 部署和运维；
- 快照序列化与跨组件一致性问题；
- Redis 故障对调度链路的影响；
- 与本次 Demo 无关的恢复逻辑。

如果未来必须把 Algorithm 扩为多副本，需要重新决定一致性策略；这属于后续扩展，不在当前第一版中预埋 Redis。
