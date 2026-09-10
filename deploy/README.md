# NGD-NGG 统一部署操作手册

本文只解决三个问题：

1. 镜像是否需要自己构建；
2. 每种部署方式具体执行什么；
3. 每一步需要修改哪个配置。

所有命令默认在 Git 仓库根目录执行：

```bash
cd ngd-ngg-scheduling-demo
```

> 当前PRC与Algorithm的`v0.6.0`发布已切换为“Go双架构预编译后再打包”流程：
> `make release-images`负责本地打包，`make release-verify`负责envtest镜像
> 验证，登录Docker Hub后才执行`make release-push`。详细说明见
> [发布镜像验证说明](../image_validation/README.md)。原来的
> `deploy.py images build`仍包含尚未迁移的LLDP旧构建流程，本阶段不要用它
> 发布PRC与Algorithm。

## 1. 先选择操作路线

### 1.1 镜像是否需要自己 Build

不是每次部署都要自己构建镜像，二选一即可：

| 情况 | 是否执行 `images build/push` | 要做什么 |
|---|---:|---|
| 需求方已经提供 PRC、Algorithm、LLDP 三个镜像地址 | 否 | 将地址写入 `deploy/config.local.json` 后直接部署 |
| 没有可用镜像，或者本次修改了代码 | 是 | 先配置自己的镜像仓库地址，再 Build、Push、部署 |

完整顺序如下：

```text
已有镜像：初始化配置 → 填写镜像地址 → 配置部署模式 → bootstrap → up → 验证

自己构建：初始化配置 → 填写自己的镜像地址 → build → push
          → 配置部署模式 → bootstrap → up → 验证
```

### 1.2 选择部署模式

| 模式 | PRC/Algorithm | LLDP Agent | 使用场景 |
|---|---|---|---|
| A. 集群内 In-Cluster | Kubernetes Deployment | Kubernetes DaemonSet | 默认推荐，操作最少 |
| B. 集群内 Kubeconfig | Kubernetes Deployment | Kubernetes DaemonSet | 需求方明确要求显式 Kubeconfig |
| C. 集群外 | 管理服务器 Docker Compose | 每个 Worker 上的 Docker Compose | 管理组件不允许放进集群 |

只执行自己选择的一条路线，不要把三种部署命令混在一起。

### 1.3 每个组件需要的环境写在哪里

项目里的“环境”分成三层，不能都写在 `config.local.json`：

| 环境类型 | 写在哪里 | 负责什么 |
|---|---|---|
| 镜像构建环境 | 根目录三个 `Dockerfile.*` 和 `scripts/04*-build*.sh` | Go/Python版本、基础镜像、编译和打包过程 |
| 代码依赖版本 | `prc/go.mod`、`algorithm_server/go/go.mod`、`topology_agent/go.mod` | controller-runtime、client-go、Cobra等依赖版本 |
| 部署运行环境 | `deploy/config.local.json` | 镜像地址、资源限制、认证模式、拓扑、Prometheus、节点选择器 |
| Kubernetes安全与挂载 | `deploy/render.py` 自动生成 | ServiceAccount、Kubeconfig、RBAC、Host Network、`NET_RAW`、只读 `/sys` |

三个组件的镜像环境如下：

| 组件 | 构建环境 | 最终运行环境 | 定义文件 |
|---|---|---|---|
| PRC | `golang:1.25.0` 容器编译 | `scratch`，只包含 `/prc` 二进制，以 UID/GID 65532 运行 | `scripts/04-build-prc.sh`、`Dockerfile.prc` |
| Algorithm | `golang:1.25-alpine` 编译 Go 主服务 | `python:3.13-alpine`，包含 Go Server 和 Python Worker，以 UID/GID 65532 运行 | `Dockerfile.algorithm` |
| LLDP Agent | `golang:1.25-alpine` 编译 | `alpine:3.22`，以容器镜像用户启动，部署时授予所需 `NET_RAW` | `Dockerfile.lldp-agent` |

Python Worker 当前只使用 Python 标准库，没有额外的 `pip install` 或
`requirements.txt`。Algorithm 镜像会把 Python Worker 一起复制进去，
不需要单独部署 Python 容器。

Docker 构建仍然需要考虑，但分两种情况：

- **使用需求方已发布镜像**：你不负责构建，部署机不需要安装 Go，也不
  需要 Docker；集群内模式只需要 Python 3 和 `kubectl`；
- **本次自己发布镜像**：构建机需要 Docker、Bash、Python 3，以及访问
  基础镜像和 Go 依赖源的网络；不需要在宿主机安装 Go 1.25 或 Python
  3.13，具体版本由构建容器提供。

不同机器需要的软件：

| 机器 | 必需软件 |
|---|---|
| 镜像构建机 | Docker Engine、Bash、Python 3 |
| 集群内部署机 | Python 3、kubectl；使用现成镜像时不需要 Docker |
| 集群外管理服务器 | Python 3、Docker Engine、Docker Compose v2；执行 bootstrap 还需要 kubectl |
| 集群外每个 Worker | Python 3、Docker Engine、Docker Compose v2 |

`python3 deploy/deploy.py images build` 已经封装了三个组件的构建过程，
一般不需要手工执行 `go build` 或 `docker build`。但是基础镜像能否下载、
构建机和目标节点 CPU 架构是否一致、镜像仓库是否可访问，仍需要部署方
提前确认。

## 2. 所有路线共同的准备工作

### 2.1 初始化实际配置文件

项目提供的是不含真实环境信息的模板：

```text
deploy/config.example.json   示例模板，不要写入真实凭证
deploy/config.local.json     实际配置，Git 已忽略
```

首次执行：

```bash
python3 deploy/deploy.py init
```

该命令创建 `deploy/config.local.json`，已存在时不会覆盖。之后编辑：

```bash
vi deploy/config.local.json
```

修改完成先验证 JSON 语法：

```bash
python3 -m json.tool deploy/config.local.json >/dev/null
```

所有后续命令都显式使用这个配置文件：

```text
--config deploy/config.local.json
```

### 2.2 三种模式都要检查的配置

打开 `deploy/config.local.json`，至少检查以下字段：

```json
{
  "namespace": "ngd-ngg-system",
  "context": "production",
  "clusterId": "production-cluster",
  "images": {
    "prc": "registry.unicom.example.com/ngd-ngg/prc:v0.5.0",
    "algorithm": "registry.unicom.example.com/ngd-ngg/algorithm:v0.5.0",
    "lldp": "registry.unicom.example.com/ngd-ngg/lldp-agent:v0.5.0"
  },
  "topologyFile": "../config/topology/unicom-huailai-102-sample.yaml",
  "prc": {
    "demandRefreshSeconds": 15,
    "maxConcurrentRefreshes": 5
  }
}
```

| 字段 | 修改内容 |
|---|---|
| `namespace` | PRC、Algorithm、LLDP 和 RBAC 使用的命名空间 |
| `context` | 部署人员本机 `kubectl` 连接目标集群的 Context |
| `clusterId` | PRC 发给 Algorithm 的集群标识 |
| `images.prc` | PRC 完整镜像地址和 Tag |
| `images.algorithm` | Algorithm 完整镜像地址和 Tag |
| `images.lldp` | LLDP Agent 完整镜像地址和 Tag |
| `topologyFile` | 上层网络拓扑文件；相对路径以 `config.local.json` 所在目录为基准 |
| `prc.demandRefreshSeconds` | 每个NGD完成一次业务计算后，等待多少秒再周期刷新，必须为正整数 |
| `prc.maxConcurrentRefreshes` | 单个PRC实例可同时处理的不同NGD数量，必须为正整数；默认5 |

不要保留 `REPLACE_*` 或 `registry.example.com` 占位符。

确认管理员 Context 没有选错集群：

```bash
kubectl config get-contexts
kubectl --context production cluster-info
kubectl --context production get nodes -o wide
```

`maxConcurrentRefreshes`控制的是controller-runtime Refresh Controller的
原生Worker数量，不是自行维护的线程池。提高该值会增加并行Algorithm HTTP
请求和Kubernetes API读写；生产环境应结合Algorithm容量、API Server限流和
NGD积压量逐步调整。配置修改后重新执行对应部署模式的`up`，使PRC重建并读取
新启动参数。

### 2.3 配置 Prometheus

Algorithm 在哪里运行，`prometheus.url` 就必须能从哪里访问。

集群内无认证示例：

```json
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
}
```

字段说明：

| 字段 | 修改条件 |
|---|---|
| `url` | 改成 Algorithm 实际可访问的 Prometheus 地址；空字符串表示不采集 |
| `refreshSeconds` | 指标刷新周期，当前建议 15 秒 |
| `nodeLabel` | Prometheus 查询结果中代表 Kubernetes Node 名称的 Label |
| `secretName` | 集群内部署且 Prometheus 需要 Token/CA 时填写 Secret 名称 |
| `tokenFile`、`caFile` | 非空表示启用对应认证文件；集群内表示 Secret Key，集群外表示本地文件 |
| `metricsConfigFile` | 留空使用内置 14 项指标；非空时加载指定指标目录 JSON |

## 3. 可选流程：自己构建和 Push 镜像

如果已经拿到三个可用镜像，跳过本章。

### 3.1 修改镜像地址

先在 Harbor 或其他镜像仓库创建项目，例如 `ngd-ngg`，然后修改：

```json
"images": {
  "prc": "registry.unicom.example.com/ngd-ngg/prc:v0.5.0",
  "algorithm": "registry.unicom.example.com/ngd-ngg/algorithm:v0.5.0",
  "lldp": "registry.unicom.example.com/ngd-ngg/lldp-agent:v0.5.0"
}
```

格式是：

```text
<仓库域名>/<仓库项目>/<镜像名>:<版本>
```

### 3.2 构建

构建机需要 Docker，并能下载基础镜像和 Go 依赖：

```bash
docker info

python3 deploy/deploy.py images build \
  --config deploy/config.local.json
```

构建完成后同时产生：

| 结果 | 位置 |
|---|---|
| 实际用于 Push 的镜像 | 构建机 Docker Engine 本地镜像库 |
| PRC 离线包 | `images/ngd-ngg-prc-v0.3.0.tar` |
| Algorithm 离线包 | `images/ngd-ngg-algorithm-v0.4.0.tar` |
| LLDP 离线包 | `images/ngd-ngg-lldp-agent-v0.2.0.tar` |
| 镜像 ID/Tag 记录 | `results/prc-image.txt`、`algorithm-image.txt`、`lldp-agent-image.txt` |

离线包名称是脚本保留的固定历史名称，包内镜像 Tag 仍以
`config.local.json` 为准。在线 Push 直接读取 Docker 本地镜像，不读取
这些 tar 包。

检查本地镜像：

```bash
docker image inspect registry.unicom.example.com/ngd-ngg/prc:v0.5.0
docker image inspect registry.unicom.example.com/ngd-ngg/algorithm:v0.5.0
docker image inspect registry.unicom.example.com/ngd-ngg/lldp-agent:v0.5.0
ls -lh images/ results/*-image.txt
```

### 3.3 Push

```bash
docker login registry.unicom.example.com

python3 deploy/deploy.py images push \
  --config deploy/config.local.json
```

该命令依次执行三个 `docker push`，目标就是 `images.prc`、
`images.algorithm` 和 `images.lldp` 中的完整地址。Push 完成后在 Harbor
页面确认 Tag，或者从另一台有权限的机器执行：

```bash
docker pull registry.unicom.example.com/ngd-ngg/lldp-agent:v0.5.0
```

## 4. 路线 A：集群内 In-Cluster 部署

这是默认推荐路线。PRC 和 LLDP 使用 Kubernetes 自动注入的
ServiceAccount Token，不需要准备 Kubeconfig 文件。

### A1. 修改配置

修改 `deploy/config.local.json`：

```json
"kubernetes": {
  "imagePullSecrets": [],
  "prcAuth": {"mode": "incluster", "secretName": "", "subject": null},
  "lldpAuth": {"mode": "incluster", "secretName": "", "subject": null},
  "workerNodeSelector": {"node-role.kubernetes.io/worker": ""},
  "workerTolerations": []
}
```

需要确认：

- `workerNodeSelector` 能选中所有需要采集 LLDP 的 Worker；
- 有 Taint 的 Worker 要把对应 Toleration 写入 `workerTolerations`；
- `images.*`、`topologyFile` 和 `prometheus.*` 已按第 2 章修改。

查看 Worker Label 和 Taint：

```bash
kubectl --context production get nodes --show-labels
kubectl --context production describe node <worker-name>
```

### A2. 安装 CRD、命名空间和 RBAC

首次部署或者 CRD/RBAC 发生变化时执行：

```bash
python3 deploy/deploy.py kubernetes bootstrap \
  --config deploy/config.local.json
```

该操作创建/更新：

- NGD、NGG CRD；
- Namespace；
- PRC、LLDP ServiceAccount；
- ClusterRole 和 ClusterRoleBinding。

### A3. 私有镜像仓库认证（按需执行）

如果集群无需认证即可拉取镜像，保持：

```json
"imagePullSecrets": []
```

需要认证时，先创建 Secret：

```bash
kubectl --context production -n ngd-ngg-system \
  create secret docker-registry registry-credential \
  --docker-server=registry.unicom.example.com \
  --docker-username='<用户名>' \
  --docker-password='<密码或Robot Token>'
```

再修改 `deploy/config.local.json`：

```json
"imagePullSecrets": ["registry-credential"]
```

### A4. 生成、检查、部署

```bash
# 只生成清单，不访问集群。
python3 deploy/deploy.py kubernetes render \
  --config deploy/config.local.json

# 检查本地文件，不创建工作负载。
python3 deploy/deploy.py kubernetes check \
  --config deploy/config.local.json

# 服务端 dry-run、应用清单并等待三个组件 Ready。
python3 deploy/deploy.py kubernetes up \
  --config deploy/config.local.json
```

生成的清单位于：

```text
deploy/generated/config.local/kubernetes/bootstrap.json
deploy/generated/config.local/kubernetes/kubernetes.json
```

### A5. 验证

```bash
python3 deploy/deploy.py kubernetes status \
  --config deploy/config.local.json

python3 deploy/deploy.py kubernetes logs \
  --config deploy/config.local.json

kubectl --context production get nodes \
  -L topology.demo.ngg.io/leaf-set-id,topology.demo.ngg.io/leaf-count
```

查看 Algorithm 缓存：

```bash
kubectl --context production -n ngd-ngg-system \
  port-forward service/ngd-ngg-algorithm 8080:8080
```

另一个终端执行：

```bash
curl -fsS http://127.0.0.1:8080/internal/v1/cache/status
```

## 5. 路线 B：集群内显式 Kubeconfig 部署

组件仍然是 Kubernetes Pod，但 PRC 和 LLDP 不使用自动注入的 Token，
而是读取挂载到 Pod 中的 Kubeconfig。

### B1. 修改认证模式

修改 `deploy/config.local.json` 的 `kubernetes`：

```json
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
}
```

`subject` 必须是 API Server 认证后的真实身份。若管理员提供客户端证书
身份，则改为：

```json
{"kind": "User", "name": "实际认证用户名"}
```

这里不是 kubeconfig 中 `users[].name` 的本地别名。

### B2. 先安装 CRD、身份和 RBAC

```bash
python3 deploy/deploy.py kubernetes bootstrap \
  --config deploy/config.local.json
```

### B3. 准备两个 Kubeconfig

生产环境应由集群管理员或凭证平台提供两个独立文件：

```text
deploy/secrets/prc.kubeconfig
deploy/secrets/lldp.kubeconfig
```

项目提供了不含真实凭证的结构样例：

```text
deploy/examples/prc.kubeconfig.example
deploy/examples/lldp.kubeconfig.example
```

可以复制后填写：

```bash
mkdir -p deploy/secrets
cp deploy/examples/prc.kubeconfig.example deploy/secrets/prc.kubeconfig
cp deploy/examples/lldp.kubeconfig.example deploy/secrets/lldp.kubeconfig
chmod 0600 deploy/secrets/*.kubeconfig
```

必须替换样例中的 API Server、CA 和 Token：API Server和Token使用
`REPLACE_WITH_*` 明文占位符；CA字段当前保存的是占位文字的合法Base64，
同样必须替换为真实集群CA的Base64内容。`deploy/secrets/` 已被Git忽略，
填写真实凭证后的文件不得提交。

文件中的 API Server 地址必须能从 Pod 内访问，不能随意写
`127.0.0.1`。Kubeconfig 应内嵌 CA 和专用 Token/客户端证书，不要复制
管理员凭证。

联调环境可以基于前面创建的 ServiceAccount 生成短期凭证：

```bash
mkdir -p deploy/secrets

NGG_API_SERVER="$(kubectl --context production config view \
  --raw --minify --flatten -o jsonpath='{.clusters[0].cluster.server}')"

kubectl --context production config view --raw --minify --flatten \
  -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' \
  | base64 --decode > deploy/secrets/cluster-ca.crt

NGG_PRC_TOKEN="$(kubectl --context production -n ngd-ngg-system \
  create token prc --duration=24h)"
NGG_LLDP_TOKEN="$(kubectl --context production -n ngd-ngg-system \
  create token lldp-agent --duration=24h)"

kubectl --kubeconfig=deploy/secrets/prc.kubeconfig config set-cluster production \
  --server="${NGG_API_SERVER}" \
  --certificate-authority=deploy/secrets/cluster-ca.crt --embed-certs=true
kubectl --kubeconfig=deploy/secrets/prc.kubeconfig config set-credentials runtime \
  --token="${NGG_PRC_TOKEN}"
kubectl --kubeconfig=deploy/secrets/prc.kubeconfig config set-context production \
  --cluster=production --user=runtime
kubectl --kubeconfig=deploy/secrets/prc.kubeconfig config use-context production

kubectl --kubeconfig=deploy/secrets/lldp.kubeconfig config set-cluster production \
  --server="${NGG_API_SERVER}" \
  --certificate-authority=deploy/secrets/cluster-ca.crt --embed-certs=true
kubectl --kubeconfig=deploy/secrets/lldp.kubeconfig config set-credentials runtime \
  --token="${NGG_LLDP_TOKEN}"
kubectl --kubeconfig=deploy/secrets/lldp.kubeconfig config set-context production \
  --cluster=production --user=runtime
kubectl --kubeconfig=deploy/secrets/lldp.kubeconfig config use-context production

unset NGG_API_SERVER NGG_PRC_TOKEN NGG_LLDP_TOKEN
chmod 0600 deploy/secrets/*.kubeconfig
```

短期 Token 只适合联调，生产环境必须安排凭证续期。

### B4. 验证 Kubeconfig 权限

```bash
kubectl --kubeconfig deploy/secrets/prc.kubeconfig get nodes
kubectl --kubeconfig deploy/secrets/prc.kubeconfig auth can-i get nodegroupdemands.scheduling.platform.example.io
kubectl --kubeconfig deploy/secrets/prc.kubeconfig auth can-i create nodegroupgrants.scheduling.platform.example.io

kubectl --kubeconfig deploy/secrets/lldp.kubeconfig get nodes
kubectl --kubeconfig deploy/secrets/lldp.kubeconfig auth can-i get nodes
kubectl --kubeconfig deploy/secrets/lldp.kubeconfig auth can-i patch nodes
kubectl --kubeconfig deploy/secrets/lldp.kubeconfig auth can-i create nodegroupgrants.scheduling.platform.example.io
```

前三类 PRC 权限和 LLDP 的 `get/patch nodes` 应返回 `yes`；LLDP 创建 NGG
应返回 `no`。

### B5. 创建 Kubeconfig Secret

```bash
kubectl --context production -n ngd-ngg-system create secret generic prc-kubeconfig \
  --from-file=kubeconfig=deploy/secrets/prc.kubeconfig \
  --dry-run=client -o yaml | kubectl --context production apply -f -

kubectl --context production -n ngd-ngg-system create secret generic lldp-kubeconfig \
  --from-file=kubeconfig=deploy/secrets/lldp.kubeconfig \
  --dry-run=client -o yaml | kubectl --context production apply -f -
```

如果使用私有镜像仓库，还要按 A3 创建镜像拉取 Secret。

### B6. 检查、部署、验证

```bash
python3 deploy/deploy.py kubernetes check \
  --config deploy/config.local.json
python3 deploy/deploy.py kubernetes up \
  --config deploy/config.local.json
python3 deploy/deploy.py kubernetes status \
  --config deploy/config.local.json
python3 deploy/deploy.py kubernetes logs \
  --config deploy/config.local.json
```

生成器会自动：

- 挂载两个 Kubeconfig Secret；
- 设置 `KUBECONFIG=/etc/ngd-ngg/auth/kubeconfig`；
- 设置 `automountServiceAccountToken: false`；
- 保留 LLDP 所需的 `hostNetwork`、`NET_RAW` 和宿主机 `/sys` 只读挂载；
- 使用 Kubeconfig 对应身份的 Node RBAC。

## 6. 路线 C：集群外部署

部署结构：

```text
管理服务器：PRC + Algorithm（Docker Compose）
    PRC ──Kubeconfig──> Kubernetes API
    Algorithm ──HTTP(S)──> Prometheus

每个 Worker：LLDP Agent（Docker Compose）
    LLDP ──Kubeconfig──> Kubernetes API
```

### C1. 修改管理服务器配置

修改 `deploy/config.local.json`：

```json
"external": {
  "projectName": "ngd-ngg",
  "prcKubeconfig": "secrets/prc.kubeconfig",
  "prcSubject": {"kind": "User", "name": "实际PRC认证用户名"},
  "lldpSubject": {"kind": "User", "name": "实际LLDP认证用户名"},
  "bindAddress": "127.0.0.1",
  "algorithmPort": 8080,
  "prcPort": 8081
}
```

同时修改：

- `images.*`：管理服务器和 Worker 能拉取的完整镜像地址；
- `topologyFile`：管理服务器上的真实上层拓扑；
- `prometheus.url`：必须从管理服务器的 Algorithm 容器访问；集群内
  `.svc` 地址通常不能在集群外直接使用；
- `external.prcKubeconfig`：管理服务器上的 PRC Kubeconfig 文件；
- `external.prcSubject/lldpSubject`：两个凭证的真实认证身份。

在管理服务器准备文件：

```text
deploy/secrets/prc.kubeconfig
```

若 Prometheus 需要认证，再填写并准备：

```json
"tokenFile": "secrets/prometheus.token",
"caFile": "secrets/prometheus-ca.crt"
```

PRC/Algorithm 镜像默认使用 UID/GID 65532，挂载文件必须允许该身份读取。

### C2. 管理员安装 CRD 和外部身份 RBAC

```bash
python3 deploy/deploy.py external bootstrap \
  --config deploy/config.local.json
```

这一步只操作 Kubernetes API，不启动容器。

### C3. 启动管理服务器

私有镜像先登录：

```bash
docker login registry.unicom.example.com
```

然后执行：

```bash
python3 deploy/deploy.py external check \
  --config deploy/config.local.json
python3 deploy/deploy.py external up \
  --config deploy/config.local.json
python3 deploy/deploy.py external status \
  --config deploy/config.local.json
```

生成文件：

```text
deploy/generated/config.local/external/compose.json
```

### C4. 在每个 Worker 启动 LLDP

每个 Worker 都要放置 `deploy/` 目录、配置文件和该节点的
`lldp.kubeconfig`。修改同一份 `deploy/config.local.json`：

```json
"images": {
  "lldp": "registry.unicom.example.com/ngd-ngg/lldp-agent:v0.5.0"
},
"lldp": {
  "nodeName": "worker-001",
  "kubeconfig": "secrets/lldp.kubeconfig",
  "interfaces": "bond0",
  "timeoutSeconds": 65,
  "count": 0,
  "intervalSeconds": 180
}
```

默认限定`bond0`。Agent将Bond Master展开为Slave：`active-backup`只监听Active
Slave，其他Bond模式监听全部Link/MII有效Slave。`count=0`时每轮跑满65秒，
超时后只要发现至少一个有效外部邻居就写入Node；不会再因为未凑够两个Leaf
而放弃打标。完成一轮后按180秒的轮次周期开始下一轮。没有`bond0`的机器应将
`interfaces`改为`auto`或真实接口名。

`nodeName` 必须与 Kubernetes Node 的 `metadata.name` 完全一致。也可以
不反复修改文件，而是在命令中覆盖：

```bash
docker login registry.unicom.example.com

python3 deploy/deploy.py node check \
  --config deploy/config.local.json \
  --node-name worker-001

python3 deploy/deploy.py node up \
  --config deploy/config.local.json \
  --node-name worker-001

python3 deploy/deploy.py node status \
  --config deploy/config.local.json \
  --node-name worker-001
```

其他 Worker 分别替换 `worker-001`。生产批量下发可由 Ansible 或已有运维
平台调用该单节点入口。

## 7. 日常操作命令

### 7.1 每个动作是什么意思

| 动作 | 是否改变环境 | 作用 |
|---|---:|---|
| `render` | 否 | 根据配置生成 Kubernetes/Compose 文件 |
| `check` | 否 | 检查配置和挂载文件，不启动服务 |
| `bootstrap` | 是 | 安装 CRD、Namespace、ServiceAccount 和 RBAC |
| `up` | 是 | 创建或更新工作负载并等待启动 |
| `status` | 否 | 查看运行状态 |
| `logs` | 否 | 查看最近日志 |
| `down` | 是 | 停止本部署方式创建的工作负载 |
| `images build` | 是 | 在本机 Docker 中构建三个镜像并导出 tar |
| `images push` | 是 | 将三个本地镜像推送到配置的远端仓库 |

### 7.2 集群内常用命令

```bash
python3 deploy/deploy.py kubernetes status --config deploy/config.local.json
python3 deploy/deploy.py kubernetes logs --config deploy/config.local.json
python3 deploy/deploy.py kubernetes up --config deploy/config.local.json
python3 deploy/deploy.py kubernetes down --config deploy/config.local.json
```

### 7.3 集群外管理服务器常用命令

```bash
python3 deploy/deploy.py external status --config deploy/config.local.json
python3 deploy/deploy.py external logs --config deploy/config.local.json
python3 deploy/deploy.py external up --config deploy/config.local.json
python3 deploy/deploy.py external down --config deploy/config.local.json
```

### 7.4 Worker LLDP 常用命令

```bash
python3 deploy/deploy.py node status --config deploy/config.local.json --node-name worker-001
python3 deploy/deploy.py node logs --config deploy/config.local.json --node-name worker-001
python3 deploy/deploy.py node up --config deploy/config.local.json --node-name worker-001
python3 deploy/deploy.py node down --config deploy/config.local.json --node-name worker-001
```

`down` 不删除 CRD、Namespace、RBAC、Secret、NGD、NGG 或已有 Node
标签。停止 LLDP 后旧标签仍会存在，不能据此判断采集服务仍然正常。

## 8. 更新操作

### 8.1 代码更新

```text
修改代码 → 设置新镜像 Tag → images build → images push → up
```

不要覆盖正在使用的旧 Tag，推荐每次发布使用唯一版本号或镜像 Digest。

### 8.2 上层拓扑更新

1. 修改 `topologyFile` 指向的 YAML；
2. 执行对应模式的 `up`；
3. Kubernetes 模式会因为配置 Hash 改变而重建 Algorithm Pod；
4. 集群外模式会强制重建 Compose 容器。

### 8.3 Kubeconfig 或 Prometheus 凭证更新

集群内先更新 Secret，再重新执行 `kubernetes up`。必要时执行：

```bash
kubectl --context production -n ngd-ngg-system \
  rollout restart deployment/prc
kubectl --context production -n ngd-ngg-system \
  rollout restart deployment/ngd-ngg-algorithm
kubectl --context production -n ngd-ngg-system \
  rollout restart daemonset/lldp-agent
```

集群外修改本地文件后重新执行 `external up` 或对应 Worker 的 `node up`。

## 9. 最终业务验收

### 9.1 检查 LLDP Node 标签

```bash
kubectl --context production get nodes \
  -L topology.demo.ngg.io/leaf-set-id,topology.demo.ngg.io/leaf-count
```

### 9.2 提交 NGD 并检查 NGG

```bash
kubectl --context production apply -f /path/to/production-ngd.yaml
kubectl --context production get nodegroupdemands.scheduling.platform.example.io -o yaml
kubectl --context production get nodegroupgrants.scheduling.platform.example.io -o yaml
```

`/healthz` 和 `/readyz` 只代表服务进程可用。最终验收必须同时确认：

- LLDP Agent 已写入预期 Node 标签；
- Algorithm 缓存中有静态拓扑和 Prometheus 指标；
- PRC 能 Watch NGD；
- Algorithm 返回节点组；
- PRC 创建符合接口定义的 NGG。

## 10. 验证部署工具本身

以下命令不会创建 Kubernetes 资源或启动业务服务：

```bash
python3 -m unittest discover -s deploy -p 'test_*.py' -v
python3 deploy/deploy.py kubernetes render --config deploy/config.example.json
python3 deploy/deploy.py external render --config deploy/config.example.json
python3 deploy/deploy.py node render --config deploy/config.example.json
```

真实部署前仍需要需求方确认镜像仓库、API Server 地址、凭证、Prometheus
地址、Worker Label/Taint 和集群准入策略。
