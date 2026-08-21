# NGD-NGG 三组演示测试

三组测试统一采用“两遍运行”：`timing-run`只计算业务时间，`evidence-run`重新使用相同的确定性输入生成原始输入、中间过程和输出。证据采集、Trace、序列化和文件写入不会污染正式性能时间。

## 一键运行

```bash
cd /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo

make demo-group1       # Algorithm：Cold/Warm Timing + Evidence
make demo-group2       # PRC：单一 normal_create Timing + Evidence
make demo-group3       # 完整链路：Cold/Warm Timing + Evidence
make demo-all-groups   # 三组全部运行
make demo-show-latest  # 展示每组最新结果和文件清单
```

每组也可单独运行一遍：

```bash
make demo-group1-timing
make demo-group1-evidence
make demo-group2-timing
make demo-group2-evidence
make demo-group3-timing
make demo-group3-evidence
```

`demo-group1`和`demo-group3`依赖最新 Algorithm 镜像，Make 会先执行`algorithm-image`。第二组使用可控 Mock Algorithm，只验证 PRC。

## 结果目录

```text
test_suites/<group>/runs/<run-id>/
├── timing-run/          # 第一遍：只保留业务耗时和结论
├── evidence-run/        # 第二遍：原始格式的输入、过程和输出
├── logs/                # 环境、构建和Runner日志
├── result.json
└── report.md
```

数据不强制转换成同一种格式：

| 数据                          | 文件格式 |
| ----------------------------- | -------- |
| NGD、NGG、Pod等Kubernetes对象 | YAML     |
| PRC与Algorithm HTTP协议       | JSON     |
| Go与Python stdin/stdout协议   | JSONL    |
| Prometheus请求审计            | JSONL    |
| 进程输出                      | LOG      |
| 演示摘要                      | Markdown |

## 第一组：Algorithm Server

运行真实Go Algorithm API Server和长期Python Worker，不启动Kubernetes或PRC进程。测试驱动模拟PRC，使用3000 Node静态、动态和14类Prometheus指标。

```bash
make demo-group1
./test_suites/show-latest.sh group1_algorithm cold_cache
./test_suites/show-latest.sh group1_algorithm warm_cache
```

Timing输出：

```text
timing-run/
├── timing-result.json   # Cold两个时间；Warm 30次分布
└── summary.md
```

- `prcSendToReceiveMs`：模拟PRC发送到接收并解码。
- `algorithmProcessingMs`：Go HTTP Handler接收请求到候选结果完成。
- Cold包含静态PUT和首次指标Ready；Warm不包含缓存准备。

Evidence每个Cold/Warm Case输出：

```text
evidence-run/<case>/
├── 01-prc-go-http/
│   ├── prc-to-go-request.json
│   ├── node-dynamic-state.json
│   └── go-to-prc-response.json
├── 02-go-cache/
│   ├── node-static-snapshot.json
│   ├── prometheus-metric-snapshot.json
│   └── cache-status.json
├── 03-go-python-worker/
│   ├── go-to-python-request.jsonl
│   └── python-to-go-response.jsonl
├── 04-python-pipeline/
│   ├── 01-requirement-input.json
│   ├── 01-requirement-output.json
│   ├── 02-topology-input.json
│   ├── 02-topology-output.json
│   ├── 03-loadbalance-input.json
│   └── 03-loadbalance-output.json
└── 05-final/allocation-response.json
```

`go-to-python-request.jsonl`是Go写入Python stdin的原始一行消息，包含Go规范化后的动态状态、实际静态缓存和实际Prometheus缓存；`python-to-go-response.jsonl`是Python stdout的原始一行响应。

## 第二组：PRC normal_create

运行真实envtest API Server/etcd、真实PRC Manager/Reconciler和可控Mock Algorithm，只模拟一种正常情况。

```bash
make demo-group2
./test_suites/show-latest.sh group2_prc normal_create
```

Timing边界：PRC Reconciler收到该NGD UID/generation并开始处理，到正式NGG和NGD/NGG Status Ready。envtest、3000对象准备、Manager Cache Sync和文件写入不计时。

Evidence输出：

```text
evidence-run/normal_create/
├── input/ngd-input.yaml
├── process/
│   ├── prc-static-snapshot-request.json  # 3000 Node静态属性和三层拓扑
│   ├── prc-static-snapshot-response.json
│   ├── prc-allocation-request.json       # 3000 Node动态状态
│   ├── algorithm-result.json
│   └── prc-algorithm-http.jsonl
└── output/
    ├── ngd-final.yaml
    └── ngg-generated.yaml
```

## 第三组：完整链路

运行envtest、真实PRC、真实Go Algorithm、真实Python Worker和NGG Consumer/Binding模拟。主演示只保留`cold_cache`和`warm_cache`。

```bash
make demo-group3
./test_suites/show-latest.sh group3_full_chain cold_cache
./test_suites/show-latest.sh group3_full_chain warm_cache
```

Timing边界：PRC Reconciler收到NGD Watch，到NGG生效并完成20个Pod的模拟Binding。

Evidence每个Case输出：

```text
evidence-run/<case>/
├── input/
│   ├── ngd-input.yaml
│   └── pending-pods.yaml
├── process/
│   ├── prc-static-snapshot-request.json
│   ├── prc-allocation-request.json
│   ├── algorithm-result.json
│   ├── prc-algorithm-http.jsonl
│   └── ngg-consumer/
└── output/
    ├── ngd-final.yaml
    ├── ngg-generated.yaml
    └── pods-after-binding.yaml
```

演示时依次打开`ngd-input.yaml`、`algorithm-result.json`、`ngg-generated.yaml`和`pods-after-binding.yaml`，即可说明需求、算法候选、PRC授权和最终绑定结果。

## 代码入口

| 功能                 | 文件                               |
| -------------------- | ---------------------------------- |
| 3000 Node确定性数据  | `common/fixture.py`              |
| Mock Prometheus      | `common/mock_prometheus.py`      |
| 时间分布和结果写入   | `common/runtime.py`              |
| 第一组               | `group1_algorithm/run_group.py`  |
| 第二组包装           | `group2_prc/run_group.py`        |
| 第二/三组envtest核心 | `group2_prc/runner/main.go`      |
| 第三组包装           | `group3_full_chain/run_group.py` |
| 最新结果展示         | `show-latest.sh`                 |
