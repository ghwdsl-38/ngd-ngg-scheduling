# NGD-NGG 集群演示手册

> **当前控制器名称**：Kubernetes Deployment 名称是 `platform-resource-controller`。旧名称 `prc` 已删除，不能再执行 `kubectl get deployment prc ...`。本文中的 PRC 仅作为 Platform Resource Controller 的简称。

正确检查命令：

```bash
kubectl get deployment platform-resource-controller ngd-ngg-algorithm -n welkin-system
```

## 简介

本演示展示从服务器网络拓扑发现到节点组授权生成的完整流程：

1. `lldp-agent` 在节点上发现上联 Leaf 交换机，并写入 Kubernetes Node 标签。
2. 提交 `NodeGroupDemand`（NGD）描述调度器、节点、资源和拓扑要求。
3. Platform Resource Controller（PRC）读取 NGD、Node、Pod 和拓扑标签，将计算请求发送给 Algorithm。
4. Algorithm 结合静态拓扑和 Prometheus 实时指标，对候选节点分组、过滤和评分。
5. Platform Resource Controller（PRC）创建 `NodeGroupGrant`（NGG），供 Volcano 等调度器消费。

当前演示环境已经验证：LLDP、Platform Resource Controller（PRC）、Algorithm 均正常运行，NGD 可以进入 `Fulfilled`，NGG 可以进入 `Active`。

今天实际执行的安装、部署、DNS 修复、验证和清理命令见：[2026-09-14 下午操作命令](2026-09-14-下午操作命令.md)。

## 当前演示环境

| 项目 | 当前值 | 用途 |
| --- | --- | --- |
| namespace | `welkin-system` | 部署 LLDP、Platform Resource Controller（PRC）和 Algorithm |
| master | `10.129.195.253` / `hl-tstmix7-paasmaster3` | 当前 Platform Resource Controller（PRC）、Algorithm 运行节点 |
| Platform Resource Controller（PRC）镜像 | `hlcsq-registry.cucloud.cn/paas/scheduling/prc:v0.6.0` | 本地缓存标签，实际内容来自 Docker Hub `prc-v0.6.1` |
| Algorithm 镜像 | `hlcsq-registry.cucloud.cn/paas/scheduling/algorithm:v0.6.0` | 本地缓存标签，实际内容来自 Docker Hub `algorithm-v0.6.1` |
| Algorithm Service | `ngd-ngg-algorithm.welkin-system.svc:8080` | Platform Resource Controller（PRC）通过 Service DNS 调用 Algorithm，已验证 |
| Prometheus Service | `240.5.112.101:8481` | Algorithm 查询 VictoriaMetrics/Prometheus API |
| 演示 NGD | `go-test-demand` | 触发节点组计算 |
| 预期 NGG | `ngg-go-test-demand` | Platform Resource Controller（PRC）生成的授权结果 |

镜像目前只导入 master3，因此 Platform Resource Controller（PRC）和 Algorithm 使用 `nodeSelector` 固定在 master3。Platform Resource Controller（PRC）通过 Service DNS 调用 Algorithm 已验证；Algorithm 查询 Prometheus 暂时使用固定 ClusterIP。

### master3 演示文件

master3 已准备以下文件：

```text
/root/ghw/
├── ngd-go-test-demand.yaml
└── deploy/
    ├── algorithm.yaml
    ├── lldp-agent.yaml
    └── platform-resource-controller.yaml
```

进入 master3 后可直接部署或更新三个组件：

```bash
cd /root/ghw/deploy
kubectl apply -f algorithm.yaml
kubectl apply -f platform-resource-controller.yaml
kubectl apply -f lldp-agent.yaml
```

`Platform Resource Controller` 的 Kubernetes Deployment 和容器名称均为 `platform-resource-controller`；ServiceAccount、RBAC 和 Leader Lease 保留现有内部名称。


## 1. 进入 master3

在 Windows PowerShell 中执行：

```powershell
kubectl --kubeconfig D:/project/ngd-ngg/config-bupt.yaml exec -it -n bupt bupt-ops-mannual -- ssh 10.129.195.253
```

进入后，以下命令均在 master3 执行。

## 2. 演示前检查

确认两个 CRD 已安装：

```bash
kubectl get crd nodegroupdemands.scheduling.platform.example.io nodegroupgrants.scheduling.platform.example.io
```

确认 LLDP DaemonSet 已在物理节点运行：

```bash
kubectl get daemonset lldp-agent -n welkin-system
kubectl get pods -n welkin-system -l app=ngd-ngg-lldp-agent -o wide
```

确认 Platform Resource Controller（PRC）和 Algorithm 就绪并位于 master3：

```bash
kubectl get deployment platform-resource-controller ngd-ngg-algorithm -n welkin-system
kubectl get pods -n welkin-system -l 'app in (ngd-ngg-platform-resource-controller,ngd-ngg-algorithm)' -o wide
```

确认 LLDP 写入的拓扑摘要标签：

```bash
kubectl get nodes -L topology.demo.ngg.io/leaf-count,topology.demo.ngg.io/leaf-set-id
```

查看某个节点发现的 Leaf、bond、远端端口和观测时间。详细邻居数据位于 Node annotations：

```bash
NODE=hl-tstmix7-paasmaster1

kubectl get node "$NODE" -o jsonpath='{.metadata.annotations.topology\.demo\.ngg\.io/leaf-switch-ids}{"\n"}'
kubectl get node "$NODE" -o jsonpath='{.metadata.annotations.topology\.demo\.ngg\.io/local-interface}{"\n"}'
kubectl get node "$NODE" -o jsonpath='{.metadata.annotations.topology\.demo\.ngg\.io/remote-port}{"\n"}'
kubectl get node "$NODE" -o jsonpath='{.metadata.annotations.topology\.demo\.ngg\.io/observed-at}{"\n"}'
kubectl get node "$NODE" -o jsonpath='{.metadata.annotations.topology\.demo\.ngg\.io/leaf-links}' | python3 -m json.tool
```

查看 LLDP Agent Pod 和最近日志：

```bash
kubectl get pods -n welkin-system -l app=ngd-ngg-lldp-agent -o wide
kubectl logs -n welkin-system -l app=ngd-ngg-lldp-agent --tail=100 --prefix
```

只实时跟踪一个节点时，先从上一条命令找到该节点对应的 Pod，再执行：

```bash
kubectl logs -n welkin-system <lldp-pod-name> -f --tail=100
```

确认 Algorithm 和 Prometheus Service/Endpoint：

```bash
kubectl get svc,endpoints -n welkin-system ngd-ngg-algorithm -o wide
kubectl get svc,endpoints -n prometheus-system vmselect -o wide
```

确认 Algorithm 正常刷新 14 项指标：

```bash
kubectl logs -n welkin-system deployment/ngd-ngg-algorithm --tail=30
```

正常日志包含：

```text
prometheus_refresh_completed status=success metricCount=14
```

## 3. 准备一轮干净演示

如果集群中保留着上次演示对象，先删除 NGD。NGG 设置了 NGD ownerReference，会随 NGD 自动删除：

```bash
kubectl delete ngd go-test-demand --ignore-not-found=true
kubectl get ngd,ngg
```

## 4. 提交 NGD

演示用 NGD 已放在 master3 的 `/root/ghw/ngd-go-test-demand.yaml`。进入 master3 后直接执行：

```bash
cd /root/ghw
kubectl apply -f ngd-go-test-demand.yaml
```

如果本地 YAML 有修改，使用下面的命令重新同步到 `/root/ghw`：

```powershell
python -c "import base64,sys; sys.stdout.write(base64.b64encode(open(r'D:\project\ngd-ngg-scheduling-demo-git\deploy-incluster\demo\ngd-go-test-demand.yaml','rb').read()).decode('ascii'))" | kubectl --kubeconfig D:/project/ngd-ngg/config-bupt.yaml exec -i -n bupt bupt-ops-mannual -- ssh -o BatchMode=yes 10.129.195.253 "base64 --decode --ignore-garbage | tee /root/ghw/ngd-go-test-demand.yaml >/dev/null; chmod 0644 /root/ghw/ngd-go-test-demand.yaml"
```

预期输出：

```text
nodegroupdemand.scheduling.platform.example.io/go-test-demand created
```

NGD 的主要含义：

| 字段 | 演示值 | 含义 |
| --- | --- | --- |
| `schedulerName` | `volcano` | NGG 归属的调度器 |
| `maxNodes` | `3` | 最多返回 3 个节点 |
| `nodeSelector` | `kubernetes.io/arch=amd64` | 只选择 amd64 节点 |
| `minResources` | `3 CPU / 3Gi` | 候选组最低资源保障 |
| `quota` | `300 CPU / 1200Gi` | 资源配额上限 |
| `data-center` | `HB-HL-DC1` | 指定数据中心 |
| `room` | `HB-HL-DC1-102` | 指定机房 |
| `border-switch` | `requiredSame` | 候选节点位于同一 Border 域 |
| `leaf-switch` | `requiredSame` | 候选节点位于同一 Leaf 域 |
| `spine-switch` | `spine-01` | 当前无 Spine，Algorithm 按规则回退到同 Border 域 |

## 5. 查看 NGD 处理状态

重新进入 master3，然后观察状态：

```bash
kubectl get ngd go-test-demand -w
```

看到 `Fulfilled` 后按 `Ctrl+C`。查看完整状态：

```bash
kubectl get ngd go-test-demand -o yaml
```

关键字段：

```yaml
status:
  phase: Fulfilled
  grantRef: ngg-go-test-demand
  resolvedNodeCount: 1
```

## 6. 查看生成的 NGG

```bash
kubectl get ngg
kubectl get ngg ngg-go-test-demand -o yaml
```

当前环境最近一次结果选择了 `hl-tstmix7-paasmaster1`，分数为 `95`，数据来源为 `normal`。分数、可用资源和时间戳会随 Prometheus 实时数据变化。

快速查看节点和分数：

```bash
kubectl get ngg ngg-go-test-demand -o jsonpath='{range .spec.nodes[*]}{.name}{"\t"}{.score}{"\t"}{.resources.cpuAvailable}{"\t"}{.resources.memoryAvailable}{"\n"}{end}'
```

## 7. 查看处理日志

Platform Resource Controller（PRC）日志用于展示需求监听、调用 Algorithm、创建 NGG 和刷新结果：

```bash
# 最近 100 行
kubectl logs -n welkin-system deployment/platform-resource-controller --tail=100

# 实时跟踪
kubectl logs -n welkin-system deployment/platform-resource-controller -f --tail=100

# 只查看本次计算的关键事件
kubectl logs -n welkin-system deployment/platform-resource-controller --since=10m | \
  grep -E 'calling Algorithm Server|received Algorithm Server result|published formal platform NGG'
```

关注以下事件：

```text
observed NGD event
calling Algorithm Server
received Algorithm Server result
published formal platform NGG
```

Algorithm 日志用于展示拓扑解析、指标快照和候选组评分：

```bash
# 最近 100 行
kubectl logs -n welkin-system deployment/ngd-ngg-algorithm --tail=100

# 实时跟踪
kubectl logs -n welkin-system deployment/ngd-ngg-algorithm -f --tail=100

# 只查看本次计算的关键事件
kubectl logs -n welkin-system deployment/ngd-ngg-algorithm --since=10m | \
  grep -E 'topology_resolution_completed|metric_snapshot_resolved|python_calculation_completed|allocation_completed'
```

关注以下事件：

```text
topology_constraints_resolved
metric_snapshot_resolved
python_calculation_completed
allocation_completed
```

## 8. 演示结束后清理

```bash
kubectl delete ngd go-test-demand
kubectl get ngd,ngg
```

## 常见问题

### NGD 一直是 Pending

检查 Platform Resource Controller（PRC）是否为 Leader，并查看日志：

```bash
kubectl get lease -n welkin-system ngd-ngg-prc.scheduling.platform.example.io
kubectl logs -n welkin-system deployment/platform-resource-controller --tail=100
```

### NGD 变为 Failed

查看 `.status.message`，通常是节点标签、拓扑约束或最低资源不满足：

```bash
kubectl get ngd go-test-demand -o jsonpath='{.status.phase}{"\n"}{.status.message}{"\n"}'
```

### Pod 出现 ImagePullBackOff

当前 Harbor 需要认证，且临时镜像只在 master3 的 containerd 中。确认 Pod 是否仍调度到 master3：

```bash
kubectl get pods -n welkin-system -l 'app in (ngd-ngg-platform-resource-controller,ngd-ngg-algorithm)' -o wide
```

### Algorithm 指标刷新失败或 Platform Resource Controller（PRC）无法访问 Algorithm

master3 的 `clusterDomain` 已修复，Platform Resource Controller（PRC）通过 Service DNS 调用 Algorithm 已验证成功。Algorithm 查询 Prometheus 仍使用固定 ClusterIP；其他 DNS 风险见 `dns-diagnosis.md`。
