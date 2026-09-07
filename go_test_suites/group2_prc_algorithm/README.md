# 第二组：PRC调用完整Algorithm

验证PRC生产快照代码和AlgorithmClient通过HTTP调用真实Go Algorithm；Algorithm读取带Bearer认证的Go Mock Prometheus，再调用真实Python Worker。

本组不启动Kubernetes API Server，不测试Watch和NGG写入。

```bash
cd /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo
source scripts/go-test-env.sh
go test -p=1 ./go_test_suites/group2_prc_algorithm -run '^TestGroup2_' -v -count=1
```

静态快照PUT并确认Ready、Prometheus指标预热都在计时前完成。计时只覆盖PRC发送`POST /api/v1/allocate`到PRC解析Allocate响应，这与3000 Node整组测试的Group2口径一致。

## 测试流程

```text
本地1000 Node、Pod和正式NGD
  -> PRC BuildStaticSnapshot生成内容Hash静态快照
  -> PRC BuildSchedulerState聚合Node动态状态
  -> Mock Prometheus验证401/403/422
  -> 真实Algorithm预热14项指标缓存
  -> PRC AlgorithmClient PUT静态快照
  -> 查询静态缓存状态并确认snapshotId Ready
  -> 开始计时
  -> PRC AlgorithmClient POST Allocate
  -> Algorithm将五级topologyLabels中的物理名称映射为逻辑域
  -> 指定Spine不存在，记录Warning并转换为Border requiredSame
  -> Go Algorithm调用真实Python Worker
  -> PRC解析候选组响应
  -> 停止计时并写入本组results
```

Mock Prometheus严格校验`Authorization: Bearer go-test-prometheus-token`和正式指标目录中的完整PromQL，并返回标准`vector`响应。认证错误Case和指标预热都在主链路计时前完成。

输入位于`testdata/input/`，包含Node、Pod、NGD、静态/动态视图、Prometheus指标和Allocate请求。主要输出为：

- `node-static-snapshot.json`、`scheduler-state.json`；
- `prometheus-requests.jsonl`；
- `go-to-python-request.jsonl`、`python-to-go-response.jsonl`；
- `prc-algorithm-http.json`；
- `static-ack.json`、`algorithm-response.json`及对应Diff。

主要断言包括Hash应答关系、静态缓存Ready、动态状态Hash关系、Prometheus 14项查询、五级拓扑约束、Spine回退Warning、固定流水线顺序和候选组完整性。`go test -v`会直接打印`businessTiming ... elapsedMs=...`；末尾的包耗时包含环境准备，不能当作业务时间。
