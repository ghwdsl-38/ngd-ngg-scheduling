# NGD-NGG 手工部署说明

本目录用于在 Kubernetes 集群内手工部署 PRC、Algorithm Server 和
LLDP Agent。它不调用 `deploy/deploy.py`，也不负责构建或推送镜像。

PRC 和 LLDP Agent 均采用显式 Kubeconfig；Algorithm Server 不访问
Kubernetes API，因此不需要 Kubeconfig。

## 1. 目录结构

```text
deploy-manual/
├── README.md
├── kustomization.yaml
├── 00-namespace.yaml
├── 10-serviceaccounts.yaml
├── 20-algorithm.yaml
├── 30-prc.yaml
├── 40-lldp-agent.yaml
├── config/
│   └── network-topology.yaml
└── secrets/
    ├── prc.kubeconfig       部署前放入，不提交Git
    └── lldp.kubeconfig      部署前放入，不提交Git
```

`kubectl apply -k` 会根据 `kustomization.yaml`：

- 读取 `config/network-topology.yaml`，生成拓扑 ConfigMap；
- 读取两份 Kubeconfig，生成两个 Secret；
- 应用 Namespace、ServiceAccount、Service、Deployment 和 DaemonSet。

本目录不创建 ClusterRole 或 ClusterRoleBinding。两份 Kubeconfig 对应的
身份必须由需求方提前授权。

## 2. 最短操作步骤

在项目根目录执行。

### 第一步：放置 Kubeconfig

将需求方提供的文件分别放到：

```text
deploy-manual/secrets/prc.kubeconfig
deploy-manual/secrets/lldp.kubeconfig
```

### 第二步：修改配置

至少检查以下内容：

1. 在 `kustomization.yaml` 和 `00-namespace.yaml` 中确认 Namespace；
2. 确认两份 Kubeconfig 对应的身份已经由需求方授予所需权限；
3. 在 `config/network-topology.yaml` 中换成真实 Leaf 以上拓扑；
4. 在 `20-algorithm.yaml` 中修改 Algorithm 镜像和 Prometheus 地址；
5. 在 `30-prc.yaml` 中修改 PRC 镜像和 `CLUSTER_ID`；
6. 在 `40-lldp-agent.yaml` 中修改 LLDP 镜像、Worker 选择器和容忍规则。

### 第三步：安装 NGD、NGG CRD

```bash
kubectl --context production apply -f \
  docs/paas-schedbridge-master-new/crd-deploy/nodegroupdemand-crd.yaml

kubectl --context production apply -f \
  docs/paas-schedbridge-master-new/crd-deploy/nodegroupgrant-crd.yaml
```

### 第四步：检查最终清单

该命令只显示生成结果，不创建资源：

```bash
kubectl kustomize deploy-manual
```

重点确认清单中没有 `REPLACE_WITH_` 占位符。

### 第五步：部署

先创建 Namespace，再应用全部资源：

```bash
kubectl --context production apply -f deploy-manual/00-namespace.yaml
kubectl --context production apply -k deploy-manual
```

### 第六步：等待启动并查看日志

```bash
kubectl --context production -n ngd-ngg-system rollout status \
  deployment/ngd-ngg-algorithm --timeout=300s
kubectl --context production -n ngd-ngg-system rollout status \
  deployment/prc --timeout=300s
kubectl --context production -n ngd-ngg-system rollout status \
  daemonset/lldp-agent --timeout=300s

kubectl --context production -n ngd-ngg-system get deploy,ds,pod,svc -o wide
kubectl --context production -n ngd-ngg-system logs deployment/ngd-ngg-algorithm --tail=100
kubectl --context production -n ngd-ngg-system logs deployment/prc --tail=100
kubectl --context production -n ngd-ngg-system logs daemonset/lldp-agent --tail=100
```

命令中的 `production` 和 `ngd-ngg-system` 要替换为真实 Context 和
Namespace。

## 3. 各文件怎么修改

### 3.1 Namespace

默认 Namespace 是 `ngd-ngg-system`。需要更换时同时修改：

- `kustomization.yaml` 中的 `namespace`；
- `00-namespace.yaml` 中的 `metadata.name`。

修改后先运行 `kubectl kustomize deploy-manual`，检查所有命名空间引用。

### 3.2 Kubeconfig 权限前提

本目录不创建 RBAC，直接使用需求方提供的 Kubeconfig。部署前由需求方
确认其身份已经具备以下权限：

- PRC：读取 Node、Pod 和 NGD，更新 NGD Status，读取、创建、更新和删除
  NGG，并更新 NGG Status；
- LLDP：读取 Node，并 Patch Node 标签。

`10-serviceaccounts.yaml` 创建的两个 ServiceAccount 只供 Pod 引用，没有
绑定任何权限。PRC 和 LLDP Pod 均设置：

```yaml
automountServiceAccountToken: false
```

因此程序访问 Kubernetes API 时只使用挂载的 Kubeconfig 身份，不使用
Pod ServiceAccount Token。如果 Kubeconfig 权限不足，日志会出现
`forbidden`，需要由需求方调整该 Kubeconfig 身份的授权。

### 3.3 上层拓扑

直接用真实文件替换：

```text
deploy-manual/config/network-topology.yaml
```

LLDP 写入 Node 的 Leaf 名称必须能在该文件的 `topology` 中找到。
`SPINE: {}` 是合法配置，算法会使用 Border Domain。

### 3.4 Algorithm Server

修改 `20-algorithm.yaml`：

```yaml
image: ghwdsl/ngd-ngg-scheduling:algorithm-v0.6.1
```

同时修改 Prometheus：

```yaml
- name: PROMETHEUS_URL
  value: http://prometheus.monitoring.svc.cluster.local:9090
- name: PROMETHEUS_NODE_LABEL
  value: node
```

该地址必须能从 Algorithm Pod 内访问。当前清单默认 Prometheus 不需要
Bearer Token；需要认证时，应额外创建 Secret，并为 Algorithm 增加
认证文件挂载和 `PROMETHEUS_BEARER_TOKEN_FILE` 环境变量。

### 3.5 PRC

修改 `30-prc.yaml`：

```yaml
image: ghwdsl/ngd-ngg-scheduling:prc-v0.6.1
```

集群标识按实际修改：

```yaml
- name: CLUSTER_ID
  value: production-cluster
```

PRC 在显式 Kubeconfig 模式下使用单副本并关闭 Leader Election：

```yaml
- --leader-elect=false
```

并发刷新 Worker 数量通过下面的参数设置：

```yaml
- --max-concurrent-refreshes=5
```

### 3.6 LLDP Agent

修改 `40-lldp-agent.yaml` 中的镜像占位符：

```yaml
image: REPLACE_WITH_LLDP_IMAGE
```

根据真实 Worker Label 修改：

```yaml
nodeSelector:
  node-role.kubernetes.io/worker: ""
```

有 Taint 的 Worker 还要增加对应 `tolerations`。清单已经包含 LLDP 所需的：

- `hostNetwork: true`；
- `NET_RAW`；
- 宿主机 `/sys` 只读挂载；
- `NODE_NAME`；
- 显式 Kubeconfig Secret。

清单默认使用`--interfaces=bond0`：Agent将`bond0`展开成物理Slave，
`active-backup`只监听Active Slave，其他Bond模式监听全部链路有效的Slave。
如果目标机器没有`bond0`，将其改成`--interfaces=auto`，按物理链路和Bond状态
自动选择。每轮在完整的65秒窗口中采集，默认每180秒启动一轮。

### 3.7 私有镜像仓库

如果镜像仓库需要认证，先在目标 Namespace 创建
`imagePullSecret`，然后在三个工作负载的 `spec.template.spec` 下加入：

```yaml
imagePullSecrets:
  - name: registry-credential
```

当前 PRC 和 Algorithm 使用公开 Docker Hub 镜像时不需要此配置。

## 4. 验证运行结果

查看 LLDP 写入的 Node 标签：

```bash
kubectl --context production get nodes \
  -L topology.demo.ngg.io/leaf-switch,topology.demo.ngg.io/leaf-set-id,topology.demo.ngg.io/leaf-count
```

查看 Algorithm 缓存：

```bash
kubectl --context production -n ngd-ngg-system port-forward \
  service/ngd-ngg-algorithm 8080:8080
```

在另一个终端执行：

```bash
curl -fsS http://127.0.0.1:8080/internal/v1/cache/status
```

提交 NGD 并检查 NGG：

```bash
kubectl --context production apply -f /path/to/production-ngd.yaml
kubectl --context production get ngd -o yaml
kubectl --context production get ngg -o yaml
```

## 5. 更新和停止

修改 YAML、拓扑或 Kubeconfig 后重新执行：

```bash
kubectl --context production apply -k deploy-manual
```

由于 Secret 和 ConfigMap 使用固定名称，内容更新后应重启对应工作负载：

```bash
kubectl --context production -n ngd-ngg-system rollout restart deployment/prc
kubectl --context production -n ngd-ngg-system rollout restart deployment/ngd-ngg-algorithm
kubectl --context production -n ngd-ngg-system rollout restart daemonset/lldp-agent
```

停止本目录部署的工作负载：

```bash
kubectl --context production delete -k deploy-manual
```

该命令会删除本目录生成的 Namespace，因此也会删除该 Namespace 中的
Secret 和工作负载。NGD、NGG、CRD 和已有 Node 标签不会随 Namespace 删除。
