# Go Test运行说明

本目录中的测试直接调用生产Go包，每组独立保存固定输入、预期输出和本次实际结果。Group1～Group5保留原有编号；Group7专门补充Bond主备/负载模式全链路。

这套测试在本机进程中运行，不需要外部集群、容器运行时或外部 Prometheus：

- 第一、二组不启动 Kubernetes；
- 第三、四、五、七组通过envtest启动本地`kube-apiserver`和`etcd`；
- 第二、四、五、七组使用带Bearer Token认证的Go Mock Prometheus；
- 第一、二、四、五、七组由Go Algorithm启动真实Python Worker。

## 1. 最快运行方式

进入项目根目录：

```bash
cd /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo
```

串行运行全部现有测试组：

```bash
make go-test-all
```

`Makefile` 会自动加载 `scripts/go-test-env.sh`，所以使用上述 `make` 命令时不需要手动配置 Go、缓存和 envtest 路径。

所有组都出现`PASS`且最后没有`FAIL`，即表示测试全部通过。

## 2. 运行环境检查

本项目当前使用数据盘中的工具，避免占用系统盘：

| 工具                | 当前路径或用途                                             |
| ------------------- | ---------------------------------------------------------- |
| Go                  | `/mnt/data0/tools/go/bin/go`                             |
| Delve               | `/mnt/data0/tools/bin/dlv`                               |
| Python 3            | 启动真实 Python Algorithm Worker                           |
| envtest             | `.cache/envtest/1.35.5/`中的 API Server、etcd 和 kubectl |
| Go module/build缓存 | 项目下`.cache/go-mod`和`.cache/go-build`               |

直接执行 `go test` 或命令行 Debug 前，先加载环境：

```bash
cd /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo
source scripts/go-test-env.sh
```

然后检查工具：

```bash
go version
python3 --version
dlv version
test -x "$KUBEBUILDER_ASSETS/kube-apiserver"
test -x "$KUBEBUILDER_ASSETS/etcd"
```

最后两个命令没有输出且退出码为 0，表示 envtest 二进制存在。正式测试统一设置 `GOMAXPROCS=2`，并使用 `go test -p=1`，避免共享服务器瞬时并发过高。

## 3. 各组分别验证什么

| 组     | 验证内容                            | Algorithm                      | Kubernetes              |
| ------ | ----------------------------------- | ------------------------------ | ----------------------- |
| 第一组 | Go 调用 Python Worker及JSONL协议    | 真实Python Worker              | 不启动                  |
| 第二组 | PRC快照/HTTP客户端调用完整Algorithm | 真实Go+Python，Mock Prometheus | 不启动                  |
| 第三组 | PRC Watch NGD并生成正式NGG          | Mock Algorithm                 | envtest API Server+etcd |
| 第四组 | PRC—Algorithm—NGG完整组件链路     | 真实Go+Python，Mock Prometheus | envtest API Server+etcd |
| 第五组 | PRC周期刷新、修改与删除生命周期 | 真实Go+Python，Mock Prometheus | envtest API Server+etcd |
| 第七组 | Bond主备/负载模式到NGG完整链路 | 真实Go+Python，Mock Prometheus | envtest API Server+etcd |

各组更详细的输入、流程和断言见：

- [第一组说明](group1_algorithm_worker/README.md)
- [第二组说明](group2_prc_algorithm/README.md)
- [第三组说明](group3_prc_ngd_ngg/README.md)
- [第四组说明](group4_full_real_algorithm/README.md)
- [第五组说明](group5_prc_refresh_lifecycle/README.md)
- [第七组说明](group7_bond_topology_flow/README.md)

## 4. 分组运行

以下命令都从项目根目录执行。

### 4.1 第一组：Go 调用 Python Worker

```bash
make go-test-group1
```

它验证：Go 启动 Python Worker，通过 stdin/stdout 发送和接收 JSONL，Python 按 `requirement -> topology -> loadbalance` 运行，并向 Go 返回 Top-3 候选组及具体 Node。

### 4.2 第二组：PRC 调用完整 Algorithm

```bash
make go-test-group2
```

它验证：PRC 构造 1000 Node 的静态快照和动态状态，通过 HTTP 调用真实 Go Algorithm；Algorithm 读取带认证的 Mock Prometheus，再调用真实 Python Worker并返回结果。本组不启动 Kubernetes。

### 4.3 第三组：PRC Watch NGD并生成NGG

```bash
make go-test-group3
```

它验证：envtest 启动本地 API Server/etcd并安装联通 NGD/NGG CRD，真实 PRC Watch 到 NGD 后调用 Mock Algorithm，将算法第一候选组写成一个正式 NGG，并更新 NGD/NGG Status。

### 4.4 第四组：完整组件链路

```bash
make go-test-group4
```

它验证完整链路：

```text
NGD -> Kubernetes Watch -> PRC -> Go Algorithm
    -> Mock Prometheus -> Python Worker -> 候选组
    -> PRC -> NGG + NGD/NGG Status
```

本组验证到 NGG 生成，不包含下游 Pod Bind。

### 4.5 第五组：周期刷新生命周期

```bash
make go-test-group5
```

### 4.6 第七组：Bond拓扑完整链路

```bash
make go-test-group7
```

它分别验证Active-Backup只选择Active Slave，以及802.3ad选择全部Up Slave；随后使用真实PRC、Algorithm和Python生成正式NGG。

### 4.7 一次运行全部组

```bash
make go-test-all
```

该目标串行执行Topology Agent单测及Group1、2、3、4、5、7；任何一组失败，`make`会以失败状态退出。

## 5. 不通过 Makefile直接运行

先加载环境：

```bash
source scripts/go-test-env.sh
```

再选择一组运行：

```bash
go test -p=1 ./go_test_suites/group1_algorithm_worker -run '^TestGroup1_' -v -count=1
go test -p=1 ./go_test_suites/group2_prc_algorithm -run '^TestGroup2_' -v -count=1
go test -p=1 ./go_test_suites/group3_prc_ngd_ngg -run '^TestGroup3_' -v -count=1
go test -p=1 ./go_test_suites/group4_full_real_algorithm -run '^TestGroup4_' -v -count=1
go test -p=1 ./go_test_suites/group5_prc_refresh_lifecycle -run '^TestGroup5_' -v -count=1
go test -p=1 ./go_test_suites/group7_bond_topology_flow -run '^TestGroup7_' -v -count=1
```

参数含义：

- `-p=1`：Go 包串行编译/测试，降低并发资源占用；
- `-run '^TestGroupX_'`：只运行指定组的测试函数；
- `-v`：显示详细步骤、结果目录和业务耗时；
- `-count=1`：禁用 Go Test结果缓存，确保每次真正重新执行。

## 6. 输入、预期输出和实际输出在哪里

每组目录结构相同：

```text
groupX_xxx/
├── README.md
├── groupX_test.go
├── testdata/
│   ├── input/          # 固定输入；原始是什么格式就保留什么格式
│   └── expected/       # 纳入Git的Golden预期输出
└── results/
    └── <run-id>/       # 每次运行新建，不覆盖上一次
        ├── actual/     # 本次真实输入、协议过程和输出
        ├── comparison/
        │   └── diff.txt
        └── timing.txt
```

运行后先查看最新一次结果目录：

```bash
ls -1dt go_test_suites/group1_algorithm_worker/results/* | head -1
ls -1dt go_test_suites/group2_prc_algorithm/results/* | head -1
ls -1dt go_test_suites/group3_prc_ngd_ngg/results/* | head -1
ls -1dt go_test_suites/group4_full_real_algorithm/results/* | head -1
ls -1dt go_test_suites/group5_prc_refresh_lifecycle/results/* | head -1
ls -1dt go_test_suites/group7_bond_topology_flow/results/* | head -1
```

以第一组为例，查看最新结果：

```bash
latest_result=$(ls -1dt go_test_suites/group1_algorithm_worker/results/* | head -1)
find "$latest_result" -maxdepth 2 -type f | sort
cat "$latest_result/timing.txt"
cat "$latest_result/comparison/diff.txt"
```

`comparison/diff.txt`为空表示实际输出与Expected一致。四组最关键的证据如下：

| 组     | 关键Actual文件                                                                             |
| ------ | ------------------------------------------------------------------------------------------ |
| 第一组 | `go-to-python-request.jsonl`、`python-to-go-response.jsonl`、`worker-result.json`    |
| 第二组 | 静态/动态快照、`prometheus-requests.jsonl`、PRC HTTP记录、Algorithm响应和Go/Python JSONL |
| 第三组 | `ngd-input.yaml`、PRC发给Mock Algorithm的请求、Mock响应、`ngg-raw.yaml`和Status        |
| 第四组 | NGD、静态/动态数据、Prometheus请求、Go/Python JSONL、Algorithm结果、NGG和Status            |
| 第五组 | 初次、周期刷新、更新、消费方字段保留、删除及全部Algorithm交换记录                         |
| 第七组 | Bond/Mock LLDP输入、Node Leaf元数据、静态快照、Algorithm请求/响应及正式NGG              |

`results/`是运行证据目录，不作为固定测试源码提交；`testdata/input/`和`testdata/expected/`才是可复现的固定基线。

## 7. 时间怎么看

测试输出中的 `go test` 总耗时和 `timing.txt` 的含义不同：

- `go test` 总耗时包括本组涉及的进程启动、Fixture读取、envtest/CRD/Node预置（仅第三、四组）、证据写盘和清理；
- `timing.txt`只记录方案约定的真实业务边界，不计算生成输入和写结果文件的时间。
- `go test -v`同时打印`businessTiming boundary=... elapsedMs=...`，应以这个毫秒值或`timing.txt`为准。

各组业务计时边界：

| 组     | 开始                                      | 结束                          |
| ------ | ----------------------------------------- | ----------------------------- |
| 第一组 | Go发送已准备好的Python JSONL请求          | Go解析完Python JSONL响应      |
| 第二组 | PRC发送POST Allocate（静态和指标已Ready） | PRC收到并解析Allocate响应     |
| 第三组 | PRC通过Watch观察到NGD并进入Reconcile      | NGG Active                    |
| 第四组 | PRC通过Watch观察到NGD并进入Reconcile      | 真实Algorithm返回且NGG Active |
| 第五组 | 各生命周期事件提交                        | 相应刷新、更新或删除完成       |
| 第七组 | PRC通过Watch观察到NGD并进入Demand Processor | 真实Algorithm/Python返回且NGG Active |

第三、四组在创建NGD前，由独立`NodeStaticSnapshotReconciler`完成静态快照构造、Hash计算和PUT，并等待Algorithm确认Ready；这些步骤不计入NGD业务时间。Prometheus同样在计时前由Algorithm独立读取并预热。任务Reconcile只读取已确认的`snapshotId`并发送Node动态状态。

相同真实Algorithm输入下，调用包含关系应为`Group1 JSONL子区间 < Group2 Algorithm HTTP子区间 < Group4完整PRC链路`。单组测试用于功能和Debug，只记录一次；正式性能判断使用3000 Node、每档30次的Mean/P50/P95，聚合报告会自动检查`Group4 Mean > Group2 Mean`。

所以第三、四组可能显示几十秒的`go test`总耗时，但业务链路通常是毫秒到秒级；展示性能时应读取日志中的`businessTiming`或对应`timing.txt`。

## 8. VS Code断点Debug

1. 使用 VS Code打开项目根目录：

   ```text
   /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo
   ```
2. 打开左侧“运行和调试”。
3. 在下拉框选择以下任一配置：

   - `Debug Group1 - Go calls Python`
   - `Debug Group2 - PRC calls Algorithm`
   - `Debug Group3 - PRC watches NGD with Mock Algorithm`
   - `Debug Group4 - Full component chain`
   - `Debug Group5 - PRC refresh update delete`
   - `Debug Group7 - Bond topology full flow`
4. 在测试代码或生产 Go 代码中设置断点。
5. 按 `F5`启动。

建议断点位置：

- 第一组：`algorithm_server/go/algorithm/worker.go`，查看 Go 写入和解析 JSONL；
- 第二组：`prc/pkg/controller/algorithm_client.go`，查看静态快照PUT和Allocate请求；
- 第三组：`prc/pkg/controller/static_snapshot_controller.go`查看独立静态同步，`prc/pkg/controller/prc_controller.go`查看任务Reconcile、NGD转换和NGG创建；
- 第四组：同时在上述 PRC、Algorithm代码处设断点，观察完整调用链。
- 第七组：在`pkg/bond/discovery.go`、`pkg/topologyfacts/facts.go`、PRC静态快照及Algorithm拓扑解析处设断点。

Delve只能单步调试 Go 主进程，不能直接进入子进程中的 Python 代码。Python 的真实输入输出可通过本次结果目录中的 `go-to-python-request.jsonl`、`python-to-go-response.jsonl`和pipeline trace查看。

## 9. 命令行Delve Debug

先加载环境，再启动相应测试：

```bash
source scripts/go-test-env.sh
dlv test ./go_test_suites/group1_algorithm_worker -- -test.run '^TestGroup1_' -test.v -test.count=1
```

完整链路可使用：

```bash
dlv test ./go_test_suites/group4_full_real_algorithm -- -test.run '^TestGroup4_' -test.v -test.count=1
```

进入 Delve 后常用命令：

```text
b 文件路径:行号   设置断点
c                 继续运行
n                 单步到下一行
s                 进入函数
p 变量名          查看变量
q                 退出
```

## 10. Expected/Golden如何更新

普通运行只比较 Actual与Expected，不会覆盖Expected。发生不一致时先查看：

```bash
cat go_test_suites/<组目录>/results/<run-id>/comparison/diff.txt
```

只有业务协议或预期结果被有意修改，并且人工确认新的Actual正确后，才能显式更新Golden。例如：

```bash
source scripts/go-test-env.sh
UPDATE_GOLDEN=1 go test -p=1 ./go_test_suites/group1_algorithm_worker -run '^TestGroup1_' -v -count=1
git diff -- go_test_suites/group1_algorithm_worker/testdata/expected
```

不要为了让失败测试变绿而直接更新Golden；否则会把真实回归问题写成新的“正确结果”。

## 11. 常见问题

### `go: command not found`

当前终端没有加载数据盘Go环境：

```bash
source scripts/go-test-env.sh
go version
```

使用 `make go-test-groupX` 时会自动加载，不应出现该问题。

### 找不到`kube-apiserver`或`etcd`

第三、四组需要 envtest 资源：

```bash
source scripts/go-test-env.sh
ls -l "$KUBEBUILDER_ASSETS/kube-apiserver"
ls -l "$KUBEBUILDER_ASSETS/etcd"
```

第一、二组不依赖 envtest，可以单独运行。

### 出现`listen tcp: operation not permitted`

第二至第四组会在本机回环地址启动临时 HTTP或API服务。该错误通常表示当前执行沙箱禁止监听本地端口；请在服务器正常终端中执行，或为测试进程开放本地回环监听权限。

### `comparison/diff.txt`不为空或测试报Golden不一致

先查看Actual和Diff，判断是实现回归、输出顺序变化，还是需求确实修改。未确认前不要设置 `UPDATE_GOLDEN=1`。

### 第三、四组比第一、二组慢

这是正常现象。第三、四组会启动 API Server/etcd、安装CRD并创建1000个Node API对象，这些准备时间不属于业务耗时；以 `timing.txt`为准。

## 12. 公共测试代码作用

| 文件                          | 功能                                                                                                          |
| ----------------------------- | ------------------------------------------------------------------------------------------------------------- |
| `common/fixture.go`         | 确定性生成1000 Node、2 Core/4 Border/50 Leaf拓扑、Node动态状态、Pod和14项Prometheus指标，并输出本地输入文件。 |
| `common/golden.go`          | 建立本组独立运行目录、保存Actual、比较Expected并输出Diff和业务耗时。                                          |
| `common/normalize.go`       | 清理时间、Kubernetes动态元数据、Boot ID和派生Hash，同时保留候选组与Node顺序。                                 |
| `common/mock_prometheus.go` | 使用Go`httptest`模拟Prometheus查询、Bearer认证、标准vector响应和请求记录。                                  |
| `common/envtest.go`         | 启动API Server/etcd、安装联通CRD、预置1000 Node并提供状态轮询。                                               |
| `common/process.go`         | 定位项目根目录并向真实Algorithm注入Python Worker模块路径。Worker关闭由正式`Application.Close()`负责。       |

## 13. 3000 Node规模性能矩阵

独立规模测试位于[`scale_benchmark_3000/`](scale_benchmark_3000/README.md)，不会修改本目录四组1000 Node功能测试的输入和Expected。

它固定准备3000个静态Node，分别选择1000、800、500、300、100和10个Node；四组中每个规模预热3次、正式计时30次，自动生成Mean、P50、P95、Min、Max、标准差和24行总表。

```bash
cd /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo
make benchmark-3000-all
```

详细拓扑、计时边界、输入输出、Debug方法和2026年8月25日正式结果见[3000 Node测试README](scale_benchmark_3000/README.md)。
