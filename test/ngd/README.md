# 三节点 NGD/NGG 分配测试

测试时间：2026-09-16（Asia/Shanghai）

## 测试目的

验证当前集群中的 Platform Resource Controller 与 Algorithm 能否根据节点标签、动态可用资源、`maxNodes`、`minResources`、`quota` 和网络拓扑生成正确的 NodeGroupGrant（NGG），并验证无解需求能否明确失败且不生成 NGG。

## 测试环境

当前可参与分配且具有 LLDP 拓扑的节点只有以下三台。三台节点均属于逻辑 Leaf 域 `pair-8e3df4c9cdcb`，对应同一组双 Leaf。

| 节点 | Allocatable CPU | NGG 计算时可用 CPU | NGG 计算时可用内存 | 算法得分 |
|---|---:|---:|---:|---:|
| `hl-tstmix7-paasmaster1` | 104 | 101830m | 480657050026 bytes（约 447.7 GiB） | 94 |
| `hl-tstmix7-paasmaster3` | 104 | 87430m | 466698406314 bytes（约 434.6 GiB） | 92 |
| `hl-tstmix7-paasmaster2` | 104 | 71890m | 450791508394 bytes（约 419.8 GiB） | 91 |

这里的 NGG 可用资源是 PRC 扣除当前 Pod 请求量后传给算法的动态值，因此小于 Node 的 `status.allocatable`。本轮 Algorithm 的 Prometheus 刷新正常：14 项指标查询成功，指标快照包含 4 个节点，`degraded=false`。

## 测试文件

| 文件 | 验证内容 | 预期 |
|---|---|---|
| `01-single-best.yaml` | 单节点择优，最少 1 CPU/1 GiB | 选择得分最高的 master1 |
| `02-pinned-master2.yaml` | `nodeSelector` 指定 hostname | 只选择 master2 |
| `03-two-node-resources.yaml` | 最少 150 CPU/700 GiB，最多 2 节点 | 选择两个节点并满足资源下限 |
| `04-three-node-resources.yaml` | 最少 230 CPU/1100 GiB，最多 3 节点 | 三个节点全部进入 NGG |
| `05-topology-same-leaf.yaml` | 数据中心、机房、Border、Spine、同 Leaf 约束 | 选择同一逻辑 Leaf 域的三个节点 |
| `06-explicit-leaf.yaml` | 指定一个物理 Leaf 交换机 | 映射到双 Leaf 逻辑域并选择三个节点 |
| `07-no-matching-selector.yaml` | 不存在的 Node Label | NGD Failed，不生成 NGG |
| `08-insufficient-resources.yaml` | 400 CPU/2 TiB，超过三节点总量 | NGD Failed，不生成 NGG |
| `09-quota-too-small.yaml` | Quota 小于任一完整节点的可用容量 | NGD Failed，不生成 NGG |
| `10-invalid-topology.yaml` | 不存在的数据中心 | 返回不可重试的拓扑参数错误 |

## 实际结果

| 用例 | NGD | NGG | 分配节点 | 汇总可用资源 | 结论 |
|---|---|---|---|---|---|
| 01 | Fulfilled，1 节点 | Active | master1 | 101830m / 480657050026 bytes | 得分择优生效 |
| 02 | Fulfilled，1 节点 | Active | master2 | 71890m / 450791508394 bytes | hostname 选择器生效 |
| 03 | Fulfilled，2 节点 | Active | master1、master3 | 189260m / 947355456340 bytes | 两节点资源聚合生效 |
| 04 | Fulfilled，3 节点 | Active | master1、master3、master2 | 261150m / 1398146964734 bytes | 三节点资源聚合生效 |
| 05 | Fulfilled，3 节点 | Active | master1、master3、master2 | 261150m / 1398146964734 bytes | `requiredSame` 拓扑约束生效 |
| 06 | Fulfilled，3 节点 | Active | master1、master3、master2 | 261150m / 1398146964734 bytes | 物理 Leaf 到逻辑双 Leaf 域映射生效 |
| 07 | Failed，0 节点 | 未生成 | 无 | 无 | 无匹配标签被正确拒绝 |
| 08 | Failed，0 节点 | 未生成 | 无 | 无 | 资源不足被正确拒绝 |
| 09 | Failed，0 节点 | 未生成 | 无 | 无 | Quota 上限检查生效 |
| 10 | Failed，0 节点 | 未生成 | 无 | 无 | 无效拓扑返回 `INVALID_TOPOLOGY_LABELS` |

成功用例统一选择 `leaf-domain:pair-8e3df4c9cdcb`。多节点结果始终按分数降序排列：master1（94）、master3（92）、master2（91）。

2026-09-23 复测中，原用例 05 的 `spine-01` 不在当前拓扑，算法正确返回 `TOPOLOGY_SWITCH_NOT_FOUND`。现已将 Spine 和 Border 都设为 `requiredSame`，在当前 Uplink Domain 模式下重新运行成功，生成包含三个节点的 NGG。

用例 09 说明 `quota` 是整个候选节点集合的资源上限。算法加入节点时按该节点的完整动态可用容量累加；本例 CPU Quota 为 80，而三台节点各自均超过 80 CPU，因此没有节点能够加入候选组。

失败用例 07、08、09 在 Algorithm 中均为 `UNSATISFIABLE`，PRC 将 NGD 标为 `Failed`，消息为 `NoFeasibleGroup`。用例 10 返回 HTTP 400、`INVALID_TOPOLOGY_LABELS`、`retryable=false`，PRC 同样正确标记为 `Failed`。

## 结论

当前 PRC → Algorithm → NGG 链路可以正常工作：节点标签过滤、动态资源计算、节点数量上限、最低资源、Quota、拓扑分组、Prometheus 评分排序以及失败状态处理均已生效。测试结束后已删除全部测试 NGD/NGG，集群查询结果为 `No resources found`。

目前拓扑分配只能覆盖 master1/2/3，因为只有这三台节点具有有效 LLDP Leaf 信息。master4、paasnode1、paasnode2 没有 LLDP 邻居标签，不会进入当前拓扑候选集合。

## 演示命令

进入目录：

```bash
cd /root/ghw/test/ngd
```

运行单个成功用例并观察结果：

```bash
kubectl apply -f 03-two-node-resources.yaml
kubectl get ngd test-ngd-03-two-node-resources -w
kubectl get ngg ngg-test-ngd-03-two-node-resources -o yaml
kubectl delete -f 03-two-node-resources.yaml
```

查看 PRC 和 Algorithm 日志：

```bash
kubectl logs -n welkin-system deployment/platform-resource-controller -f
kubectl logs -n welkin-system deployment/ngd-ngg-algorithm -f
```

重新运行完整测试：

```bash
cd /root/ghw/test/ngd
./run-tests.sh
cat results/summary.tsv
```

`run-tests.sh` 会先删除旧的 `go-test-demand` 以及带 `ngg.demo/test-suite=three-node` 标签的残留测试 NGD，然后逐个创建、采集并删除本目录中的 10 个测试对象。

原始结果保存在 `results/raw/`：成功用例同时保存 NGD 与 NGG YAML，失败用例保存 NGD YAML；机器可读汇总位于 `results/summary.tsv`。

## 最新复测

最新复测：[2026-09-23-v0.6.3-smoke-test.md](./2026-09-23-v0.6.3-smoke-test.md)。历史记录：[2026-09-16-v0.6.1-smoke-test.md](./2026-09-16-v0.6.1-smoke-test.md)。
