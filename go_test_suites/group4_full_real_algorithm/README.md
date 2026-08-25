# 第四组：完整PRC—Algorithm—NGG组件链路

本组使用envtest真实Watch、真实PRC、真实Go Algorithm Application、真实Python Worker和带Bearer认证的Go Mock Prometheus，最终生成联通正式NGG。

```bash
cd /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo
source scripts/go-test-env.sh
go test -p=1 ./go_test_suites/group4_full_real_algorithm -run '^TestGroup4_' -v -count=1
```

计时前已经完成Prometheus指标预热、envtest启动、1000 Node/Pod及拓扑标签预置和PRC Cache Sync。计时从PRC通过Watch观察到NGD并进入Reconcile开始，到真实Algorithm返回且NGG为`Active`结束。NGD `Fulfilled`继续作为正确性断言，但不计时。

本组不启动Volcano或kube-scheduler，因此验证到正式NGG生成，不验证Pod最终Bind。

## 完整流程

```text
启动带Bearer认证的Go Mock Prometheus
  -> 启动真实Go Algorithm Application和真实Python Worker
  -> 完成14项Prometheus指标预热
  -> 启动envtest API Server/etcd并安装联通CRD
  -> 预置1000 Node和Pod
  -> 启动真实PRC Manager/Reconciler并等待Cache Sync
  -> 创建正式NGD
  -> PRC通过Watch观察到NGD，开始计时并进入Reconcile
  -> PRC PUT静态快照并POST动态状态和完整NGD
  -> Algorithm固定执行requirement/topology/loadbalance
  -> Algorithm返回最多3个候选组和具体Node
  -> PRC将rank 1写成联通正式NGG
  -> NGG Active后停止计时
  -> 继续断言NGD Fulfilled
  -> 保存全部Actual并与Expected比较
```

`testdata/input/`包含1000 Node、Pod、正式NGD、静态/动态数据和14项Prometheus指标。`results/<run-id>/actual/`保存：

- Mock Prometheus真实请求记录；
- PRC静态快照PUT和Allocate请求/响应；
- Go与Python的JSONL协议；
- Algorithm候选组和Pipeline Trace；
- 正式NGG Raw YAML、标准化结果和NGD Status。

主要断言包括认证及14项PromQL、固定三段流水线、Top-3排序、每组具体Node、NGG节点与rank 1逐项一致，以及正式CRD Status。
