# 第三组：PRC Watch NGD并生成NGG

envtest启动真实`kube-apiserver`和`etcd`，安装联通正式NGD/NGG CRD。测试预置1000个Node后创建正式NGD，验证真实PRC Watch、Reconcile、Mock Algorithm调用、正式NGG创建和Status更新。

```bash
cd /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo
source scripts/go-test-env.sh
go test -p=1 ./go_test_suites/group3_prc_ngd_ngg -run '^TestGroup3_' -v -count=1
```

计时前已经完成envtest启动、CRD安装、1000 Node/Pod及Leaf标签预置、PRC Cache Sync和独立静态快照同步。计时从PRC通过Watch观察到NGD并进入Reconcile开始，到NGG为`Active`结束。静态快照构造与PUT不在任务计时内；NGD `Fulfilled`继续作为正确性断言，但不计时。

## Kubernetes与Algorithm如何模拟

envtest启动真实本地`kube-apiserver`和`etcd`，不是fake client。测试安装：

- 联通`nodegroupdemand-crd.yaml`；
- 联通`nodegroupgrant-crd.yaml`；
- PRC兼容所需的旧版NGD CRD；当前静态快照只Watch Node，不安装或读取NodeNetworkTopology。

1000个Node只是API对象，测试先创建Node，再通过Status子资源写入Allocatable和Ready状态。Mock Algorithm只代替算法边界：它实现静态快照PUT/状态查询和Allocate接口，记录PRC请求，并按确定规则返回一个包含3个具体Node的候选组。

## 测试流程

```text
启动envtest并安装CRD
  -> 预置1000 Node和Pod
  -> 启动真实PRC Manager、静态快照Controller和NGD Reconciler
  -> 静态Controller在没有NGD时构造并PUT快照，等待StaticSnapshotState Ready
  -> 修改一个Node静态Label，验证Node Watch可独立生成并确认新Hash
  -> 记录静态PUT次数
  -> 通过Kubernetes API创建正式NGD
  -> API Server发送真实Watch事件
  -> PRC观察到NGD，开始计时
  -> PRC读取NGD、Node、Pod和已确认snapshotId，构造动态状态并调用Mock Algorithm
  -> PRC只把rank 1转换为联通正式NGG
  -> NGG Active后停止计时
  -> 继续断言NGD Fulfilled
  -> 断言任务Reconcile期间静态PUT次数没有增加
  -> 导出输入、PRC请求、Mock响应、NGG和Status
```

本组最外层输入是`testdata/input/ngd.yaml`和预置集群对象，最外层输出是正式NGG及NGD Status。实际文件在`results/<run-id>/actual/`，Expected在`testdata/expected/ngg-and-status.json`。

主要断言包括静态同步不依赖NGD、任务链路不重复PUT静态快照、真实Watch触发、PRC协议内容、正式NGG只有一组`spec.nodes`、Node来自算法rank 1，以及NGD/NGG状态正确。
