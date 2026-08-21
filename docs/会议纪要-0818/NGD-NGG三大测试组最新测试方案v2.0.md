# NGD-NGG 三大测试组最新测试方案 v2.0

> **版本提示（2026-08-20）**：本文保留为上一轮设计记录。当前实现已改为每组独立执行`timing-run`和`evidence-run`；第二组只保留`normal_create`；第三组主演示只保留Cold/Warm；NGD/NGG按YAML输出，并新增第一组Go↔Python原始JSONL证据。当前运行命令和目录以`test_suites/README.md`为准。

> 版本：v2.0  
> 日期：2026-08-19  
> 状态：测试设计与实施基线。三组统一执行器均已实现并完成首轮通过；具体结果见第 15 章。  
> 依据：0818 会议要求、联通正式 NGD/NGG CRD、当前 PRC 与 Algorithm Server 实现。

## 1. 文档目的

本文统一原有两份测试大纲的口径，将正式验收划分为三个可独立启动、独立判断、独立输出结果的测试组：

1. Algorithm 完整与 3000 Node 规模测试；
2. PRC 正式接口完整测试；
3. 无真实 Kubernetes 集群的全链路模拟测试。

三组统一使用 3000 Node 规模。但这些 Node 是 JSON 数据或 Kubernetes API 对象，不是 3000 台真实物理机、虚拟机或 Kind Worker。

## 2. 已确定的总体边界

### 2.1 不需要真实 Kubernetes 集群

正式三组测试不要求：

- 部署真实大型 Kubernetes 集群；
- 创建 3000 台真实 Worker；
- 启动 3000 个 kubelet 或容器运行时；
- 将测试规模限制在当前 9 Worker Kind Demo；
- 启动真实 Volcano Scheduler 或 kube-scheduler；
- 要求业务容器真正进入 Running。

第二、第三组使用 controller-runtime envtest 提供轻量 Kubernetes API Server 和 etcd，用于验证 CRD、List/Watch、Status 和 Binding 等 API 语义。它不是实际业务集群。

### 2.2 3000 Node 的具体含义

| 测试组 | 3000 Node 的存在形式 | 是否需要真实机器 |
|---|---|---|
| Algorithm 完整测试 | Mock PRC 构造的 JSON 静态快照和动态状态 | 否 |
| PRC 完整测试 | envtest API Server 中的 3000 个 Node 对象 | 否 |
| 全链路模拟测试 | envtest API Server 中的 3000 个 Node 对象 | 否 |

### 2.3 以联通正式 CRD 为验收接口

正式测试使用：

~~~text
apiVersion: scheduling.platform.example.io/v1alpha1
kind: NodeGroupDemand

apiVersion: scheduling.platform.example.io/v1alpha1
kind: NodeGroupGrant
~~~

两种资源均为 Cluster-scoped。

正式 NGG 使用一个扁平节点列表：

~~~yaml
spec:
  schedulerName: volcano
  version: "v1"
  timestamp: "2026-08-19T00:00:00Z"
  source: normal
  demandRef: demand-001
  nodes:
  - name: worker-0001
    score: 95
  - name: worker-0002
    score: 94
~~~

正式验收不混入当前 Demo 的以下语义：

~~~text
candidateNodeGroups
activeGroupRef
Trying / Locked / Exhausted
Top-3 失败切组
~~~

当前 Top-3 Demo 可以保留为兼容回归测试，但不属于本方案的正式三组验收口径。

### 2.4 正式模式的 Top-1 与 1000 个候选 Node

本方案统一定义：

~~~text
Algorithm 产生按分数排序的候选结果
    → PRC 使用第一个有效候选结果
    → 构造一个正式 NGG
    → spec.nodes 保存约 1000 个按 score 降序排列的 Node
~~~

“1000 个候选 Node”表示最终写入一个正式 NGG 的 spec.nodes 的节点数，不是三个候选组各包含 1000 个 Node。

### 2.5 数据来源

- Node 静态数据：名称、UID、Label、Allocatable、Leaf/Border/Core、带宽和时延；
- Node 动态状态：PRC 随每次请求完整发送，不使用增量数据；
- Prometheus 指标：Go Algorithm Server 周期读取并缓存；
- 当前必测动态指标：CPU、内存以及 5.3.1 列出的 node-exporter 网络指标；
- 静态带宽和时延仍来自 Node 拓扑，不冒充 Prometheus 主动探测指标；
- GPU、交换机侧指标和需要额外 Exporter 的主动网络探测不属于当前必测范围。

## 3. 三大测试组总体关系

~~~mermaid
flowchart TB
    G1["第一组<br/>Algorithm完整与3000 Node规模测试"]
    G2["第二组<br/>PRC正式接口完整测试"]
    G3["第三组<br/>无真实K8s全链路模拟测试"]
    R["正式验收结论"]

    G1 -->|"证明Algorithm可独立处理3000 Node"| G2
    G2 -->|"证明正式NGD可生成正式NGG"| G3
    G3 -->|"证明后续消费只使用NGG授权Node"| R
~~~

| 测试组 | Mock 内容 | 真实运行内容 | 最终输出 |
|---|---|---|---|
| Algorithm 完整测试 | Mock PRC、Mock Prometheus | Go Algorithm、缓存、Prometheus Client、Python Worker、算法流水线 | Allocation Response 与性能报告 |
| PRC 完整测试 | Mock Node/Pod、Mock Algorithm HTTP | envtest API/etcd、PRC Manager、List/Watch、正式 NGD/NGG 读写 | 正式扁平 NGG |
| 全链路模拟测试 | Mock Node/Pod、Mock Prometheus、调度执行环境 | envtest、PRC、Algorithm、Python Worker、NGG 消费核心逻辑 | 模拟 Binding 与消费状态 |

## 4. 公共测试数据

### 4.1 规模和期望数量

| 数据 | 目标数量 |
|---|---:|
| Node 静态数据 | 3000 |
| Node 动态状态 | 3000 |
| Prometheus CPU 指标 | 3000 |
| Prometheus 内存指标 | 3000 |
| 资源、状态和 Label 过滤后 | 固定 Fixture 决定，建议 2000～2500 |
| 拓扑约束后 | 约 1000 |
| 正式 NGG | 1 |
| 正式 NGG spec.nodes | 约 1000 |

“约 1000”不能依赖每次不同的随机结果。数据生成器必须使用固定随机种子，并固化每个阶段的精确期望数量。

### 4.2 模拟拓扑

~~~mermaid
flowchart TB
    DC["DataCenter"]
    C1["Core-01"]
    C2["Core-02"]
    B1["Border-01"]
    B2["Border-02"]
    B3["Border-03"]
    B4["Border-04"]
    L1["Leaf-001 … Leaf-038"]
    L2["Leaf-039 … Leaf-075"]
    L3["Leaf-076 … Leaf-113"]
    L4["Leaf-114 … Leaf-150"]
    N1["Node-0001 … Node-0750"]
    N2["Node-0751 … Node-1500"]
    N3["Node-1501 … Node-2250"]
    N4["Node-2251 … Node-3000"]

    DC --> C1
    DC --> C2
    C1 --> B1
    C1 --> B2
    C2 --> B3
    C2 --> B4
    B1 --> L1
    B2 --> L2
    B3 --> L3
    B4 --> L4
    L1 --> N1
    L2 --> N2
    L3 --> N3
    L4 --> N4
~~~

上图只表示数量级。正式 Fixture 必须生成无歧义的 Node → Leaf → Border → Core 关系，一个 Leaf 只能归属一个 Border。

### 4.3 三类 Node 数据

~~~text
静态数据
├─ Node name / UID
├─ labels / annotations
├─ allocatable CPU / memory
└─ Leaf / Border / Core / bandwidth / latency

动态状态
├─ Ready
├─ Unschedulable
├─ Bound Pod requests
└─ request-scoped inUse / requested resources

Prometheus 指标
├─ CPU/内存：cpuUsageRatio、memoryUsageRatio
├─ 吞吐：收/发 bytes/s、收/发 packets/s
├─ 质量：收/发丢包率、收/发错误率、TCP重传率
├─ 链路：Link Up、网络利用率、可用带宽
└─ 访问：标准 /api/v1/query + Bearer Token认证
~~~

### 4.4 数据共用原则

- 三组使用同一个生成器、同一个固定随机种子和同一份数据 Schema；
- 第一组将数据转换成 Algorithm HTTP 协议；
- 第二、第三组将数据转换成 Kubernetes Node/Pod 对象；
- 三组的期望过滤结果和最终候选 Node 集必须可以相互对照；
- 测试不得绕过公开协议直接向 Algorithm 内存缓存写数据。

## 5. 第一组：Algorithm 完整与 3000 Node 规模测试

### 5.1 目标

不启动 PRC 和 Kubernetes，由 Mock PRC 使用真实 HTTP 协议驱动 Algorithm Server，验证：

- Go HTTP API；
- Node 静态快照 Hash 和双版本内存缓存；
- Prometheus 周期查询、认证、解析、缓存和降级；
- Go 与长期运行 Python Worker 的 JSON Lines 通信；
- Requirement → Topology → LoadBalance 流水线；
- 3000 Node 下的正确性、确定性、内存和性能。

### 5.2 测试示意图

~~~mermaid
sequenceDiagram
    participant T as 测试驱动器/Mock PRC
    participant A as Go Algorithm API
    participant C as Go内存缓存
    participant M as Mock Prometheus HTTP
    participant W as Python Worker

    T->>A: PUT静态快照<br/>3000 Node
    A->>A: 校验内容Hash、UID和拓扑
    A->>C: 保存current/previous快照
    A-->>T: acceptedSnapshotId + nodeCount=3000
    A->>M: 周期GET PromQL + Bearer Token
    M-->>A: 3000 Node CPU/内存Vector
    A->>C: 保存指标快照
    T->>A: POST计算请求<br/>3000条动态状态+任务需求
    A->>C: 按snapshotId取静态和指标快照
    A->>W: JSON Lines完整计算上下文
    W->>W: Requirement过滤
    W->>W: Topology分组和约束
    W->>W: LoadBalance评分和稳定排序
    W-->>A: 候选结果
    A-->>T: Allocation Response
~~~

### 5.3 Mock 与真实边界

| 类型 | 内容 |
|---|---|
| Mock | PRC 请求驱动器、Prometheus HTTP Server、3000 Node 数据 |
| 真实 | Go Algorithm、HTTP、静态/指标缓存、Prometheus Client、Python Worker、算法流水线 |
| 不启动 | PRC、Kubernetes API、Volcano、kube-scheduler |

Mock Prometheus 必须是真实 HTTP Server，不能让 Algorithm 直接读取本地指标 JSON。

#### 5.3.1 Mock Prometheus必须与真实Prometheus共用指标契约

Mock与真实环境必须共用同一份指标目录。指标目录至少定义：内部字段名、PromQL、单位、Node标签名、数值范围、是否必需和缺失值策略。Mock不能根据“查询字符串里是否出现cpu”之类的模糊判断临时选择数据，也不能返回Algorithm内部对象代替Prometheus响应。

当前不增加额外网络采集器，除CPU和内存外，第一阶段从现有node-exporter增加以下网络指标：

| 内部字段 | 单位 | Prometheus原始序列或计算方式 |
|---|---|---|
| networkReceiveBytesPerSecond | bytes/s | node_network_receive_bytes_total的5分钟rate |
| networkTransmitBytesPerSecond | bytes/s | node_network_transmit_bytes_total的5分钟rate |
| networkReceivePacketsPerSecond | packets/s | node_network_receive_packets_total的5分钟rate |
| networkTransmitPacketsPerSecond | packets/s | node_network_transmit_packets_total的5分钟rate |
| networkReceiveDropRatio | ratio | receive_drop rate / receive_packets rate |
| networkTransmitDropRatio | ratio | transmit_drop rate / transmit_packets rate |
| networkReceiveErrorRatio | ratio | receive_errs rate / receive_packets rate |
| networkTransmitErrorRatio | ratio | transmit_errs rate / transmit_packets rate |
| tcpRetransmitRatio | ratio | Tcp_RetransSegs rate / Tcp_OutSegs rate |
| networkLinkUpRatio | ratio | 参与统计的物理接口中UP接口比例 |
| networkUtilizationRatio | ratio | 收发字节速率 / 网卡额定字节速率 |
| availableBandwidthBytesPerSecond | bytes/s | 网卡额定速率减去当前收发速率 |

RTT、主动探测丢包、目标网络可达性等需要Blackbox Exporter或Node探测Agent的指标，本轮不实现、不模拟成已经存在的真实采集结果。

所有网络PromQL必须先在Prometheus侧按Node聚合，返回“一台Node、一个字段、一个标量”；不得把带device维度的多条Vector直接写入当前Node级指标缓存。需要排除lo、veth、cali、cni、flannel、docker、br等非业务接口，并由正式配置维护排除表达式。

#### 5.3.2 HTTP协议必须真实

Algorithm必须通过正式Prometheus Client发出：

~~~http
GET /api/v1/query?query=<URL编码后的PromQL>
Accept: application/json
Authorization: Bearer test-prometheus-token
~~~

Mock必须返回标准instant query vector：

~~~json
{
  "status": "success",
  "data": {
    "resultType": "vector",
    "result": [
      {
        "metric": {"node": "worker-0001"},
        "value": [1787040000, "0.237"]
      }
    ]
  }
}
~~~

测试数据中的时间戳、数值字符串、node标签和resultType必须与真实Prometheus响应一致。Mock保存的本地JSON只是生成响应的Fixture，Algorithm不得直接读取该文件。

#### 5.3.3 Mock Prometheus是测试基础设施

Mock使用固定Bearer Token并提供与真实Prometheus一致的HTTP响应。它是Algorithm测试的指标数据源，不单独作为Demo或Case；只要Algorithm无法取得完整指标缓存，业务Case就直接失败。

| 场景 | Mock响应 | Algorithm预期行为 |
|---|---|---|
| Token正确 | HTTP 200 + vector | 更新当前指标快照 |
| Header缺失 | HTTP 401 | 保留上一份成功快照并记录认证错误 |
| Token错误 | HTTP 403 | 保留上一份成功快照并记录认证错误 |
| 请求超时 | 客户端超时 | 保留上一份成功快照并进入降级判断 |

生产配置同时支持直接Token和Token文件，测试运行使用Token文件。测试日志和结果文件不得输出Token原文。401/403/超时属于Mock基础设施能力，不再单独统计Demo时间。

#### 5.3.4 真实与Mock一致性检查

- 同一个指标配置文件同时用于真实Prometheus和Mock测试；
- Mock按完整PromQL精确匹配响应，不接受未知查询；
- 每条查询检查HTTP方法、路径、query参数、Accept和Authorization；
- Ratio指标按0～1校验，bytes/s、packets/s和milliseconds等绝对值不得统一截断到0～1；
- 必需指标缺失导致该轮指标快照失败，可选网络指标缺失记录coverage和warning；
- 缺失、NaN、正负Inf、重复Node和未知Node均有明确处理和测试；
- 指标快照记录指标名、单位、Node覆盖数、采集时间和内容Hash；
- 后续接入真实Prometheus时只替换URL、认证和TLS配置，不改变PromQL目录、响应解析和评分字段。

### 5.4 公开接口

~~~text
PUT  /internal/v1/node-static-snapshots/{snapshotId}
POST /api/v1/allocate
GET  /healthz
GET  /readyz
GET  /internal/v1/cache/status
~~~

### 5.5 子阶段

| 阶段 | 输入 | 输出 | 留存证据 |
|---|---|---|---|
| A1 数据生成 | 种子、拓扑规则 | 3000 Node Fixture | 数量、Hash，不计业务时间 |
| A2 服务启动 | Algorithm、Worker、Mock Prometheus | HTTP Ready | 仅作为前置，不计业务时间 |
| A3 Cold静态PUT/指标刷新 | 静态快照、PromQL | 缓存Ready | Cold计入`prcSendToReceiveMs`；Warm作为前置不计时 |
| A4 动态 POST | 3000条状态、任务需求 | Allocation Response | Cold/Warm分别统计`prcSendToReceiveMs` |
| A5 Algorithm处理 | HTTP请求 | 候选结果Ready | 统计服务端`algorithmProcessingMs` |
| A6 Requirement/Topology/LoadBalance | 调试请求 | 三阶段输入输出 | 另发不计时Trace请求，业务计时结束后写盘 |

### 5.6 必测用例

正常用例：

- 3000 Node 快照 Hash 被正确接受；
- 相同快照重复 PUT 幂等；
- CPU、内存和网络指标与 3000 Node 正确匹配，绝对值不得被错误截断到 0～1；
- 相同输入多次运行得到完全相同的节点集、score 和顺序；
- 动态状态改变后结果按固定预期改变；
- Algorithm 不修改输入 Fixture。

异常用例：

- URL Hash 与快照内容不一致；
- 重复或未知 Node UID；
- Prometheus Token 缺失、错误或返回 401/403；
- Prometheus 超时、空 Vector、非 Vector 和快照过期；
- requireMetrics=false 时降级返回；
- requireMetrics=true 时返回可重试错误；
- Python Worker 返回非法 JSON、错误消息 ID、超时或退出。

### 5.7 通过条件

- 静态、动态和指标数据均覆盖 3000 Node；
- 流水线顺序是 Requirement → Topology → LoadBalance；
- 结果不包含未知、重复、NotReady、Unschedulable 或明确占用的 Node；
- 最终候选数量符合固定 Fixture，目标约 1000；
- 纯 Python 算法优先不超过 500 ms，复杂情况不超过 1 s；
- 预热 5 次，正式执行至少 30 次；
- 输出 min、mean、P50、P95、P99 和 max；
- Cold与Warm分开报告；每种场景只报告两种业务时间：PRC发送到接收和Algorithm服务端处理；
- 时间单位统一为毫秒`ms`，缓存准备、Trace、环境准备和写盘不计时；
- HTTP请求和Go/Python单条消息小于32 MiB；
- 返回PRC的响应小于4 MiB。

## 6. 第二组：PRC 正式接口完整测试

### 6.1 目标

不启动真实 Algorithm、Python Worker、Prometheus 和业务 Kubernetes 集群，验证真实 PRC Manager 完成：

~~~text
正式Cluster-scoped NGD
    → Watch / Work Queue / Reconcile
    → 读取3000 Node和Bound Pod
    → 构造静态快照和完整动态状态
    → 调用Mock Algorithm HTTP
    → 校验返回结果
    → 生成正式扁平NGG
    → 更新NGD/NGG Status
~~~

### 6.2 测试示意图

~~~mermaid
sequenceDiagram
    participant T as 测试驱动器
    participant K as envtest API Server/etcd
    participant P as 真实PRC Manager
    participant A as Mock Algorithm HTTP

    T->>K: 安装正式NGD/NGG CRD
    T->>K: CREATE/PATCH 3000 Node和Bound Pod
    T->>A: 启动固定响应HTTP Mock
    T->>P: 启动Manager并等待Cache Sync
    T->>K: CREATE正式Cluster-scoped NGD
    K-->>P: Watch事件进入Work Queue
    P->>K: LIST Node/Pod并读取拓扑和占用
    P->>A: PUT静态快照
    A-->>P: acceptedSnapshotId + nodeCount
    P->>A: POST正式计算请求
    A-->>P: 固定Top-1结果，约1000 Node
    P->>P: 保留score和顺序<br/>构造扁平spec.nodes
    P->>K: CREATE/PATCH正式NGG
    P->>K: UPDATE NGD/NGG Status
    T->>K: GET/Watch并执行Schema和内容断言
~~~

### 6.3 Mock 与真实边界

| 类型 | 内容 |
|---|---|
| Mock | 3000 Node/Pod 对象、Algorithm HTTP Server及固定响应 |
| 真实 | envtest API/etcd、正式CRD、PRC Manager、Informer/Cache、Watch/Queue/Reconcile、HTTP Client、NGD/NGG读写 |
| 不启动 | 真实Algorithm、Python Worker、Prometheus、Volcano、kube-scheduler、kubelet |

Mock Algorithm 必须实现 PRC 实际使用的公开接口：

~~~text
PUT  /internal/v1/node-static-snapshots/{snapshotId}
POST /api/v1/allocate
~~~

如果兼容期 PRC 仍调用 /api/v1/node-groups/calculate，测试必须显式标记为 Demo 兼容协议；正式模式最终统一到 /api/v1/allocate。

### 6.4 PRC 修改前置关系

截至 2026-08-19，本轮实现后 PRC 已同时 Watch 正式 Cluster-scoped NGD 和旧任务级 namespaced NGD，完成正式 `/api/v1/allocate` 调用、Top-1 截取和正式扁平 NGG 输出。正式资源池请求使用明确的 `requestMode=resourcePool`，不虚构 PodSet。

本轮已经完成：

1. Watch Cluster-scoped 正式 NGD；
2. 将正式 NGD 转换为 Algorithm 内部请求；
3. 生成 3000 Node 静态快照和请求级完整动态状态；
4. 使用正式 Algorithm HTTP 协议；
5. 选择第一个有效结果并构造扁平 spec.nodes；
6. 填写 schedulerName、version、timestamp、source、demandRef；
7. 更新 NGD phase、grantRef、resolvedNodeCount、lastUpdated、message；
8. 更新 NGG phase 和 resolvedCapacity；
9. 增加 Cluster-scoped RBAC；
10. 保留当前 Demo CRD 回归模式，避免正式字段侵入算法核心。

正式 NGD 当前没有 algorithms 字段。因此正式模式的算法流水线顺序和默认参数由 PRC 适配层给出，测试不假设 NGD 含有 CRD Schema 中不存在的字段。

正式 NGD 与当前任务级 Algorithm 请求并不完全等价，测试实现前必须固定以下转换规则：

| 正式 NGD 字段 | 测试中的预期语义 |
|---|---|
| nodeSelector | Node Label 硬过滤 |
| maxNodes | 最终正式 NGG 的节点数量上限 |
| minResources | 候选节点集合的总 CPU/内存下限，不伪造成单 Pod 请求 |
| quota | 资源池授权上限，由正式适配层按约定处理 |
| minThroughput | 节点吞吐量硬过滤 |
| crossClusterAffinity / intraClusterAffinity | 汇聚层或对应拓扑层的硬约束 |
| networkReachability | 外部目标可达性硬过滤 |
| preferredSubnet | 排序偏好，不作为硬过滤 |

当前 Python Requirement 主要处理 podSets 和 resourcesPerPod。正式资源池需求没有 Pod 数量，因此不能在测试里虚构 PodSet；需要在 Algorithm 内部请求中增加正式资源池约束模式，或在独立适配插件中处理上述字段。该协议应在修改 PRC 前先通过 Fixture 固定。

正式 NGG 拓扑输出建议采用明确映射并通过测试断言：Leaf 对应 accessSwitch，Border 对应 convergenceSwitch；dataCenter、subnet、rack、hops 和 networkReachable 从约定的 Node Label/Annotation 或静态拓扑数据取得。Core 信息是否需要扩展到正式 CRD，应作为接口版本问题单独确认，不能静默丢失后仍声称完整输出三层拓扑。

### 6.5 子阶段

| 阶段 | 输入 | 输出 | 留存证据 |
|---|---|---|---|
| P1 envtest/对象/PRC准备 | API、CRD、3000 Node/Pod | Cache Sync | 前置证据，不计业务时间 |
| P2 NGD Watch | 正式NGD | Reconcile开始 | 业务T0，由ReconcileObserver记录 |
| P3 Node/Pod读取和快照构造 | API对象 | 静态快照、动态请求 | 输入输出文件，不单独计时 |
| P4 Mock PUT/POST | PRC请求 | Mock响应 | 内存留痕，T1后写盘 |
| P10 响应校验 | Mock结果 | 合法Top-1列表 | ID、Hash、UID、重复和顺序 |
| P11 NGG构造 | 候选Node | 正式扁平NGG | Node数、大小、耗时 |
| P7 NGG/Status写入 | NGG和Status | API持久化结果 | 全部Ready为业务T1 |

### 6.6 正常流程

- PRC 通过 Watch 自动收到 NGD，测试不直接调用 Reconcile；
- PRC 从 API Server 读取 3000 Node 和 Bound Pod；
- 静态快照 Hash 在相同输入下稳定；
- PRC 先 PUT 静态快照，再 POST 计算请求；
- POST 包含完整而非增量的 Node 动态状态；
- PRC 不修改 Algorithm 返回的 Node score 和顺序；
- PRC 生成一个 Cluster-scoped 正式 NGG；
- spec.nodes 约 1000 条，按 score 降序且同分顺序稳定；
- NGG source=normal、status.phase=Active；
- NGD status.phase=Fulfilled，grantRef 和 resolvedNodeCount 正确；
- NGG 已存在时 Patch，而不是重复 Create。

### 6.7 异常流程

- NGD LabelSelector 或资源字段非法；
- Node 缺少必需拓扑；
- Algorithm 不可访问或超时；
- Algorithm 不确认静态快照；
- Algorithm 返回错误 requestId、snapshotId 或未知 Node；
- Algorithm 返回重复 Node、越界 score 或非稳定结果；
- 没有可行 Node 时 NGD 进入 Failed；
- 指标暂时异常时不删除 NGG 对象，而是 Patch 同一个 NGG 为 source=degraded；保留硬约束过滤结果，并按正式协议将缺少可信负载数据的节点改为中性分 50，不沿用已经失真的旧负载分数；
- NGD 删除时按最终确定的正式生命周期规则处理 NGG。

### 6.8 通过条件

- 真实 PRC Manager、Watch、Work Queue 和 Reconcile 被实际执行；
- Mock Algorithm 完整记录并通过 PUT/POST 协议断言；
- 正式 CRD Schema 接受生成的 NGG；
- NGG 可保存约 1000 Node，并记录 YAML/JSON 大小和 API 写入耗时；
- NGD/NGG 状态、版本、时间戳、source 和引用关系正确；
- 所有异常用例都输出明确、可断言的状态和原因。

## 7. 第三组：无真实 Kubernetes 集群的全链路模拟测试

### 7.1 目标

在 envtest 中连接真实 PRC、真实 Algorithm Server 和真实 Python Worker，并使用调度消费模拟器验证正式 NGG 能否真正成为后续节点选择范围。

本组验证“系统逻辑全链路”，不宣称完成真实 Volcano/kube-scheduler 性能或容器运行验收。

### 7.2 测试示意图

~~~mermaid
flowchart TB
    T["测试驱动器<br/>固定种子3000 Node"]
    K["envtest API Server + etcd<br/>正式NGD/NGG CRD"]
    N["3000 Mock Node<br/>Bound Pod + Pending Pod"]
    P["真实Go PRC Manager"]
    A["真实Go Algorithm Server"]
    M["Mock Prometheus HTTP<br/>3000 Node CPU/内存/网络 + Bearer认证"]
    W["真实Python Worker<br/>Requirement→Topology→LoadBalance"]
    G["正式扁平NGG<br/>spec.nodes约1000"]
    S["NGG消费/调度模拟器<br/>复用Store/Filter/Score核心逻辑"]
    B["模拟Binding<br/>Pod.spec.nodeName"]
    C["consumer.acceptedGeneration<br/>越界Node=0"]

    T -->|"创建CRD和模拟对象"| K
    N -->|"API对象"| K
    K -->|"Watch NGD/Node/Pod"| P
    P -->|"PUT静态快照 + POST计算"| A
    A -->|"周期PromQL"| M
    M -->|"指标Vector"| A
    A -->|"JSON Lines"| W
    W -->|"候选Node和score"| A
    A -->|"Allocation Response"| P
    P -->|"CREATE/PATCH"| G
    G -->|"List/Watch Active NGG"| S
    K -->|"Pending Pod + Node"| S
    S -->|"只从NGG授权Node选择"| B
    B -->|"Binding/更新消费状态"| K
    K -->|"读取最终对象"| C
~~~

### 7.3 Mock 与真实边界

| 类型 | 内容 |
|---|---|
| Mock | 3000 Node/Pod、Prometheus HTTP、调度触发和Binding执行环境 |
| 真实 | envtest、PRC、Algorithm、Python Worker、正式NGD/NGG、可复用的NGG Store/Filter/Score核心逻辑 |
| 不启动 | Kind Worker、kubelet、真实Volcano Scheduler、真实kube-scheduler、容器运行时 |

调度消费模拟器不能自行重写一套与插件无关的规则。应将正式 NGG 的解析、版本/时效/source/phase 校验、Node集合构造和score读取逻辑抽成可测试核心包，由未来插件和模拟器共用。

### 7.4 正式模式不使用 Locked

联通正式 NGG 生命周期是：

~~~text
Active / ReclaimRequested / Draining / Returned
~~~

因此本组不使用当前 Demo 的 Trying/Locked/Exhausted。消费方成功加载 NGG 后验证：

~~~yaml
status:
  phase: Active
  consumer:
    acceptedGeneration: <metadata.generation>
~~~

模拟 Pod Binding 是授权效果证据，不改变正式 NGG 生命周期语义。

### 7.5 子阶段

| 阶段 | 输入 | 输出 | 必须记录 |
|---|---|---|---|
| F1 环境启动 | envtest、正式CRD | API Ready | 启动耗时 |
| F2 对象写入 | 3000 Node、Bound/Pending Pod | API对象 | 数量、写入耗时 |
| F3 指标/Algorithm启动 | 3000组指标、Worker配置 | 指标快照就绪 | 准备和刷新耗时 |
| F4 PRC启动 | API和Algorithm地址 | Manager Ready | Cache Sync耗时 |
| F5 NGD处理 | 正式NGD | Reconcile | Watch和队列耗时 |
| F6 PRC数据构造 | 3000 Node/Pod | 静态快照和动态状态 | 数量和耗时 |
| F7 Algorithm计算 | 快照、状态、指标、Demand | 约1000候选Node | 各算法阶段和总耗时 |
| F8 NGG写入 | 候选结果 | 正式NGG | Node数、大小、API耗时 |
| F9 NGG消费 | Active NGG | 授权Node Store | Watch延迟、generation |
| F10 Filter/Score模拟 | Pending Pod、Node、授权Store | 最终Node | 授权数、拒绝数、score |
| F11 Binding模拟 | Pod和最终Node | Pod.spec.nodeName | API耗时、是否越界 |
| F12 消费状态 | NGG generation | acceptedGeneration | Status写入耗时 |

### 7.6 必测用例

- 正常 NGD 产生 source=normal 的 Active NGG；
- 消费模拟器只接受 schedulerName 匹配、version=v1、未过期、phase=Active 的 NGG；
- source=normal、degraded、disabled 按正式协议执行对应策略；
- 多个有效 NGG 存在时按正式规则合并 Node，重复 Node 保留最高 score；
- 所有模拟绑定 Pod 都在当时有效 NGG 授权范围内；
- 过期、版本错误、schedulerName不匹配或非Active NGG不产生授权；
- 缺少有效 NGG 时故障关闭，受管 Pod 保持 Pending；
- NGG generation 变化后重建 Store，并更新 acceptedGeneration；
- 有效 NGG 变化后，新 Pod 使用新的授权范围。

### 7.7 通过条件

- NGD → PRC → Algorithm → Python Worker → NGG → 消费模拟 → Binding 链路实际执行；
- PRC与Algorithm使用真实HTTP，Go与Python使用真实JSON Lines；
- Algorithm通过真实Prometheus Client访问Mock Prometheus HTTP；
- 正式NGG实际写入envtest API Server；
- 最终授权Node集、score和顺序与NGG一致；
- 模拟绑定越界Node数为0；
- 无有效NGG时绑定数为0；
- consumer.acceptedGeneration等于metadata.generation；
- 报告明确标识为“全链路模拟测试”，不写成真实调度器性能结论。

## 8. 统一计时和结果记录

### 8.1 业务时间记录格式

~~~json
{
  "requestId": "request-001",
  "testGroup": "algorithm-complete",
  "unit": "ms",
  "prcSendToReceiveMs": 578.306,
  "algorithmProcessingMs": 565.859,
  "excluded": ["cache preparation", "debug trace", "evidence file writes"]
}
~~~

第二、三组使用`durationMs`，但T0必须来自PRC Reconciler收到NGD Watch的观察点。中间步骤文件用于解释数据变化，不为每个文件写入动作建立耗时。

### 8.2 统一跟踪字段

~~~text
requestId
ngdUID
ngdGeneration
nodeStaticSnapshotId
metricSnapshotId
topologyVersion
component
stage
~~~

### 8.3 计时口径

所有耗时统一使用毫秒 `ms`。测试框架使用单调时钟计算差值，墙上时间只用于日志定位。

| 组 | T0 | T1 | 结果字段 |
|---|---|---|---|
| 第一组Cold：PRC发送到接收 | Algorithm缓存为空，PRC开始PUT静态快照 | 静态/指标缓存Ready，且Allocation Response已接收并解码 | `prcSendToReceiveMs` |
| 第一组Warm：PRC发送到接收 | 缓存Ready，PRC发出`POST /api/v1/allocate`前 | Allocation Response已接收并解码 | `prcSendToReceiveMs` |
| 第一组：Algorithm内部 | Algorithm HTTP Handler接受请求 | 候选组结果已经计算完成 | `algorithmProcessingMs` |
| 第二组：PRC | PRC Manager收到NGD事件并开始本generation的Reconcile | 正式NGG和NGD Status达到本Case期望状态 | `durationMs` |
| 第三组：全链路 | PRC Manager收到NGD事件并开始本generation的Reconcile | NGG产生且消费模拟完成授权、Binding和acceptedGeneration更新 | `durationMs` |

第一组的Cold和Warm分别报告PRC侧与Algorithm内部两个时间。Cold的PRC侧时间包含静态PUT和等待Prometheus指标缓存，Warm只包含动态POST往返；两者的Algorithm内部时间口径相同。第二、三组不能从测试脚本调用Create之前或Create成功时起算，而必须以PRC实际收到Watch事件并开始Reconcile为T0。

以下过程不计入真实业务耗时：

- Fixture生成、envtest/API Server/Mock Server/进程启动；
- Warm场景的静态快照预装、Prometheus指标预读及缓存就绪等待；Cold场景明确将这些过程计入PRC侧时间；
- 为展示而额外发送的 `debugTrace` 请求；
- JSON格式化、证据整理、目录组织、报告及中间文件写盘；
- 第三组测试为复用同一envtest而执行的测试专用对象清理间隔。

Mock Prometheus是第一、三组的测试基础设施，负责提供与真实Prometheus HTTP API一致的响应、Bearer认证与请求审计；它不作为独立Demo或独立业务计时Case。

## 9. 结果文件规划

实际统一实现目录为 `test_suites/`。大体量真实输入、过程证据和最终输出只在大组运行目录保存；小组Case目录只保存场景定义、对大组文件的引用、该Case计时及结论，避免重复和混乱。

~~~text
test_suites/<group>/runs/<run-id>/
├─ input/
│  ├─ fixture/                    仅保存本大组实际使用的Node、Pod、静态/动态或指标数据
│  └─ cases/<case>/               本Case实际提交的NGD、请求或Pending Pod
├─ output/
│  ├─ infrastructure/             Mock Prometheus审计、缓存状态等非业务计时证据
│  ├─ cases/<case>/intermediate/  该Case各真实业务步骤的输入输出
│  ├─ cases/<case>/final/         正式响应、NGD/NGG、Binding等最终产物
│  └─ summary.json
├─ result.json
└─ report.md

test_suites/<group>/cases/<case>/runs/<run-id>/
├─ input/
│  ├─ case.json                   场景、步骤和断言
│  └─ group-input-reference.json  指向大组真实输入
├─ output/
│  └─ group-output-reference.json 指向大组过程和最终输出
├─ timing/                        只保存该Case业务耗时
├─ result.json
└─ report.md
~~~

summary.json 优先保存数量、Hash、ID、状态、耗时和文件引用，不重复嵌入3000条完整Node。

## 10. 计划运行入口

当前 Makefile 已提供统一入口。三组均可执行；缺少 Docker、Algorithm 镜像或 envtest 资产时返回非零状态，其中可识别的前置资产缺失记录为 `BLOCKED`，不会误报通过：

~~~bash
make test-algorithm-complete
make test-prc-complete
make test-full-chain-simulated
make test-acceptance-v2
~~~

test-acceptance-v2 按顺序执行前三组，任意一组失败则整体失败。

## 11. 实施顺序

~~~mermaid
flowchart LR
    D["锁定正式CRD、Fixture和期望结果"]
    A["完成第一组<br/>Algorithm 3000 Node"]
    PT["先写PRC envtest骨架<br/>和Mock期望"]
    PC["适配PRC正式NGD/NGG"]
    P["跑通第二组"]
    S["抽取NGG消费核心逻辑<br/>实现调度模拟器"]
    F["跑通第三组"]

    D --> A
    D --> PT
    PT --> PC --> P --> S --> F
~~~

具体顺序：

1. 锁定正式 CRD、固定随机种子、数据 Schema 和每阶段期望数量；
2. Algorithm 组可先独立实现，不等待 PRC 修改；
3. 先建立 PRC envtest、Mock Algorithm 和期望 NGG Fixture；
4. 再适配 PRC 正式接口，边修改边运行第二组；
5. 第二组通过后，将 Mock Algorithm 替换成真实 Algorithm；
6. 抽取正式 NGG 消费核心逻辑并实现调度消费模拟器；
7. 运行第三组和总验收。

## 12. 不属于本轮验收的内容

- 3000台真实机器压测；
- 真实LLDP报文和交换机联调；
- 真实Volcano/kube-scheduler吞吐性能结论；
- kubelet拉起容器、CNI和Pod Running；
- 需要额外Exporter的交换机CPU/内存、主动RTT/丢包和跨集群可达性指标；
- ReclaimRequested、Draining、Returned完整产品流程；
- 将当前Demo Top-3切组作为正式NGG验收标准。

## 13. 最终验收清单

### 13.1 Algorithm组

- [x] Mock PRC通过真实PUT/POST提供3000 Node数据；
- [x] Mock Prometheus通过标准HTTP Query API提供3000 Node CPU/内存/网络指标；
- [x] Mock Prometheus严格校验Bearer Token，并覆盖正确、缺失、错误和超时场景；
- [x] Go缓存和Python Worker真实运行；
- [x] 输出固定、稳定、无越界的约1000候选Node；
- [x] 输出候选阶段数量，以及客户端往返和Algorithm内部处理两种业务耗时；
- [ ] 补充请求大小和进程RSS报告；Requirement/Topology/LoadBalance不要求单独计时。

### 13.2 PRC组

- [x] envtest安装联通正式Cluster-scoped NGD/NGG CRD；
- [x] API Server中存在3000个Mock Node对象；
- [x] 真实PRC Manager通过Watch/Reconcile处理NGD；
- [x] Mock Algorithm收到正确PUT/POST；
- [x] PRC生成一个spec.nodes约1000条的正式扁平NGG；
- [x] NGD/NGG Status和异常状态符合正式Schema。

### 13.3 全链路模拟组

- [x] 真实PRC、Go Algorithm和Python Worker完整连通；
- [x] 正式NGG实际写入envtest API Server；
- [x] 调度消费模拟器复用正式NGG消费核心逻辑；
- [x] 模拟绑定只使用有效NGG中的Node；
- [x] 越界绑定数为0，缺少有效NGG时绑定数为0；
- [x] consumer.acceptedGeneration与当前NGG generation一致；
- [x] 结果明确标识为模拟测试，不宣称为真实调度器性能。

## 14. 三组测试分别回答的问题

1. Algorithm组：3000 Node的静态、动态、指标和拓扑数据通过真实协议进入Algorithm后，能否稳定地产生正确候选结果并满足性能目标？
2. PRC组：正式NGD写入轻量API Server后，真实PRC能否读取3000 Node/Pod、调用Algorithm协议并生成符合正式Schema的扁平NGG？
3. 全链路模拟组：不使用真实Kubernetes Worker和真实调度器时，系统逻辑链路能否证明NGG实际限制了后续节点选择，并且不会出现越界绑定？

三个问题全部得到肯定答案，才能形成本轮完整验收结论。

## 15. 2026-08-19 首轮实现与实测结果

### 15.1 已实现目录与入口

~~~text
test_suites/
├─ common/                 3000 Node生成器、严格Mock Prometheus、计时/报告
├─ group1_algorithm/       Cold与Warm独立Case、独立报告；Mock Prometheus仅作基础设施
├─ group2_prc/             envtest + 真实PRC + Mock Algorithm
└─ group3_full_chain/      envtest + 真实PRC/Algorithm + consumer/Binding

ngg_consumer/formalgrant/  调度框架无关的正式NGG解析、校验和授权合并核心
~~~

统一入口：

~~~bash
make test-acceptance-v2
~~~

### 15.2 首轮实测结论

以下数字来自 3000 Node 固定 Fixture。运行结果文件保存在各组 `runs/<时间戳>/`，不提交 Git。

| 组/用例 | 首轮结果 | 核心耗时或结论 |
|---|---|---|
| Algorithm cold_cache | PASS | 空缓存；PRC静态PUT至最终响应 `1439.734 ms`；Algorithm内部处理 `821.458 ms` |
| Algorithm warm_cache | PASS | 缓存Ready；5次预热、30次采样；PRC发送至接收P95 `578.306 ms`；Algorithm内部处理P95 `566.697 ms` |
| Mock Prometheus | 基础设施通过 | 14类指标、42000样本、Bearer认证和PromQL审计；不建立独立Demo/Case，Cold等待已计入PRC侧时间 |
| PRC normal_create | PASS | PRC收到NGD Watch至正式NGG/Status Ready `619.728 ms` |
| PRC existing_patch | PASS | Watch至同名NGG完成Patch `740.264 ms`，节点上限从1000变为900 |
| PRC异常 | PASS | 未知Node、无可行组、Algorithm 503分别为`371.572/366.281/366.172 ms`，均进入明确Failed状态 |
| 全链路 cold_cache | PASS | Watch至NGG、授权及Binding完成`2285.188 ms`；20个Binding均未越界 |
| 全链路 warm_cache | PASS | Watch至NGG、授权及Binding完成`1398.411 ms`；20个Binding均未越界 |
| 故障关闭 | PASS（正确性） | Returned NGG授权数0、Binding数0；因没有新NGD Watch，不报告业务耗时 |
| generation更新 | PASS（正确性） | 授权集500 Node、10个Binding均在范围内、acceptedGeneration=2；不报告NGD链路耗时 |

所有数值单位均为 `ms`。Cold明确包含静态同步和指标缓存等待；Warm不包含缓存准备。两种场景都排除额外Trace请求、协议证据序列化和文件写盘。性能实现过程中发现原 `can_place_minimums` 使用 O(P×N) 全表扫描，3000 Node/1000副本时单次约14秒；改为确定性优先队列 O((N+P)logN) 后，Warm的Algorithm内部处理P95降到约0.57秒。

本轮最终统一入口为 `make test-acceptance-v2`，三个大组的报告分别是：

- `test_suites/group1_algorithm/runs/20260819-170516/report.md`；
- `test_suites/group2_prc/runs/20260819-164121/report.md`；
- `test_suites/group3_full_chain/runs/20260819-164215/report.md`。

三组使用同一份确定性输入：3000 Node、1000 个 `inUse=true` 动态状态、2000 个可用 Node、100 个已绑定 Pod、14 类指标和 42000 个指标样本。

### 15.3 当前仍未完成的产品范围

三组核心测试通过不代表以下内容已经完成：

- 正式 Volcano/kube-scheduler 插件仍需从旧 Demo NGG 语义迁移到 `ngg_consumer/formalgrant`；本轮只用消费模拟器验证正式 NGG；
- `minThroughput`、跨/集群内亲和与 `networkReachability` 等正式硬约束尚未进入 Algorithm 协议，PRC 当前显式返回 Failed，不会静默忽略；
- Algorithm的Requirement/Topology/LoadBalance已通过额外的不计时Trace请求保存输入输出；进程RSS报告尚未补齐，因此13.1最后一项仍未勾选；
- 本轮没有真实调度器、kubelet、CNI、业务容器 Running 或 3000 台真实服务器性能结论。

## 16. 组内 Case 输入、过程输出与需求方展示方法

### 16.1 统一证据结构

每个大组保存一次完整真实输入和输出；每个小Case保存引用、计时和结论，不能只留下 `PASS`：

~~~text
<group>/runs/<run-id>/
├─ input/fixture/                       仅保存本大组实际使用的Node、Pod、静态/动态或指标
├─ input/cases/<case>/                  本Case真正提交的请求、NGD或Pod
├─ output/infrastructure/               Mock Prometheus、缓存等前置证据
├─ output/cases/<case>/intermediate/    每个业务步骤的真实输入、输出和meta
├─ output/cases/<case>/final/           正式响应、NGD/NGG或Binding结果
├─ output/summary.json
├─ result.json
└─ report.md

<group>/cases/<case>/runs/<run-id>/
├─ input/case.json                      场景、步骤和断言
├─ input/group-input-reference.json     指向大组输入
├─ output/group-output-reference.json   指向大组过程和最终结果
├─ timing/                              只记录该Case真实业务耗时
├─ result.json
└─ report.md
~~~

中间文件全部在T1之后由内存中的Recorder统一写入，因此文件数量和磁盘速度不会影响业务耗时。

### 16.2 第一组 Case：Algorithm

第一组执行代码：`test_suites/group1_algorithm/run_group.py`。

#### A. Mock Prometheus基础设施

Mock Prometheus不再作为一个小Case。它在业务计时前完成服务启动，并在Cold计时过程中接受Algorithm的14类PromQL查询、Bearer认证和请求审计；指标缓存Ready是Cold业务过程的一部分，也是Warm的前置条件。证据放在大组目录：

- `input/prometheus-metrics-catalogue.json`：指标名、PromQL、类型和单位；
- `output/infrastructure/mock-prometheus.json`：服务配置和认证检查结论；
- `output/infrastructure/prometheus-requests.jsonl`：Algorithm真实发起的查询审计；
- `output/infrastructure/cache-ready.json`：静态及指标缓存已经Ready的证明。

这些文件验证Mock Server工作是否正确，但不形成独立Demo或独立Case；Cold中的真实指标查询等待已经体现在`prcSendToReceiveMs`中，Mock审计文件写盘本身不计时。

#### B. `cold_cache`

~~~text
Algorithm进程Ready，但静态/指标缓存为空
    │
    ├─ T0-PRC：PUT静态快照
    ├─ Algorithm从Mock Prometheus读取14类指标并缓存
    ├─ PRC发送动态Node POST
    │      └─ Algorithm Handler接收：T0-Algorithm
    │             └─ Requirement → Topology → LoadBalance
    │                    └─ 候选结果Ready：T1-Algorithm
    └─ Allocation Response接收并解码：T1-PRC
~~~

Cold独立报告两个时间：

- `prcSendToReceiveMs = T1-PRC - T0-PRC`，包含静态同步、指标缓存等待和动态请求；
- `algorithmProcessingMs = T1-Algorithm - T0-Algorithm`，只包含Allocate Handler内部处理。

| 内容 | 大组文件 |
|---|---|
| 正式输入 | `input/cases/cold_cache/static-snapshot.json`、`allocation-request.json` |
| 正式输出 | `output/cases/cold_cache/final/allocation-response.json` |
| 冷缓存过程 | `output/cases/cold_cache/intermediate/01-static-snapshot-put/`、`02-cache-ready/`、`03-prc-algorithm-http/` |
| 三阶段输入输出 | `output/cases/cold_cache/intermediate/algorithm-pipeline/` |

正式计时请求不启用 `debugTrace`。为展示Requirement、Topology和LoadBalance的小过程，测试会在正式计时结束后另发一个相同数据的Trace请求，再把三阶段输入输出写入上述目录，因此Trace与写盘均不会污染业务时间。

#### C. `warm_cache`

缓存Ready后先预热5次，再正式执行30次。循环内只保留内存中的两个耗时数组，正式采样结束后才统一写文件和统计分布：

- PRC发送动态POST至接收响应：min、mean、P50、P95、P99、max；
- Algorithm服务端处理：min、mean、P50、P95、P99、max；
- 最后一次真实请求：`input/cases/warm_cache/allocation-request-last.json`；
- 最后一次真实结果：`output/cases/warm_cache/final/allocation-response-last.json`；
- Warm自己的三阶段证据：`output/cases/warm_cache/intermediate/algorithm-pipeline/`。

### 16.3 第二组 Case：PRC

第二组包装代码为 `test_suites/group2_prc/run_group.py`，核心 Go 执行代码为 `test_suites/group2_prc/runner/main.go`，运行真实 controller-runtime Manager/Reconciler 和 envtest API Server/etcd。Algorithm 在本组有意使用 Mock，以便只定位 PRC 行为。

每个Case使用的大体量共同输入只保存于大组的 `input/fixture/`：

- `input/fixture/kubernetes-nodes.json`：实际写入的3000个Node；
- `input/fixture/kubernetes-pods.json`：实际写入的100个已绑定Pod；
- `input/cases/<case>/formal-ngd-*.json`：实际写入envtest的正式Cluster-scoped NGD；
- `output/cases/<case>/intermediate/prc-algorithm-http/*/input.json`：PRC实际构造的静态快照或动态调度请求；
- 对应的 `output.json`：Mock Algorithm真实HTTP响应；
- `meta.json`：PUT/POST路径、HTTP状态码和往返耗时。

此外，`output/cases/<case>/intermediate/prc-snapshot-build/`将Kubernetes Node/Pod输入与PRC生成的静态快照、动态分配请求直接对应；成功Case的`prc-ngg-publish/input.json|output.json`则把Algorithm候选响应与最终正式NGG直接对应。

| Case | 关键输入 | 中间输出 | 最终输出 |
|---|---|---|---|
| `normal_create` | 正式NGD、3000 Node、100 Pod | 静态PUT ACK、1000 Node候选响应 | `formal-ngd-final.json`、`formal-ngg.json` |
| `existing_patch` | 旧NGG、maxNodes=900的新NGD generation | 新的PRC分配请求和候选响应 | 同名NGG的新generation，900 Node |
| `invalid_response` | Mock返回`unknown-node-uid` | PRC身份校验拒绝 | NGD `Failed/AlgorithmResponseInvalid`，无错误Active NGG |
| `no_feasible_nodes` | `UNSATISFIABLE`和空候选 | PRC执行NoFeasibleGroup分支 | NGD Failed、resolvedNodeCount=0 |
| `algorithm_unavailable` | Mock HTTP 503 | Recorder保存503原始响应 | NGD `Failed/AlgorithmRequestFailed` |

PRC协议Recorder位于`prc/internal/controller/algorithm_client.go`，通过可选回调先把请求/响应保存在内存中；生产运行时该回调为`nil`。测试Runner在所有业务T1已经确定后才调用`flushAll()`写盘，因此协议证据文件不进入计时范围。

第二组每个Case的T0均由Reconciler观察回调记录：PRC开始处理该NGD UID和generation时起算；T1为对应NGG及NGD Status达到预期。NGD对象Create调用本身、环境准备和后续写盘都不计入。

一个 Case 中可能看到多组编号连续的 PUT/POST，这是 Node/Pod Watch、失败重试或状态更新再次触发 Reconcile 的真实结果，不是测试脚本复制数据。展示时先看最早一组请求响应，再结合 `meta.json` 和最终 CR 状态说明幂等重算。

### 16.4 第三组 Case：完整链路模拟

第三组包装代码为 `test_suites/group3_full_chain/run_group.py`，与第二组共用 Go runner，但把 Mock Algorithm 替换为真实 Go Algorithm + Python Worker。

~~~text
正式NGD + Pending Pod
        │
        ▼
真实PRC ──HTTP──> 真实Algorithm ──> FILTER/GROUP/SCORE
        │                                   │
        │<────────────候选组────────────────┘
        ▼
正式Active NGG
        │
        ▼
formalgrant解析/校验 ──> 授权Node集合 ──> pods/binding ──> Pod.spec.nodeName
        │
        └──────────────────────────────> acceptedGeneration
~~~

| Case | 实际输入 | 中间过程文件 | 最终输出 |
|---|---|---|---|
| `cold_cache` | Algorithm缓存尚未预装时创建正式NGD和20个Pending Pod；3000 Node及指标在大组输入 | PRC HTTP、NGG授权和20次Binding | Active NGG、20个已绑定Pod、越界0；从PRC收到NGD Watch起计时 |
| `warm_cache` | 缓存Ready后创建新正式NGD和20个Pending Pod | PRC HTTP、NGG授权和20次Binding | 20个已绑定Pod、越界0；从PRC收到新NGD Watch起计时 |
| `fail_closed` | Returned NGG、5 Pending Pod | `ngg-consumer/02-output-authorized-nodes.json`为空 | 0 Binding、Pod `nodeName`为空 |
| `generation_update` | 500 Node新generation NGG、10 Pending Pod | 新授权集合、10次Binding、consumer状态 | acceptedGeneration=2、越界0 |

NGG消费阶段统一保存：

~~~text
intermediate/ngg-consumer/
├─ 01-input-formal-ngg.json
├─ 02-output-authorized-nodes.json
├─ 03-input-output-bindings.json
└─ 04-output-consumer-status.json
~~~

`cold_cache`和`warm_cache`报告业务耗时，单位为`ms`；计时终点是NGG已经生成且授权、Binding和acceptedGeneration已完成。Consumer证据先保存在内存，计时结束后再写盘。`fail_closed`与`generation_update`是直接验证已有NGG消费语义，没有新的NGD Watch，因此只报告正确性，不伪造“从Watch开始”的耗时。

第三组正式计时请求不启用Algorithm Pipeline Trace。需要展示算法内部三阶段时，分别使用第一组`cold_cache`和`warm_cache`在各自计时结束后生成的独立Trace证据。

### 16.5 测试代码位置

| 功能 | 代码路径 |
|---|---|
| Fixture生成 | `test_suites/common/fixture.py` |
| Mock Prometheus认证、查询和审计日志 | `test_suites/common/mock_prometheus.py` |
| 计时、报告及大组/Case证据组织 | `test_suites/common/runtime.py` |
| 第一组执行 | `test_suites/group1_algorithm/run_group.py` |
| 第二组执行包装 | `test_suites/group2_prc/run_group.py` |
| envtest、PRC Case、Consumer和Binding | `test_suites/group2_prc/runner/main.go` |
| 第三组执行包装 | `test_suites/group3_full_chain/run_group.py` |
| Algorithm Requirement/Topology/LoadBalance Trace | `algorithm_server/python/algorithm_worker/pipeline.py` |
| PRC→Algorithm协议留痕 | `prc/internal/controller/algorithm_client.go` |
| 正式NGG解析与授权 | `ngg_consumer/formalgrant/grant.go` |
| 最新结果展示脚本 | `test_suites/show-latest.sh` |

### 16.6 如何运行和展示

首次修改 Algorithm 后重建镜像：

~~~bash
cd /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo
make algorithm-image
~~~

分别运行：

~~~bash
make test-algorithm-complete
make test-prc-complete
make test-full-chain-simulated
~~~

一次运行全部：

~~~bash
make test-acceptance-v2
~~~

向需求方展示最新报告和 Case 文件：

~~~bash
make test-showcase

./test_suites/show-latest.sh group1_algorithm cold_cache
./test_suites/show-latest.sh group1_algorithm warm_cache
./test_suites/show-latest.sh group2_prc normal_create
./test_suites/show-latest.sh group3_full_chain cold_cache
./test_suites/show-latest.sh group3_full_chain warm_cache
./test_suites/show-latest.sh group3_full_chain fail_closed
~~~

建议现场按以下顺序讲：

1. 先打开Case的`input/case.json`，说明场景和预期；
2. 根据`group-input-reference.json`进入大组`input/fixture/`和`input/cases/<case>/`查看真实输入；
3. 根据`group-output-reference.json`进入大组`output/cases/<case>/intermediate/`，按编号展示每一步的`input.json → output.json`；
4. 打开大组`output/cases/<case>/final/`中的正式响应、NGG和绑定后Pod；
5. 最后看Case的`timing/`、`result.json`和`report.md`，说明毫秒耗时和断言；第一组同时展示客户端往返与服务端处理时间；
6. Mock Prometheus只在大组`output/infrastructure/`中展示请求审计与认证结论，不作为独立Demo。

不建议在终端直接 `cat` 3000 Node或1000 Node候选大文件；现场用编辑器折叠JSON，或只搜索 `nodeCount`、`groupId`、`groupScore`、`bindingCount` 和 `outsideAuthorizedNodeCount` 等关键字段。
