# 第一组：Go Algorithm调用Python Worker

验证Go使用生产Worker管理代码启动Python，并通过stdin/stdout JSONL执行固定`requirement -> topology -> loadbalance`流水线。

输入位于`testdata/input/`，预期输出位于`testdata/expected/`，每次实际结果位于本目录`results/<run-id>/`。

```bash
cd /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo
source scripts/go-test-env.sh
go test -p=1 ./go_test_suites/group1_algorithm_worker -run '^TestGroup1_' -v -count=1
```

通用Fixture先在计时前转换成生产Worker协议类型。计时只包含Go写入已准备好的JSONL请求至Go解析正式响应，不包含Fixture转换、展示map转换和文件写入，与3000 Node整组Group1保持相同口径。

## 测试流程

```text
固定1000 Node Worker Payload
  -> Go NewPythonWorker启动python3 -m algorithm_worker.worker
  -> 计时前PreparePythonRequest转换成正式Worker协议类型
  -> 开始计时
  -> Go通过stdin发送workerEnvelope JSONL
  -> Python requirement过滤占用、标签及具体逻辑域不匹配Node
  -> Python topology按Leaf/空Spine/Border Domain/Room/DataCenter分组
  -> Python loadbalance评分并返回Top-3
  -> Go从stdout读取JSONL、校验消息ID并解析结果
  -> 停止计时
  -> 转换为展示map
  -> 保存协议证据、Actual和Diff
```

输入文件：

- `testdata/input/worker-payload.json`：Go发送的完整上下文；
- `worker-payload.json.request.topologyConstraints`：Algorithm Go层已经把联通五级`topologyLabels`解析成的内部逻辑域约束；
- `testdata/input/node-static-snapshot.json`：1000 Node静态资源和直连Leaf；
- `testdata/input/network-topology.yaml`：联通式独立上层拓扑、空SPINE和双Border Domain；
- `testdata/input/resolved-node-static-snapshot.json`：Go拓扑层补齐后传给Python的完整静态输入；
- `testdata/input/node-dynamic-state.json`：请求级`inUse`状态；
- `testdata/input/prometheus-metrics.json`：已经准备好的14项Node指标。

输出文件：

- `testdata/expected/worker-result.json`：固定预期；
- `results/<run-id>/actual/go-to-python-request.jsonl`：真实Go输入；
- `results/<run-id>/actual/python-to-go-response.jsonl`：真实Python输出；
- `results/<run-id>/actual/worker-result.json`：标准化实际结果；
- `results/<run-id>/comparison/diff.txt`和`timing.txt`。

主要断言包括固定三段流水线顺序、候选组Top-3、连续rank、具体Node及分数和Worker正常退出。
