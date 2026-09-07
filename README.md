# NGD/NGG 两层资源调度

本仓库实现联通正式接口下的任务级节点资源池划分：

```text
Node/Bond/LLDP ──> Node Leaf元数据 ──> PRC静态快照 ──┐
Prometheus ──> Algorithm指标缓存 ──────────────────┤
正式NGD ──> PRC动态状态 ──> Algorithm/Python算法 ──> 正式NGG
Leaf以上拓扑配置 ───────────────────────────────────┘
```

当前代码范围到 PRC 生成正式 `NodeGroupGrant` 为止，不包含下游 Pod 调度实现。

## 当前组件

- `topology_agent/`：Go LLDP Agent。识别主机 Bond，采集 Node 直连 Leaf，并通过 In-Cluster 或 Kubeconfig 身份更新 Node Label/Annotation。
- `prc/`：Go PRC。独立同步 Node 静态快照，Watch 正式 Cluster-scoped NGD，构造 Node 动态状态，调用 Algorithm，并创建或更新正式 NGG。
- `algorithm_server/go/`：Go Algorithm 主进程。提供 HTTP API，维护静态拓扑、Node 静态快照和 Prometheus 指标缓存，并管理一个长期 Python Worker。
- `algorithm_server/python/algorithm_worker/`：Python 算法实现，固定执行需求过滤、拓扑分组和负载评分。
- `go_test_suites/`：统一 Go Test，包括组件调用、PRC 全链路、周期刷新、3000 Node 性能矩阵和双 Leaf/Bond 测试。
- `docs/paas-schedbridge-master-new/crd-deploy/`：需求方新版正式 NGD/NGG CRD，部署和envtest共同使用。
- `config/manager/`、`config/rbac/`：三个组件的通用 Kubernetes 部署和最小权限。
- `config/topology/`：Algorithm 使用的 Leaf 以上静态拓扑配置。
- `docs/`：设计、接口、测试和阶段说明。

完整实现说明见 [NGD-NGG两层调度项目整体说明v1.0.md](docs/阶段汇报-0907/NGD-NGG两层调度项目整体说明v1.0.md)。

## 数据边界

- LLDP Agent只把 Node 到一个或两个 Leaf 的直接观测事实写入 Node。
- Region、Location、DataCenter、Room、Border、Spine 和 Leaf Peer 关系只保存在 Algorithm 拓扑配置中。
- PRC通过独立接口向 Algorithm PUT 内容 Hash 标识的 Node 静态快照。
- 每次 NGD 计算时，PRC重新读取 Node/Pod并发送动态调度状态。
- Algorithm默认每15秒读取一次 Prometheus，指标不经过 PRC。
- Python Worker不维护共享缓存；Go在每次调用时通过 JSONL发送完整计算上下文。
- PRC只取 Algorithm 排名第一的候选组，写成联通正式扁平 NGG。

## 本地测试

先准备仓库自带的 Go 与 envtest 环境：

```bash
cd /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo
source scripts/go-test-env.sh
```

运行全部当前测试：

```bash
make go-test-all
```

分组运行：

```bash
make go-test-topology-agent
make go-test-group1  # Go Algorithm调用真实Python Worker
make go-test-group2  # PRC客户端调用完整Algorithm和Mock Prometheus
make go-test-group3  # envtest中PRC Watch NGD并生成NGG
make go-test-group4  # PRC + 真实Algorithm/Python完整链路
make go-test-group5  # 周期刷新、NGD更新和删除生命周期
make go-test-group7  # Active-Backup与负载Bond完整链路
```

3000 Node性能矩阵：

```bash
make benchmark-3000-all
```

Algorithm 1000 Node独立演示：

```bash
make algorithm-1000-demo
```

测试输出位于各组自己的 `results/<run-id>/`，详细输入、预期输出、实际输出、计时边界和 Debug 方法见 [go_test_suites/README.md](go_test_suites/README.md)。

## 构建与部署

真实集群优先使用新的统一入口：[deploy/README.md](deploy/README.md)。集群内和集群外部署均修改 `deploy/config.local.json`，自动生成镜像、拓扑、Prometheus 和认证配置：

```bash
python3 deploy/deploy.py init
# 按 deploy/README.md 填写配置，并完成首次 bootstrap。
python3 deploy/deploy.py kubernetes up   # 集群内方式
# 或：
python3 deploy/deploy.py external up     # 集群外管理服务器
python3 deploy/deploy.py node up --node-name worker-001  # 各 Worker 上的 LLDP
```

下面为旧脚本入口，仍使用 `config/manager/` 下的 YAML，不读取新入口配置；请勿混用。

构建三个镜像：

```bash
make prc-image
make algorithm-image
make lldp-agent-image
```

部署到当前 kubeconfig 指向的 Kubernetes 集群：

```bash
make deploy
```

也可显式选择上下文：

```bash
KUBE_CONTEXT=<context-name> make deploy
```

部署顺序为：正式 CRD/RBAC → LLDP Agent → Algorithm → PRC。镜像需要预先推送到目标集群可访问的镜像仓库，并同步修改部署 YAML 中的镜像地址。

主要配置：

- `config/manager/lldp-agent.yaml`：真实 LLDP/Bond采集 DaemonSet。
- `config/manager/lldp-agent-kubeconfig-patch.yaml`：需要外部 Kubeconfig 身份时使用的补丁示例。
- `config/manager/algorithm.yaml`：Algorithm、Prometheus与上层拓扑挂载。
- `config/manager/prc.yaml`：PRC、Algorithm地址和刷新周期。
- `docs/paas-schedbridge-master-new/crd-deploy/nodegroupdemand-crd.yaml`：需求方新版正式 NGD。
- `docs/paas-schedbridge-master-new/crd-deploy/nodegroupgrant-crd.yaml`：需求方新版正式 NGG。

## 已验证边界

统一测试使用真实 PRC、真实 Go Algorithm、长期 Python Worker、带认证 Mock Prometheus和 envtest API Server/etcd。测试验证的是资源池计算和 NGG生成，不代表真实网卡、交换机、生产 Prometheus或大规模 Kubernetes控制面的最终性能；这些仍需在目标环境进行联调。
