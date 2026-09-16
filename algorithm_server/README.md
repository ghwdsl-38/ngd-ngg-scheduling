# Algorithm API Server 说明（Go 主进程 + Python 算法 Worker）

当前镜像版本为 `v0.4.0`。本目录集中保存 Algorithm 的全部实现：`go/` 是 HTTP 主服务、静态快照缓存、Prometheus 缓存、请求适配和进程管理；`python/algorithm_worker/` 只保存 Python 算法及单一 Worker 入口。

Python 不启动 FastAPI，也不保存跨请求缓存。镜像入口是 Go 二进制 `/app/algorithm-server`，它启动并管理唯一的 `python3 -m algorithm_worker.worker` 子进程。旧 Python HTTP、缓存、Prometheus 采集和响应组装实现已经删除，避免形成两套数据所有权。

## 1. 在整体系统中的位置

```mermaid
flowchart TB
    K8S[Kubernetes API Server] -->|Node、Pod、NGD| PRC[PRC]
    PRC -->|PUT：Node静态Hash快照| API[Go Algorithm API Server]
    PRC -->|POST：任务需求和Node动态状态| API
    PROM[Prometheus] -->|Go周期拉取CPU、内存和Node网络指标| API
    API -->|完整上下文/JSON Lines| WORKER[单一Python算法Worker]
    WORKER -->|Top-3候选组| API
    API -->|保持groupScore与排序| PRC
    PRC -->|创建或更新NGG| K8S
```

数据边界如下：

- Node 静态数据：由 PRC 上传，Go 保存当前和前一个内容 Hash 快照。
- Node 动态数据：由 PRC 随每次任务请求发送，只在本次请求中使用，不跨请求缓存。
- Prometheus 指标：由 Go 自己周期读取，进程内保存当前和前一个有效快照。
- Node静态拓扑：PRC只上传每个Node的直连`leafSwitchId`。
- 上层网络拓扑：Go从独立YAML加载Region→Location→DataCenter→Room→Border Domain→可选Spine→Leaf；NGD只通过`topologyLabels`引用其中DataCenter到Leaf的名称，不携带拓扑图。
- 输出：Python 最多返回 3 个稳定排序候选组；Go 和 PRC 均不修改分数和顺序。

## 2. 目录结构

```text
algorithm_server/
├── README.md
├── go/
│   ├── algorithm/
│   │   ├── application.go
│   │   ├── server.go
│   │   ├── cache.go
│   │   ├── metrics.go
│   │   ├── metrics_config.go
│   │   ├── prometheus_metrics.json
│   │   ├── topology.go
│   │   ├── service.go
│   │   ├── worker.go
│   │   └── types.go
│   └── cmd/algorithm-server/
│       └── main.go
└── python/
    └── algorithm_worker/
        ├── worker.py
        ├── models.py
        ├── pipeline.py
        ├── context.py
        ├── errors.py
        ├── quantity.py
        ├── services/
        │   └── node_view_builder.py
        ├── algorithms/
        │   ├── requirement.py
        │   ├── topology.py
        │   └── loadbalance.py
        └── config/
            └── loadbalance_profiles.json
```

各模块职责：

| 文件                                                         | 实现方式和功能                                                                                                                                                                |
| ------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `go/cmd/algorithm-server/main.go`                          | 正式 Server 入口。读取环境变量，创建`Server`并处理SIGTERM优雅退出。                                                                                                         |
| `go/algorithm/server.go`                                   | 完整Algorithm进程封装；统一拥有Application、HTTP Listener、Prometheus后台刷新、Python Worker关闭和Ready等待。生产入口、Group2和Group4共用。                                  |
| `go/algorithm/application.go`                              | Algorithm内部组装层，创建缓存、Prometheus、Python Worker和HTTP Handler；由`Server`统一管理生命周期。                                                                        |
| `go/algorithm/cache.go`                                    | Node静态快照缓存。校验`sha256:`内容Hash、Node UID唯一性和直连Leaf，内存中仅保留current/previous两份。                                                                     |
| `go/algorithm/topology.go`                                 | 严格解析独立拓扑YAML，校验显式Border Domain，用Leaf补齐上层拓扑；校验五级`topologyLabels`、映射物理交换机到逻辑域，并处理Spine缺失回退。                                  |
| `go/algorithm/metrics.go`                                  | Prometheus 采集与缓存。周期调用 instant query，按 Node 名合并 CPU、内存、吞吐、丢包、错误、重传、链路和带宽指标。                                                           |
| `go/algorithm/metrics_config.go`                           | 加载指标目录，配置 Bearer Token/Token 文件、CA、TLS Server Name 和超时。                                                                                                    |
| `go/algorithm/prometheus_metrics.json`                     | Mock 与真实 Prometheus 共用的14项指标契约。                                                                                                                                |
| `go/algorithm/service.go`                                  | 校验协议、解析静态快照、适配Node动态状态、调用Worker并组装响应。                                                                                                            |
| `go/algorithm/worker.go`                                   | 管理Python子进程和JSON Lines协议；同时提供第一组测试直接使用的正式Worker接口。                                                                                               |
| `go/algorithm/types.go`                                    | Go内部协议类型。                                                                                                                                                              |
| `../test/go/group1_algorithm_worker/`               | 标准`go test`第一组，直接验证Go调用真实Python Worker。                                                                                                                       |
| `../test/go/group2_prc_algorithm/`                  | 标准`go test`第二组，验证PRC调用真实Algorithm和带认证的Mock Prometheus。                                                                                                     |
| `go/go.mod`                                                | Go 模块定义；当前主服务只使用 Go 标准库。                                                                                                                                     |
| `python/algorithm_worker/worker.py`                        | Python 进程入口。从 stdin 读 JSON Lines，构造单次`AllocationContext`，执行 Pipeline，向 stdout 返回 Top-3 或结构化错误。                                                    |
| `python/algorithm_worker/pipeline.py`                      | 按固定顺序执行 requirement/topology/loadbalance，不读取NGD中的算法编排字段，最后稳定截取 Top-3。                                                                            |
| `python/algorithm_worker/context.py`                       | 定义静态快照、指标快照和单次计算上下文；不保存跨请求状态。                                                                                                                    |
| `python/algorithm_worker/models.py`                        | 定义固定流水线的三个算法阶段枚举。                                                                                                                          |
| `python/algorithm_worker/errors.py`                        | 定义输入、服务端算法参数和指标未就绪等结构化错误。                                                                                                                           |
| `python/algorithm_worker/quantity.py`                      | 解析 Kubernetes CPU、内存和扩展资源 Quantity，计算 PodSet 最小资源需求并进行节点装箱检查。                                                                                    |
| `python/algorithm_worker/services/node_view_builder.py`    | 合并Node静态属性、请求级占用状态、`nodeSelector`和Go层解析后的具体拓扑逻辑域约束，生成本次算法使用的Node视图。                                                            |
| `python/algorithm_worker/algorithms/requirement.py`        | 固定FILTER步骤：排除不可用或标签不匹配的Node，并形成扣减已请求资源后的可用视图。                                                                                              |
| `python/algorithm_worker/algorithms/topology.py`           | 固定GROUP步骤：只按Leaf→可选Spine→Border Domain→Room→DataCenter执行NarrowestFit，并按`requiredSame`限制可扩展的最宽层级。                                                  |
| `python/algorithm_worker/algorithms/loadbalance.py`        | 固定SCORE步骤：综合资源和Prometheus指标评分，逐层验证资源可行性，再按`minResources/maxNodes/quota`选具体Node并稳定排序。                                                       |
| `python/algorithm_worker/config/loadbalance_profiles.json` | 评分 profile 及资源、负载、拓扑权重，新增 profile 无需修改 Go 主服务。                                                                                                        |
| `docker/algorithm/Dockerfile`                                     | 位于项目`docker/algorithm/`目录。第一阶段编译 Go 二进制，第二阶段加入 Python Worker，最终`ENTRYPOINT` 为 `/app/algorithm-server`。                                                         |

## 3. 算法流水线

默认流水线为：

```text
requirement/v1 (FILTER)
        ↓
topology/v1 (GROUP)
        ↓
loadbalance/v1 (SCORE)
        ↓
按 groupScore 降序稳定排序，最多返回 3 组
```

该顺序由 Algorithm Server 固定，既不读取也不执行 NGD 中可选的 `algorithms` 字段。算法参数来自联通 NGD 的正式字段和服务端配置，避免请求方改变生产执行链。

### FILTER

综合以下数据形成当前任务的可用 Node 视图：

- 静态快照中的标签和 `allocatable`；
- `nodeUsageStates[].inUse`；
- 正式资源池请求中的 `ngd.nodeSelector`；
- 正式资源池请求中的五级`ngd.topologyLabels`；
- Node 动态状态中的 `requestedResources`。

资源池可用量按 `allocatable - requestedResources` 计算；`inUse=true` 的 Node 整体排除。

### GROUP

正式NGD不携带拓扑profile。GROUP固定按`Leaf → 可选Spine Domain → Border Domain → Room → DataCenter`形成拓扑组；SCORE按该顺序验证资源可行性。某一层出现可行组后立即停止。具体交换机名先由Go映射为逻辑域；`requiredSame`限制结果不能跨出相应层级。联通样例`SPINE: {}`或指定Spine不存在时，本次Spine约束转换为Border `requiredSame`并返回Warning。

### SCORE

节点分数综合剩余资源比例与 Prometheus CPU、内存和网络质量；组分数再结合静态带宽、时延形成的拓扑质量。资源池从高分 Node 开始选择：满足 `minResources` 后停止，且始终不突破 `maxNodes` 和 `quota`。Node 按 `score desc → nodeName → nodeUID` 排序，同一最窄可行层级的组按 `groupScore desc → groupId` 排序。

同一Node会出现在自身Leaf、Border Domain及更宽范围的临时候选中，但同一层的显式Border Domain互不重叠。每个请求只解析一次Node容量、只计算一次Node资源/Prometheus分数，各拓扑层复用结果。

## 4. HTTP 接口

| 方法和路径                                        | 作用                                    |
| ------------------------------------------------- | --------------------------------------- |
| `GET /healthz`                                  | 进程存活检查                            |
| `GET /readyz`                                   | API 可接收请求检查；不等待缓存预热      |
| `GET /internal/v1/cache/status`                 | 查看静态快照、Prometheus 和动态状态模式 |
| `PUT /internal/v1/node-static-snapshots/{hash}` | 上传 Node 静态快照                      |
| `POST /api/v1/allocate`                         | 新版候选组计算接口                      |

静态快照路径中的 Hash 必须是对以下内容进行稳定 JSON 编码后得到的 SHA-256：

```json
{
  "clusterId": "cluster-a",
  "topologyVersion": "node-leaf-v1",
  "nodes": []
}
```

动态状态是任务级完整数据，不使用增量版本：

```json
{
  "nodeUsageStates": [
    {"nodeUID": "uid-worker-0001", "inUse": false},
    {"nodeUID": "uid-worker-0002", "inUse": true}
  ]
}
```

### 4.1 测试展示用 Pipeline Trace

测试请求可显式增加 `"debugTrace": true`。此时正式响应除候选组外还包含 `pipelineTrace`，依次给出 Requirement/FILTER、Topology/GROUP、LoadBalance/SCORE 的实际输入和输出。普通请求不返回该字段，也不会构造 Trace。

统一测试会把它拆分成便于展示的文件：

```text
intermediate/algorithm-pipeline/
├── 01-requirement/input.json + output.json
├── 02-topology/input.json + output.json
└── 03-loadbalance/input.json + output.json
```

该能力只用于验收留痕，不应作为生产业务协议的必填字段；生产性能数据应使用未开启 Trace 的请求测量。

## 5. Server 在哪里启动

### 5.1 最终进程入口

正式 Server 从 [`go/cmd/algorithm-server/main.go`](go/cmd/algorithm-server/main.go) 的 `main()` 启动；业务实例由[`go/algorithm/application.go`](go/algorithm/application.go)创建。容器中的完整进程关系是：

```text
Docker ENTRYPOINT /app/algorithm-server
└─ Go main()
   ├─ 启动 HTTP Server，默认 :8080
   ├─ 启动 Prometheus 指标刷新 goroutine
   └─ 启动 python3 -m algorithm_worker.worker
      └─ 通过 stdin/stdout JSON Lines 执行 Python 算法
```

Python `worker.py` 是算法子进程入口，不是 HTTP Server 入口。对外的 8080 端口始终由 Go 提供。

### Python Worker 超时恢复

实现位于 [go/algorithm/worker.go](go/algorithm/worker.go)，不改变 HTTP 或 JSONL 接口，也不改变 Python 算法。

| 情况 | 当前处理 |
| --- | --- |
| 正常请求 | 复用同一个 Python 进程，串行写入请求和读取响应，以 ID 校验对应关系 |
| 请求仍在排队时取消/超时 | 返回 504，不终止正在处理其他请求的进程 |
| 已发送请求取消/超时，包括 stdin 写入阻塞 | 关闭这一代管道，终止并回收 Python，等待读写协程退出后返回 504 |
| EOF、非法 JSON、ID 不匹配或缺失结果/错误 | 返回 503，丢弃当前进程，下一请求使用新进程 |
| Python 返回合法业务错误 | 原样保留错误语义，不重启进程 |
| Application 关闭或父 Context 取消 | 取消等待及执行中的请求，回收子进程，禁止再次启动；Close 可重复调用 |

恢复是**下一请求触发的新进程启动**，不会自动重放失败请求，也不会重新启动 Go HTTP 服务或清空 Go 的静态/Prometheus 缓存。异常后至少退避 100 ms，每个请求最多尝试启动一次；启动失败返回可重试的 503。回收进程等待上限为 2 s，未回收完成时不启动替代进程；Close 等待串行入口上限为 4 s，取得入口后的进程回收另有 2 s 上限。因此超时响应可能附带短暂清理耗时，不是到达 deadline 后零耗时返回。

专项测试：[worker_test.go](go/algorithm/worker_test.go)。测试使用真实 Python 进程和 [可控异常 Worker](go/algorithm/testdata/recovery_worker.py)，覆盖重复超时恢复、排队取消、协议异常、业务错误、大请求写阻塞、并发调用、关闭、父 Context 取消和重启失败恢复。

```bash
cd /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo
source scripts/go-test-env.sh
cd algorithm_server/go
GOWORK=off go test -mod=vendor -race ./... -count=3
```

需要本地 `python3` 和支持 Race 的 Go/C 工具链；无需真实 Kubernetes 或 Prometheus。正常业务链路继续使用 Group1、Group2、Group4 验证。

### Go与Python原始协议证据

正式运行不落盘Go与Python之间的大报文。第一组`evidence-run`会显式设置：

```bash
ALGORITHM_WORKER_EVIDENCE_DIR=/evidence
```

此时Go主进程按stdin/stdout的真实JSON Lines协议输出：

- `go-to-python-request.jsonl`：Go发送给Python的完整`workerEnvelope`，包含请求级动态状态、Go静态缓存和Prometheus指标缓存；
- `python-to-go-response.jsonl`：Python返回给Go的候选组、Pipeline Trace或结构化错误。

四组Go Test会把证据目录显式注入`Application`，并在业务计时结束后完成其他文件写入。完整运行命令和结果目录见`test/go/README.md`。

### 5.2 项目部署中如何启动

```text
make algorithm / make deploy
        ↓
scripts/04b-build-algorithm.sh       构建 v0.4.0 镜像
        ↓
scripts/05a-deploy-algorithm.sh      应用 Deployment/Service 并滚动重启
        ↓
config/manager/algorithm.yaml        在 ngd-ngg-system 启动 1 个 Pod
        ↓
```

- Deployment：`ngd-ngg-system/ngd-ngg-algorithm`
- Service：`ngd-ngg-system/ngd-ngg-algorithm:8080`
- PRC 访问地址：`http://ngd-ngg-algorithm.ngd-ngg-system.svc:8080`
- 镜像配置：`config/manager/algorithm.yaml`

### 5.3 当前是否已切换最新版

当前源码、默认镜像变量和 Deployment 使用 `ngd-ngg-algorithm:v0.4.0`。每次修改后应重建镜像、推送到目标仓库并执行：

```bash
make algorithm
```

该命令会重建镜像、重启 Deployment 并等待 rollout。验证最新 Go+Python Worker 的标准是：

```bash
kubectl -n ngd-ngg-system logs deployment/ngd-ngg-algorithm --tail=20

# 应出现：
# Go Algorithm API Server listening on :8080; Python module=algorithm_worker.worker
```

健康接口应返回 `"runtime":"go"` 和 `"algorithmWorker":"python"`。2026-08-19 的 Prometheus/拓扑/评分改造按要求未构建镜像、未滚动部署也未测试，因此现有集群 Pod 不能作为本轮源码版本的运行证据。

### 5.4 是否可以单独启动

可以。最稳定的方式是用 Docker，不需要 Kubernetes、Volcano 和 PRC：

```bash
cd /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo
docker build -t ngd-ngg-algorithm:v0.4.0 -f docker/algorithm/Dockerfile .
docker run --rm --name ngd-ngg-algorithm-standalone \
  -p 18080:8080 \
  -e PROMETHEUS_URL= \
  -e TOPOLOGY_CONFIG_FILE=/etc/ngd-ngg/topology.yaml \
  -v /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo/config/topology/unicom-huailai-102-sample.yaml:/etc/ngd-ngg/topology.yaml:ro \
  ngd-ngg-algorithm:v0.4.0
```

另一个终端验证：

```bash
curl http://127.0.0.1:18080/healthz
curl http://127.0.0.1:18080/readyz
curl http://127.0.0.1:18080/internal/v1/cache/status
```

关闭 Prometheus 时 Server 仍可启动，指标状态为 degraded。固定流水线当前允许指标降级继续使用硬约束。完整 PUT+POST 协议由统一测试验证：

```bash
make go-test-group1
make go-test-group2
```

也可以不经 Docker 直接启动，但需要本机已安装 Go 和 Python 3：

```bash
cd /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo/algorithm_server/go
PYTHONPATH=../python \
  ALGORITHM_LISTEN_ADDRESS=:18080 \
  PROMETHEUS_URL= \
  TOPOLOGY_CONFIG_FILE=../../config/topology/unicom-huailai-102-sample.yaml \
  /mnt/data0/tools/go/bin/go run ./cmd/algorithm-server
```

## 6. Prometheus 配置

| 环境变量                               |      默认值 | 说明                        |
| -------------------------------------- | ----------: | --------------------------- |
| `PROMETHEUS_URL`                     |          空 | 空值表示关闭指标采集        |
| `PROMETHEUS_REFRESH_SECONDS`         |          15 | 后台刷新周期，与PRC Reconcile周期一致 |
| `PROMETHEUS_STALE_SECONDS`           |         120 | 指标过期阈值                |
| `PROMETHEUS_REQUEST_TIMEOUT_SECONDS` |           5 | 单次查询超时                |
| `PROMETHEUS_NODE_LABEL`              | 指标目录中的 `node` | Prometheus 结果中的节点标签 |
| `PROMETHEUS_METRICS_CONFIG_FILE`     | 内嵌指标目录 | 可选的外部 JSON 指标目录   |
| `PROMETHEUS_BEARER_TOKEN`            | 空 | 直接配置 Bearer Token（与文件方式互斥） |
| `PROMETHEUS_BEARER_TOKEN_FILE`       | 空 | 从 Secret 挂载文件读取 Bearer Token |
| `PROMETHEUS_CA_FILE`                 | 空 | 自定义 CA PEM 文件          |
| `PROMETHEUS_TLS_SERVER_NAME`         | 空 | TLS Server Name             |
| `PROMETHEUS_INSECURE_SKIP_VERIFY`    | false | 仅隔离测试环境可显式启用  |
| `TOPOLOGY_CONFIG_FILE`               | `/etc/ngd-ngg/topology.yaml` | 独立上层网络拓扑YAML；无效时服务拒绝启动 |

未要求实时指标时，Prometheus 不可用会返回 `degraded=true` 并继续使用硬约束；算法参数设置 `requireMetrics=true` 时，指标未就绪返回 HTTP 503。

内置目录读取 CPU、内存及 node-exporter 可提供的 11 项网络指标。CPU/内存为必需项；可选网络查询失败时保留该轮快照并返回 coverage/warning，但不会仅因可选项缺失就把整份快照判为 degraded。比例值限制到 0～1，bytes/s、packets/s 等绝对值只限制为非负数。

部署样例的 `PROMETHEUS_URL` 是 `http://prometheus.monitoring.svc.cluster.local:9090`。目标环境应改为实际地址，并推荐把 Token 放入 Kubernetes Secret，再通过只读 Volume挂载并设置`PROMETHEUS_BEARER_TOKEN_FILE`，不要把Token写入日志或ConfigMap。

## 7. 统一测试

原来位于各组件旁边的独立测试已经移除。当前统一测试位于 `test/legacy/`，Algorithm 独立组入口为：

```bash
make demo-group1-timing
make demo-group1-evidence
make demo-group1
```

该组使用真实Go Server、真实长期Python Worker、Mock Prometheus基础设施和固定3000 Node Fixture。Timing与Evidence使用相同输入但独立运行：Timing不启用Trace或协议落盘；Evidence输出PRC↔Go HTTP、Go↔Python JSONL、Go缓存和Python Pipeline全过程。详细目录见`test/legacy/README.md`。
