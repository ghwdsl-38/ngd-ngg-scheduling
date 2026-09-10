# 集群内显式 Kubeconfig 部署手册

本文只包含一种部署方式：PRC、Algorithm Server 和 LLDP Agent
全部部署在 Kubernetes 集群内，PRC 和 LLDP 通过显式
Kubeconfig 访问 Kubernetes API。本文直接使用已发布镜像。

## 最短操作流程

1. 准备并放置三个文件：

   ```text
   config/topology/production-topology.yaml
   deploy/secrets/prc.kubeconfig
   deploy/secrets/lldp.kubeconfig
   ```

2. 初始化并修改部署配置：

   ```bash
   python3 deploy/deploy.py init
   vi deploy/config.local.json
   ```

   至少修改 `context`、`namespace`、三个 `images`、`topologyFile`、
   `prometheus.url`、Worker 选择器，以及 PRC/LLDP 的 Kubeconfig
   Secret 名称。

3. 安装 CRD、Namespace、ServiceAccount 和 RBAC：

   ```bash
   python3 deploy/deploy.py kubernetes bootstrap --config deploy/config.local.json
   ```

4. 将两份 Kubeconfig 创建为 Secret：

   ```bash
   kubectl --context production -n ngd-ngg-system create secret generic prc-kubeconfig \
     --from-file=kubeconfig=deploy/secrets/prc.kubeconfig \
     --dry-run=client -o yaml | kubectl --context production apply -f -

   kubectl --context production -n ngd-ngg-system create secret generic lldp-kubeconfig \
     --from-file=kubeconfig=deploy/secrets/lldp.kubeconfig \
     --dry-run=client -o yaml | kubectl --context production apply -f -
   ```

5. 检查并部署：

   ```bash
   python3 deploy/deploy.py kubernetes check --config deploy/config.local.json
   python3 deploy/deploy.py kubernetes up --config deploy/config.local.json
   ```

6. 查看状态和结果：

   ```bash
   python3 deploy/deploy.py kubernetes status --config deploy/config.local.json
   python3 deploy/deploy.py kubernetes logs --config deploy/config.local.json
   kubectl --context production get nodes \
     -L topology.demo.ngg.io/leaf-switch,topology.demo.ngg.io/leaf-set-id
   ```

以上命令中的 `production` 和 `ngd-ngg-system` 必须与
`deploy/config.local.json` 中的 `context`、`namespace` 一致。详细配置见后文。

```text
Kubernetes 集群
├─ PRC Deployment
│    └─ prc-kubeconfig Secret → Kubernetes API
├─ Algorithm Deployment
│    ├─ 上层拓扑 ConfigMap
│    └─ Prometheus Service
└─ LLDP DaemonSet
     ├─ hostNetwork + NET_RAW + 只读 /sys
     └─ lldp-kubeconfig Secret → Patch Node
```

## 1. 准备信息

部署机需要 Python 3.9+、`kubectl` 和集群管理员 Kubeconfig。
部署前确认：

- 部署用的 kubectl Context；
- 目标 Namespace；
- PRC、Algorithm、LLDP 镜像地址；
- Prometheus 在集群内的地址；
- Leaf 以上拓扑 YAML；
- LLDP DaemonSet 需要运行的 Worker Label/Taint。

已发布：

```text
ghwdsl/ngd-ngg-scheduling:prc-v0.6.1
ghwdsl/ngd-ngg-scheduling:algorithm-v0.6.1
```

LLDP 镜像需填写实际已发布地址。

## 2. 初始化部署配置

在项目根目录执行：

```bash
cd /path/to/ngd-ngg-scheduling-demo
python3 deploy/deploy.py init
```

生成 `deploy/config.local.json`。文件已存在时跳过 `init`。

在 `config.local.json` 中修改以下字段：

```json
{
  "namespace": "ngd-ngg-system",
  "context": "production",
  "clusterId": "production-cluster",
  "images": {
    "prc": "ghwdsl/ngd-ngg-scheduling:prc-v0.6.1",
    "algorithm": "ghwdsl/ngd-ngg-scheduling:algorithm-v0.6.1",
    "lldp": "REPLACE_WITH_PUBLISHED_LLDP_IMAGE"
  },
  "topologyFile": "../config/topology/production-topology.yaml",
  "prc": {
    "demandRefreshSeconds": 15,
    "maxConcurrentRefreshes": 5
  },
  "prometheus": {
    "url": "http://prometheus.monitoring.svc.cluster.local:9090",
    "refreshSeconds": 15,
    "staleSeconds": 120,
    "timeoutSeconds": 5,
    "nodeLabel": "node",
    "tlsServerName": "",
    "secretName": "",
    "tokenFile": "",
    "caFile": "",
    "metricsConfigFile": ""
  },
  "kubernetes": {
    "imagePullSecrets": [],
    "prcAuth": {
      "mode": "kubeconfig",
      "secretName": "prc-kubeconfig",
      "subject": {
        "kind": "ServiceAccount",
        "name": "prc",
        "namespace": "ngd-ngg-system"
      }
    },
    "lldpAuth": {
      "mode": "kubeconfig",
      "secretName": "lldp-kubeconfig",
      "subject": {
        "kind": "ServiceAccount",
        "name": "lldp-agent",
        "namespace": "ngd-ngg-system"
      }
    },
    "workerNodeSelector": {
      "node-role.kubernetes.io/worker": ""
    },
    "workerTolerations": []
  },
  "lldp": {
    "interfaces": "bond0",
    "timeoutSeconds": 65,
    "count": 0,
    "intervalSeconds": 180
  }
}
```

上面是需要修改的片段，不要删除原文件中的 `resources`
等其他字段。

关键点：

- `context` 是部署机管理员 Kubeconfig 中的 Context；
- `topologyFile` 相对路径以 `deploy/` 目录为基准；
- `interfaces` 默认限定`bond0`；没有该Bond时改为`auto`或实际接口名；
- `workerNodeSelector` 必须能选中目标 Worker；
- 有 Taint 的 Worker 需要填写 `workerTolerations`。

检查：

```bash
kubectl config get-contexts
kubectl --context production get nodes -o wide
python3 -m json.tool deploy/config.local.json >/dev/null
```

## 3. 准备上层拓扑

将真实拓扑保存为：

```text
config/topology/production-topology.yaml
```

格式参考 `config/topology/unicom-huailai-102-sample.yaml`。LLDP 写入
Node 的 Leaf 名称必须与拓扑 YAML 中的 Leaf ID 完全一致。

部署时该文件会被生成为 ConfigMap，并挂载到 Algorithm Pod：

```text
/etc/ngd-ngg/topology/topology.yaml
```

## 4. 安装 CRD、Namespace 和 RBAC

```bash
python3 deploy/deploy.py kubernetes bootstrap \
  --config deploy/config.local.json
```

该命令创建 NGD/NGG CRD、Namespace、`prc`/`lldp-agent`
ServiceAccount 以及对应 RBAC。

## 5. 放置 Kubeconfig 文件

由需求方或集群管理员提供两份 Kubeconfig，放在项目以下位置：

```text
deploy/secrets/prc.kubeconfig
deploy/secrets/lldp.kubeconfig
```

- `prc.kubeconfig`：供 PRC 访问 Node、Pod、NGD 和 NGG；
- `lldp.kubeconfig`：供 LLDP Agent 读取并更新 Node 标签。

Kubeconfig 中的 API Server 地址必须能从 Pod 内访问。不要使用只对部署机
有效的 `127.0.0.1` 地址，也不要将管理员 Kubeconfig 放入项目。

## 6. 创建 Kubeconfig Secret

```bash
kubectl --context production -n ngd-ngg-system create secret generic prc-kubeconfig \
  --from-file=kubeconfig=deploy/secrets/prc.kubeconfig \
  --dry-run=client -o yaml \
  | kubectl --context production apply -f -

kubectl --context production -n ngd-ngg-system create secret generic lldp-kubeconfig \
  --from-file=kubeconfig=deploy/secrets/lldp.kubeconfig \
  --dry-run=client -o yaml \
  | kubectl --context production apply -f -
```

## 7. Prometheus 认证（按需）

无认证时保持：

```json
"secretName": "",
"tokenFile": "",
"caFile": ""
```

需要 Bearer Token 时，创建 Secret：

```bash
kubectl --context production -n ngd-ngg-system create secret generic prometheus-auth \
  --from-file=token=/secure/path/prometheus.token \
  --dry-run=client -o yaml \
  | kubectl --context production apply -f -
```

然后修改：

```json
"secretName": "prometheus-auth",
"tokenFile": "enabled",
"caFile": ""
```

需要自定义 CA 时，Secret Key 必须为 `ca.crt`，并将 `caFile`
设为非空值。

## 8. 镜像拉取认证（按需）

公开镜像保持 `"imagePullSecrets": []`。私有镜像先在目标
Namespace 创建 `docker-registry` Secret，再填写：

```json
"imagePullSecrets": ["registry-credential"]
```

## 9. 部署

生成清单：

```bash
python3 deploy/deploy.py kubernetes render \
  --config deploy/config.local.json
```

生成位置：

```text
deploy/generated/config.local/kubernetes/bootstrap.json
deploy/generated/config.local/kubernetes/kubernetes.json
```

检查本地配置（不创建资源）：

```bash
python3 deploy/deploy.py kubernetes check \
  --config deploy/config.local.json
```

正式部署：

```bash
python3 deploy/deploy.py kubernetes up \
  --config deploy/config.local.json
```

`up` 会先执行 Server-Side Dry Run，再部署 Algorithm、PRC、
LLDP 和拓扑 ConfigMap，最后等待 Rollout。Kubeconfig 模式下
PRC 为单副本且关闭 Leader Election，不要同时运行另一个 PRC。

## 10. 验证

查看工作负载：

```bash
python3 deploy/deploy.py kubernetes status --config deploy/config.local.json
kubectl --context production -n ngd-ngg-system get deploy,ds,pod,svc -o wide
```

查看日志：

```bash
kubectl --context production -n ngd-ngg-system logs deployment/prc -f
kubectl --context production -n ngd-ngg-system logs deployment/ngd-ngg-algorithm -f
kubectl --context production -n ngd-ngg-system logs daemonset/lldp-agent --tail=200
```

检查 LLDP 结果：

```bash
kubectl --context production get nodes \
  -L topology.demo.ngg.io/leaf-switch,topology.demo.ngg.io/leaf-set-id,topology.demo.ngg.io/leaf-count
```

检查 Algorithm 缓存：

```bash
kubectl --context production -n ngd-ngg-system \
  port-forward service/ngd-ngg-algorithm 8080:8080
```

另一终端：

```bash
curl -fsS http://127.0.0.1:8080/internal/v1/cache/status
```

应确认 `networkTopology.ready=true`、`nodeStatic.ready=true`、
`metrics.ready=true`。`/readyz` 只代表进程就绪，不代表缓存已齐全。

提交 NGD 并查看 NGG：

```bash
kubectl --context production apply -f /path/to/production-ngd.yaml
kubectl --context production get \
  nodegroupdemands.scheduling.platform.example.io -o yaml
kubectl --context production get \
  nodegroupgrants.scheduling.platform.example.io -o yaml
```

预期：`NGG status.phase=Active`，`NGD status.phase=Fulfilled`。

## 11. 更新和停止

拓扑、镜像 Tag、Prometheus 或参数变更后，重新执行：

```bash
python3 deploy/deploy.py kubernetes up --config deploy/config.local.json
```

Kubeconfig Secret 更新后需重启：

```bash
kubectl --context production -n ngd-ngg-system rollout restart deployment/prc
kubectl --context production -n ngd-ngg-system rollout restart daemonset/lldp-agent
```

停止工作负载：

```bash
python3 deploy/deploy.py kubernetes down --config deploy/config.local.json
```

`down` 不删除 CRD、Namespace、RBAC、Secret、NGD、NGG 和 Node 拓扑标记。

## 12. 快速排错

| 问题 | 检查内容 |
|---|---|
| `ImagePullBackOff` | 镜像 Tag、仓库可达性、`imagePullSecrets` |
| Kubeconfig Secret 缺失 | `secretName` 与 Secret 名称 |
| API `connection refused` | Kubeconfig 中 API Server 地址是否可从 Pod 访问 |
| API `forbidden` | Kubeconfig 真实身份、`subject`、RBAC |
| LLDP Pod 数量不对 | Worker Label、Taint/Toleration |
| LLDP 超时 | Bond/网卡、交换机 LLDP、Host Network、`NET_RAW` |
| Algorithm 拓扑解析失败 | Node Leaf ID 是否存在于上层拓扑 YAML |
| Prometheus 不 Ready | Service DNS、Token、CA、`nodeLabel` |
| NGD 没有生成 NGG | 按 `requestId`、`ngdUID`、`generation` 关联两端日志 |
