# 第二组：PRC调用完整Algorithm

验证PRC生产快照代码和AlgorithmClient通过HTTP调用真实Go Algorithm；Algorithm读取带Bearer认证的Go Mock Prometheus，再调用真实Python Worker。

本组不启动Kubernetes API Server，不测试Watch和NGG写入。

```bash
cd /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo
source scripts/go-test-env.sh
go test -p=1 ./go_test_suites/group2_prc_algorithm -run '^TestGroup2_' -v -count=1
```

计时从PRC发送静态快照PUT开始，到PRC解析Allocate响应结束。Prometheus预热、输入读取和结果写盘不计时。

## 测试流程

```text
本地1000 Node、Pod和正式NGD
  -> PRC BuildStaticSnapshot生成内容Hash静态快照
  -> PRC BuildSchedulerState聚合Node动态状态
  -> Mock Prometheus验证401/403/422
  -> 真实Algorithm预热14项指标缓存
  -> 开始计时
  -> PRC AlgorithmClient PUT静态快照
  -> PRC AlgorithmClient POST Allocate
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

主要断言包括Hash应答关系、动态状态Hash关系、Prometheus 14项查询、固定流水线顺序和候选组完整性。
