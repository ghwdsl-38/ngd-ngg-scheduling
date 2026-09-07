# 第四组：完整PRC—Algorithm—NGG组件链路

本组使用envtest真实Watch、真实PRC、真实Go Algorithm Application、真实Python Worker和带Bearer认证的Go Mock Prometheus，最终生成联通正式NGG。

```bash
cd /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo
source scripts/go-test-env.sh
go test -p=1 ./go_test_suites/group4_full_real_algorithm -run '^TestGroup4_' -v -count=1
```

计时前已经完成Algorithm独立上层拓扑加载、Prometheus指标预热、envtest启动、1000 Node/Pod及Leaf标签预置、PRC Cache Sync和独立静态快照同步。计时从PRC通过Watch观察到NGD并进入Reconcile开始，到真实Algorithm返回且NGG为`Active`结束。静态快照构造与PUT不在任务计时内；NGD `Fulfilled`继续作为正确性断言，但不计时。

本组不启动Volcano或kube-scheduler，因此验证到正式NGG生成，不验证Pod最终Bind。

## 完整流程

```text
启动带Bearer认证的Go Mock Prometheus
  -> 创建并启动真实algorithm.Server（内部拥有Go Application、HTTP Server和Python Worker）
  -> Algorithm主动请求Mock Prometheus并完成14项指标预热
  -> 启动envtest API Server/etcd并安装联通CRD
  -> 预置1000 Node和Pod
  -> 创建并启动真实prc.Application（内部注册Manager、静态快照Controller和NGD Reconciler）
  -> 静态Controller独立PUT快照并等待Algorithm确认Ready
  -> 记录静态PUT次数
  -> 创建正式NGD
  -> PRC通过Watch观察到NGD，开始计时并进入Reconcile
  -> PRC读取已确认snapshotId并POST动态状态和完整NGD
  -> Go Algorithm解析五级topologyLabels，映射逻辑域并处理Spine缺失回退
  -> Algorithm固定执行requirement/topology/loadbalance
  -> Algorithm返回最多3个候选组和具体Node
  -> PRC将rank 1写成联通正式NGG
  -> NGG Active后停止计时
  -> 继续断言NGD Fulfilled
  -> 断言任务Reconcile期间静态PUT次数没有增加
  -> 保存全部Actual并与Expected比较
```

`testdata/input/`包含1000 Node、Pod、正式NGD、静态/动态数据和14项Prometheus指标。`results/<run-id>/actual/`保存：

- Mock Prometheus真实请求记录；
- 计时前独立静态快照PUT，以及计时内Allocate请求/响应；
- Go与Python的JSONL协议；
- Algorithm候选组和Pipeline Trace；
- 正式NGG Raw YAML、标准化结果和NGD Status。

主要断言包括认证及14项PromQL、独立静态同步、任务链路不重复PUT、PRC原样传递五级`topologyLabels`、Spine缺失回退、固定三段流水线、Top-3排序、每组具体Node、NGG节点与rank 1逐项一致，以及新版NGG的DataCenter/Room/Border/Leaf逻辑域字段。

## Delve全链路调试

### 1. 调试范围

本组Delve调试覆盖以下真实组件和通信边界：

```text
Algorithm -> Mock Prometheus：真实HTTP GET /api/v1/query + Bearer认证
PRC -> Algorithm静态缓存：真实HTTP PUT
测试/PRC -> envtest Kubernetes：真实Kubernetes REST和Watch
PRC -> Algorithm任务计算：真实HTTP POST /api/v1/allocate
Algorithm Go -> Python Worker：真实JSONL stdin/stdout进程通信
PRC -> Kubernetes：真实Create NGG和Status更新
测试 -> Kubernetes：真实GET最终NGG
```

Manager调用Reconcile、Algorithm HTTP Handler调用Go Service、PRC构造NGG属于同一进程内的Go函数调用。

Group4和正式进程一样，由`algorithm.Server.Start`在HTTP监听启动后开启Prometheus后台刷新：启动时立即拉取一次，之后每15秒刷新。测试通过`WaitForReady`等待第一次真实HTTP采集完成，再启动PRC和提交NGD。Debug模式只延长指标快照有效期，不改变采集流程。

### 2. 命令行启动Delve

必须在项目根目录执行：

```bash
cd /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo
source scripts/go-test-env.sh

NGG_TEST_DEBUG=true \
NGG_TEST_DEBUG_TIMEOUT=10m \
dlv test ./go_test_suites/group4_full_real_algorithm -- \
  -test.run '^TestGroup4_PRCWatchesNGDCallsRealAlgorithmAndCreatesNGG$' \
  -test.v \
  -test.count=1 \
  -test.timeout=30m
```

进入Delve后显示：

```text
(dlv)
```

Debug模式只改变测试保护参数：

- envtest准备、任务等待和PRC调用Algorithm的内部超时放宽到10分钟；
- Prometheus快照有效期放宽到30分钟，避免Delve暂停整个Go进程时被误判为指标过期；
- 生产PRC、生产Algorithm、普通`go test`的超时和指标过期规则不变；
- Debug耗时写入`debug-timing.json`并标记`performanceValid=false`。

### 3. 设置8个关键断点

在`(dlv)`中粘贴：

```text
b algorithm_server/go/algorithm/metrics.go:74
b prc/pkg/controller/static_snapshot_controller.go:194
b go_test_suites/group4_full_real_algorithm/group4_test.go:177
b prc/pkg/controller/prc_controller.go:106
b prc/pkg/controller/prc_controller.go:279
b algorithm_server/go/algorithm/service.go:41
b prc/pkg/controller/prc_controller.go:546
b go_test_suites/group4_full_real_algorithm/group4_test.go:206
breakpoints
c
```

这8个断点对应：

```text
Algorithm读取Prometheus
  -> PRC同步静态快照
  -> NGD提交Kubernetes
  -> PRC Watch到NGD
  -> PRC调用Algorithm
  -> Algorithm调用Python Worker
  -> PRC生成并提交NGG
  -> 从Kubernetes GET最终NGG
```

### 4. 逐断点查看

#### 断点1：Algorithm服务启动后读取Prometheus

位置：

```text
algorithm_server/go/algorithm/metrics.go:74
```

对应代码：

```go
func (c *metricsCache) refresh(ctx context.Context)
```

此时Algorithm HTTP Listener已经启动，后台协程正在执行首次采集。可查看Prometheus地址和刷新周期：

```text
p c.baseURL
p c.interval
```

继续执行14项PromQL查询并进入下一断点：

```text
n
c
```

`refresh`会对Mock Prometheus发出14项PromQL请求，携带`Authorization: Bearer go-test-prometheus-token`，解析标准Prometheus响应并写入Algorithm内存快照。

#### 断点2：PRC向Algorithm同步静态快照

位置：

```text
prc/pkg/controller/static_snapshot_controller.go:194
```

对应代码：

```go
ack, err := algorithm.putStatic(ctx, staticID, staticBody)
```

查看静态快照身份和输入规模：

```text
p staticID
p len(nodes.Items)
p len(topologies.Items)
```

`staticBody`包含1000个Node，不建议在终端完整打印。继续后，PRC通过真实HTTP PUT将快照写入Algorithm静态缓存：

```text
c
```

#### 断点3：NGD提交Kubernetes

位置：

```text
go_test_suites/group4_full_real_algorithm/group4_test.go:177
```

对应代码：

```go
apiClient.Create(setupContext, demand)
```

查看完整联通NGD：

```text
p demand.Object
```

执行Kubernetes Create，再等待PRC Watch断点：

```text
n
c
```

#### 断点4：PRC Watch到NGD

位置：

```text
prc/pkg/controller/prc_controller.go:106
```

对应代码：

```go
func (r *NodeGroupDemandReconciler) Reconcile(ctx context.Context, request ctrl.Request)
```

查看Watch事件对应的对象：

```text
p request.Name
p request.Namespace
```

预期：

```text
request.Name = "go-test-demand"
request.Namespace = ""
```

空Namespace说明这是Cluster-scoped正式NGD。继续：

```text
c
```

#### 断点5：PRC调用Algorithm

位置：

```text
prc/pkg/controller/prc_controller.go:279
```

对应代码：

```go
response, err := algorithm.calculate(ctx, algorithmRequest, ...)
```

查看PRC通过HTTP发送的任务请求：

```text
p algorithmRequest["requestId"]
p algorithmRequest["nodeStaticSnapshotId"]
p algorithmRequest["schedulerStateSnapshotId"]
p algorithmRequest["ngd"]
p len(schedulerState)
```

任务请求携带完整NGD、Node动态状态和静态快照Hash，不重复传输完整静态快照。继续：

```text
c
```

#### 断点6：Algorithm调用Python Worker

位置：

```text
algorithm_server/go/algorithm/service.go:41
```

对应代码：

```go
result, workerErr := s.worker.calculate(ctx, workerPayload{...})
```

查看Go即将交给Python的四类数据：

```text
p requestCopy["requestId"]
p requestCopy["ngd"]
p len(normalized)
p static.SnapshotID
p len(static.Nodes)
p metric.SnapshotID
p len(metric.Nodes)
```

Delve只能调试Go进程，不能直接`step`进入独立Python子进程。这里使用`c`，Python输入输出可在结果目录查看：

```text
actual/go-to-python-request.jsonl
actual/python-to-go-response.jsonl
```

继续：

```text
c
```

#### 断点7：PRC生成并提交NGG

位置：

```text
prc/pkg/controller/prc_controller.go:546
```

对应代码：

```go
r.Create(ctx, grant)
```

到达这里时，PRC已经选择Algorithm排名第一的候选组，并构造好完整NGG；当前行尚未提交Kubernetes。查看：

```text
p grant.Object
```

执行Create并继续：

```text
n
c
```

#### 断点8：从Kubernetes GET最终NGG

位置：

```text
go_test_suites/group4_full_real_algorithm/group4_test.go:206
```

对应代码：

```go
apiClient.Get(waitContext, client.ObjectKey{Name: "ngg-go-test-demand"}, persistedGrant)
```

先执行GET：

```text
n
```

再查看从Kubernetes API Server读取的持久化对象：

```text
p persistedGrant.Object
p persistedGrant.Object["spec"]
p persistedGrant.Object["status"]
```

预期NGG名称为`ngg-go-test-demand`，`status.phase`为`Active`。最后继续完成Golden比较：

```text
c
```

正常结束应显示：

```text
--- PASS: TestGroup4_PRCWatchesNGDCallsRealAlgorithmAndCreatesNGG
PASS
```

### 5. VS Code运行方式

1. 用VS Code打开项目根目录`/mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo`；
2. 打开上述8个文件位置，在行号左侧单击设置红色断点；
3. 按`Ctrl+Shift+D`打开“运行和调试”；
4. 选择`Debug Group4 - Full component chain`；
5. 第一次按`F5`启动，之后每次按`F5`继续到下一断点；
6. `F10`执行当前行，`F11`进入当前Go函数，`Shift+F5`停止调试。

VS Code配置位于项目根目录`.vscode/launch.json`，已经设置：

```text
NGG_TEST_DEBUG=true
NGG_TEST_DEBUG_TIMEOUT=10m
-test.timeout=30m
```

### 6. 常用Delve命令

| 命令 | 作用 |
|---|---|
| `c` | 继续到下一个断点 |
| `n` | 执行当前行但不进入函数 |
| `s` | 进入当前Go函数 |
| `p variable` | 打印变量 |
| `locals` | 查看当前函数的局部变量 |
| `args` | 查看当前函数参数 |
| `breakpoints` | 查看断点列表和编号 |
| `clear <编号>` | 删除指定断点 |
| `clearall` | 删除全部断点 |
| `exit` | 退出Delve |

### 7. 结果文件和时间解释

每次运行生成：

```text
go_test_suites/group4_full_real_algorithm/results/<run-id>/
├── actual/
│   ├── ngd-input.yaml
│   ├── prc-allocation-request.json
│   ├── prc-algorithm-http.json
│   ├── go-to-python-request.jsonl
│   ├── python-to-go-response.jsonl
│   ├── algorithm-response.json
│   ├── ngg-raw.yaml
│   └── ngd-status.yaml
├── comparison/
│   └── diff.txt
└── debug-timing.json
```

Debug模式会输出类似：

```text
debugTiming performanceValid=false elapsedMs=122448.767
```

该时间包含人工停在断点的时间，只用于说明调试链路持续了多久，不能作为性能结果。正式性能时间必须使用不带Debug环境变量的普通`go test`。

### 8. 常见问题

#### `context deadline exceeded`

确认使用了：

```text
NGG_TEST_DEBUG=true
NGG_TEST_DEBUG_TIMEOUT=10m
```

单独增加`-test.timeout`不能替代测试内部和PRC Algorithm请求超时。

#### `Prometheus metric snapshot is stale`

旧Debug配置使用生产默认120秒有效期，断点暂停超过两分钟后会进入Degraded并导致Golden差异。当前Debug模式已经将指标有效期扩展为30分钟；普通测试和正式服务仍保持原规则。

#### 静态Controller断点重复命中

静态Controller会周期性Reconcile。第一次确认`staticID`和HTTP PUT后，可用`breakpoints`查看编号，再用`clear <编号>`删除该断点。

#### 无法单步进入Python

Python Worker是独立操作系统进程，Go通过JSONL stdin/stdout调用。Delve只调试Go进程；Python请求和响应通过`actual/*.jsonl`检查。
