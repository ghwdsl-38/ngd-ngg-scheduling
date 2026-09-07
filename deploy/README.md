# 统一部署入口

从 Git 拉取项目、准备镜像后，修改 `deploy/config.local.json` 即可部署。此目录封装两种方式：

| 方式 | 管理服务 | 每个 Worker 的 LLDP | 启动命令 |
|---|---|---|---|
| 集群内 | PRC/Algorithm Deployment | LLDP DaemonSet | `python3 deploy/deploy.py kubernetes up` |
| 集群外 | PRC/Algorithm Docker Compose | 节点侧 Docker Compose | 管理服务器执行 `external up`，各 Worker 执行 `node up` |

集群内 PRC 和 LLDP 各自可选择 `incluster` 或 `kubeconfig` 认证；集群外使用 Kubeconfig。Algorithm 不直接访问 Kubernetes API，无需 Kubeconfig。

新入口自动生成配置并调用 kubectl / Docker Compose，不依赖 Helm 或 PyYAML。旧 `scripts/05*-deploy*.sh` 使用原有 YAML，不读取新配置；使用本目录后不要混用两套部署入口。

## 1. 目录和准备工作

```text
deploy/
├── README.md              本说明
├── config.example.json    不含凭证的完整配置模板
├── deploy.py              init/build/push/bootstrap/render/check/up/status/logs/down 入口
├── render.py              Kubernetes / Compose 配置生成逻辑
├── test_deploy.py         部署配置离线测试
├── config.local.json      自己的配置，init 生成，Git 忽略
├── secrets/               自己保存的凭证，Git 忽略
└── generated/             自动生成的 Kubernetes/Compose JSON，Git 忽略
```

部署机需要 Python 3.9+。集群内需要 kubectl；集群外管理机和运行 LLDP 容器的 Worker 需要 Docker Engine + Compose v2（支持 `up --wait`）。构建机需要 Docker、Bash、Make，并能访问基础镜像和 Go 依赖源。当前构建脚本按构建机架构编译，应与目标节点架构一致。

所有下列命令默认在 **Git 仓库根目录** 执行。配置里的相对文件路径相对于配置文件所在目录，不依赖执行命令时的目录。

```bash
git clone <repository-url>
cd ngd-ngg-scheduling-demo
git checkout <release-tag-or-commit>

python3 deploy/deploy.py init
```

`init` 不覆盖已有 `config.local.json`。编辑该文件的主要字段：

| 字段 | 要填写什么 |
|---|---|
| `context` | 部署人员 kubectl 的目标集群 context，bootstrap/up 不使用模糊的默认 context |
| `namespace` | Kubernetes 组件及 RBAC 使用的命名空间 |
| `images.prc/algorithm/lldp` | 已发布的完整镜像地址，使用唯一版本号或 digest |
| `clusterId` | PRC 发给算法的集群标识 |
| `topologyFile` | 真实上层拓扑 YAML；默认是怀来 102 样例，必须按实际网络替换 |
| `prometheus.url` | 从 Algorithm 实际运行位置能访问的 Prometheus 地址；空字符串表示关闭采集 |
| `prometheus.nodeLabel` | 查询结果中对应 Kubernetes Node 名称的标签 |
| `kubernetes.workerNodeSelector` | LLDP 要运行的 Worker 标签；默认要求 worker 角色标签，目标集群没有此标签需修改 |
| `kubernetes.workerTolerations` | 有 Taint 的 Worker 所需的容忍规则 |
| `kubernetes.imagePullSecrets` | 已在目标 namespace 创建的拉取凭证 Secret 名称列表 |

示例镜像域名和 `REPLACE_*` 是占位符。执行部署时入口会拒绝与本次操作相关的未替换占位符。无关模式的字段可以暂不填写。

## 2. 镜像：由构建机发布一次

如果需求方已有发布镜像，直接跳到第 3 或第 4 节，不需要在运行服务器编译 Go、安装 Python Worker 环境。

发布人员先修改 `images`，然后执行：

```bash
python3 deploy/deploy.py images build
python3 deploy/deploy.py images push
```

构建沿用项目三个构建脚本；Python Worker 已包含在 Algorithm 镜像里，由 Go 服务启动。本次补齐 LLDP Dockerfile 的 `topology_agent/pkg` 复制，确保它能构建目前代码。

构建缓存和镜像归档仍使用项目的数据盘路径；Docker 自身的数据目录由宿主机 Docker 配置决定。本入口不迁移 Docker 数据目录。

## 3. 集群内部署

### 3.1 默认：In-Cluster

默认配置：

```json
"prcAuth": {"mode": "incluster", "secretName": "", "subject": null},
"lldpAuth": {"mode": "incluster", "secretName": "", "subject": null}
```

这两项位于 `kubernetes` 中。PRC 使用 ServiceAccount `prc`，LLDP 使用 `lldp-agent`。API 地址、CA 和 Token 来自 Kubernetes 注入。

管理员首次执行：

```bash
# 创建/更新联通最新版 CRD、Namespace、组件 RBAC。
python3 deploy/deploy.py kubernetes bootstrap
```

CRD 直接取自 `docs/paas-schedbridge-master-new/crd-deploy/`，不再复制一份。已有集群共享这两个 CRD 时，应由管理员统一确认版本后运行 bootstrap。凭证不会由 bootstrap 自动签发。

日常部署：

```bash
# 可选：只生成文件，方便提交给需求方审阅。
python3 deploy/deploy.py kubernetes render

# 检查配置与本地拓扑/指标目录文件，不访问集群。
python3 deploy/deploy.py kubernetes check

# 校验 Secret 是否存在，进行服务端 dry-run，应用清单并等待组件启动。
python3 deploy/deploy.py kubernetes up
```

生成文件在 `deploy/generated/config.local/kubernetes/`：

- `bootstrap.json`：Namespace、ServiceAccount、ClusterRole、ClusterRoleBinding。
- `kubernetes.json`：上层拓扑 ConfigMap、Algorithm Service、两个 Deployment 和 LLDP DaemonSet。

JSON 是 Kubernetes 支持的清单格式，可以直接交给 `kubectl -f`。上层拓扑内容变化会改变 Algorithm Pod 配置摘要，触发重建。PRC/Algorithm 均为一个副本、`Recreate` 更新，更新会有短暂中断。

### 3.2 集群内 Pod 显式挂载 Kubeconfig

本方式仍然把 PRC 和 LLDP Agent 部署为 Kubernetes Pod，只是它们不使用 Pod 自动注入的 ServiceAccount Token，而是读取管理员准备的 kubeconfig 文件：

```text
PRC Pod
  └─ Secret/prc-kubeconfig
       └─ /etc/ngd-ngg/auth/kubeconfig ──> Kubernetes API

每个 Worker 上的 LLDP Agent Pod
  └─ Secret/lldp-kubeconfig
       └─ /etc/ngd-ngg/auth/kubeconfig ──> Kubernetes API
```

Kubeconfig 只负责认证 Kubernetes API。LLDP 接收物理网卡报文仍依赖 `hostNetwork`、`NET_RAW` 和只读 sysfs 挂载，二者不能互相替代。

#### 第 1 步：生成本地正式配置

所有操作在仓库根目录执行：

```bash
python3 deploy/deploy.py init
```

如果 `deploy/config.local.json` 已存在，`init` 会拒绝覆盖，这是为了保护已有配置。打开该文件并填写目标集群和镜像。下面只是需要修改的字段示意，**不能用这个片段覆盖完整配置文件**：

```json
{
  "context": "production",
  "namespace": "ngd-ngg-system",
  "images": {
    "prc": "registry.example.com/ngd-ngg/prc:v0.5.0",
    "algorithm": "registry.example.com/ngd-ngg/algorithm:v0.5.0",
    "lldp": "registry.example.com/ngd-ngg/lldp-agent:v0.5.0"
  }
}
```

`context` 是部署管理员本机已有的 kubectl context，只用于执行 bootstrap、创建 Secret 和部署，不会作为运行时 kubeconfig 自动传入 Pod。

先确认没有选错集群：

```bash
kubectl config get-contexts
kubectl --context production cluster-info
kubectl --context production get nodes
```

#### 第 2 步：配置两个运行身份

PRC 和 LLDP 可以独立选择认证模式。下面让两者都显式使用 kubeconfig，并使用 bootstrap 会创建的两个 ServiceAccount 作为实际身份：

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

以上两个对象位于 `config.local.json` 的 `kubernetes` 中。其含义是：

| 字段 | 含义 |
|---|---|
| `mode` | 选择 `kubeconfig` 后，Pod 不再自动挂载默认 ServiceAccount Token |
| `secretName` | 保存完整 kubeconfig 的 Kubernetes Secret 名称，不是本地文件路径 |
| `subject` | kubeconfig 凭证经过 API Server 认证后的真实身份，bootstrap 按此对象创建 RBAC 绑定 |

如果管理员提供的是客户端证书，证书 Subject 的 CN 通常对应 User，此时配置类似：

```json
"subject": {"kind": "User", "name": "actual-prc-user"}
```

`subject.name` 不是 kubeconfig 的 `users[].name` 别名。User/Group 必须填写 API Server 最终识别的真实名称；填写错误会导致 Secret 正常挂载但请求返回 403。

#### 第 3 步：安装 CRD、Namespace、ServiceAccount 和 RBAC

使用部署管理员的 `context` 执行：

```bash
python3 deploy/deploy.py kubernetes bootstrap \
  --config deploy/config.local.json
```

这一步会：

- 安装或更新最新版 NGD、NGG CRD；
- 创建 `ngd-ngg-system` Namespace；
- 创建 `prc`、`lldp-agent` ServiceAccount；
- 按第 2 步的 `subject` 创建两个独立的 ClusterRoleBinding。

这一步只创建身份对象和授权，**不会签发凭证，也不会自动生成 kubeconfig**。

#### 第 4 步：取得两个真实 kubeconfig 文件

生产环境推荐让集群管理员或统一凭证平台直接提供：

```text
deploy/secrets/prc.kubeconfig
deploy/secrets/lldp.kubeconfig
```

两个文件都应包含可从 Pod 内访问的 API Server 地址、CA 和各自的专用凭证。不要写 `127.0.0.1`，除非 API Server 确实与该 Pod 位于同一个网络空间；不要复制管理员 kubeconfig 给 PRC 或所有 LLDP Pod。

拿到文件后立即限制权限：

```bash
chmod 0600 deploy/secrets/prc.kubeconfig
chmod 0600 deploy/secrets/lldp.kubeconfig
```

如果只是测试环境，也可以让管理员基于第 3 步创建的 ServiceAccount 签发短期 Token，再生成对应 kubeconfig：

```bash
mkdir -p deploy/secrets

# 从部署管理员当前 context 取得 API 地址和 CA。--flatten 会把外部 CA 文件嵌入输出。
NGG_API_SERVER="$(kubectl --context production config view \
  --raw --minify --flatten -o jsonpath='{.clusters[0].cluster.server}')"
kubectl --context production config view --raw --minify --flatten \
  -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' \
  | base64 --decode > deploy/secrets/cluster-ca.crt

# 分别签发两个短期 Token，不要共用同一个身份。
NGG_PRC_TOKEN="$(kubectl --context production -n ngd-ngg-system \
  create token prc --duration=24h)"
NGG_LLDP_TOKEN="$(kubectl --context production -n ngd-ngg-system \
  create token lldp-agent --duration=24h)"

# 生成 PRC kubeconfig，并把 CA 嵌入文件中。
kubectl --kubeconfig=deploy/secrets/prc.kubeconfig config set-cluster production \
  --server="${NGG_API_SERVER}" \
  --certificate-authority=deploy/secrets/cluster-ca.crt \
  --embed-certs=true
kubectl --kubeconfig=deploy/secrets/prc.kubeconfig config set-credentials prc-runtime \
  --token="${NGG_PRC_TOKEN}"
kubectl --kubeconfig=deploy/secrets/prc.kubeconfig config set-context production \
  --cluster=production --user=prc-runtime
kubectl --kubeconfig=deploy/secrets/prc.kubeconfig config use-context production

# 生成 LLDP kubeconfig，并把 CA 嵌入文件中。
kubectl --kubeconfig=deploy/secrets/lldp.kubeconfig config set-cluster production \
  --server="${NGG_API_SERVER}" \
  --certificate-authority=deploy/secrets/cluster-ca.crt \
  --embed-certs=true
kubectl --kubeconfig=deploy/secrets/lldp.kubeconfig config set-credentials lldp-runtime \
  --token="${NGG_LLDP_TOKEN}"
kubectl --kubeconfig=deploy/secrets/lldp.kubeconfig config set-context production \
  --cluster=production --user=lldp-runtime
kubectl --kubeconfig=deploy/secrets/lldp.kubeconfig config use-context production

# 清除当前 Shell 中的明文 Token，并限制生成文件权限。
unset NGG_PRC_TOKEN NGG_LLDP_TOKEN NGG_API_SERVER
chmod 0600 deploy/secrets/prc.kubeconfig
chmod 0600 deploy/secrets/lldp.kubeconfig
```

上面命令中的 `production` 和 `ngd-ngg-system` 必须与 `config.local.json` 一致。若管理员 kubeconfig 没有 `certificate-authority-data`，而是依赖跳过 TLS 校验或其他认证代理，不要照搬该命令，应由集群管理员给出正确的 CA 和 Pod 可达的 API 地址。

该 Token 的实际有效期由 API Server 决定，即使请求了 24 小时也可能被限制。这个办法适合联调，不适合无人值守的长期生产运行；生产必须安排凭证轮换，或使用管理员认可的客户端证书/OIDC 等机制。

一个文件的结构示意如下，内容必须由目标集群的真实信息替换：

```yaml
apiVersion: v1
kind: Config
clusters:
- name: production
  cluster:
    server: https://kubernetes-api.example.com:6443
    certificate-authority-data: <目标集群CA的Base64内容>
users:
- name: prc-runtime
  user:
    token: <PRC专用Token>
contexts:
- name: production
  context:
    cluster: production
    user: prc-runtime
current-context: production
```

LLDP 文件使用 LLDP 专用 Token。文件里的 `prc-runtime`、`production` 只是配置别名，不参与 RBAC 身份匹配。

当前 PRC/LLDP 镜像不保证包含云厂商登录插件，因此使用 `exec` 的 kubeconfig 必须先确认对应程序已打入镜像。当前最稳妥的是管理员提供可以直接使用的 Token或客户端证书 kubeconfig。

#### 第 5 步：在本地验证两个 kubeconfig

不要先部署再猜测权限。直接以两个运行身份访问目标 API：

```bash
kubectl --kubeconfig deploy/secrets/prc.kubeconfig get nodes
kubectl --kubeconfig deploy/secrets/prc.kubeconfig auth can-i list pods --all-namespaces
kubectl --kubeconfig deploy/secrets/prc.kubeconfig auth can-i get nodegroupdemands.scheduling.platform.example.io
kubectl --kubeconfig deploy/secrets/prc.kubeconfig auth can-i patch nodegroupdemands.scheduling.platform.example.io/status
kubectl --kubeconfig deploy/secrets/prc.kubeconfig auth can-i create nodegroupgrants.scheduling.platform.example.io

kubectl --kubeconfig deploy/secrets/lldp.kubeconfig get nodes
kubectl --kubeconfig deploy/secrets/lldp.kubeconfig auth can-i get nodes
kubectl --kubeconfig deploy/secrets/lldp.kubeconfig auth can-i patch nodes
kubectl --kubeconfig deploy/secrets/lldp.kubeconfig auth can-i create nodegroupgrants.scheduling.platform.example.io
```

最后一条 LLDP 检查应返回 `no`，证明它没有获得 NGG 权限。PRC 的检查应按接口文档返回 `yes`。这里不要为了测试而真的 Patch Node。

#### 第 6 步：把 kubeconfig 创建为 Kubernetes Secret

Secret Key 必须叫 `kubeconfig`，因为生成的 Pod 挂载配置固定读取这个 Key：

```bash
kubectl --context production -n ngd-ngg-system create secret generic prc-kubeconfig \
  --from-file=kubeconfig=deploy/secrets/prc.kubeconfig \
  --dry-run=client -o yaml | kubectl --context production apply -f -

kubectl --context production -n ngd-ngg-system create secret generic lldp-kubeconfig \
  --from-file=kubeconfig=deploy/secrets/lldp.kubeconfig \
  --dry-run=client -o yaml | kubectl --context production apply -f -
```

确认名称和 Key，不输出 Secret 内容：

```bash
kubectl --context production -n ngd-ngg-system get secret \
  prc-kubeconfig lldp-kubeconfig

kubectl --context production -n ngd-ngg-system get secret prc-kubeconfig \
  -o jsonpath='{.data.kubeconfig}' | wc -c
kubectl --context production -n ngd-ngg-system get secret lldp-kubeconfig \
  -o jsonpath='{.data.kubeconfig}' | wc -c
```

不要执行会把 Secret 的 Base64 内容打印到会议终端或日志系统的命令。

#### 第 7 步：检查生成配置并部署

```bash
python3 deploy/deploy.py kubernetes render \
  --config deploy/config.local.json

python3 deploy/deploy.py kubernetes check \
  --config deploy/config.local.json

python3 deploy/deploy.py kubernetes up \
  --config deploy/config.local.json
```

生成器会自动完成以下配置：

- 将两个 Secret 分别挂载到对应 Pod 的 `/etc/ngd-ngg/auth/`；
- 设置 `KUBECONFIG=/etc/ngd-ngg/auth/kubeconfig`；
- 设置 `automountServiceAccountToken: false`，确保程序不回退使用默认 Token；
- PRC 使用 `fsGroup: 65532` 读取只读 Secret；
- LLDP 使用 root、`hostNetwork`、只读 sysfs 和仅 `NET_RAW` capability；
- 不传入旧补丁中的无效 `--mode=LLDP` 参数。

#### 第 8 步：验证 Pod 确实使用显式 kubeconfig

先检查运行状态和日志：

```bash
python3 deploy/deploy.py kubernetes status \
  --config deploy/config.local.json

python3 deploy/deploy.py kubernetes logs \
  --config deploy/config.local.json
```

再检查 Deployment/DaemonSet 生成结果，以下命令只显示环境变量、挂载名称和自动挂载开关，不显示凭证：

```bash
kubectl --context production -n ngd-ngg-system get deployment prc \
  -o jsonpath='{.spec.template.spec.automountServiceAccountToken}{"\n"}{.spec.template.spec.containers[0].env}{"\n"}{.spec.template.spec.volumes}{"\n"}'

kubectl --context production -n ngd-ngg-system get daemonset lldp-agent \
  -o jsonpath='{.spec.template.spec.automountServiceAccountToken}{"\n"}{.spec.template.spec.containers[0].env}{"\n"}{.spec.template.spec.volumes}{"\n"}'
```

预期能看到：

```text
false
KUBECONFIG=/etc/ngd-ngg/auth/kubeconfig
Secret 名称分别为 prc-kubeconfig、lldp-kubeconfig
```

最终结合日志确认没有 `Unauthorized`、`Forbidden`、证书错误或 API Server 地址不可达。LLDP 的 `LLDP listen timeout` 表示没有收到交换机报文，与 kubeconfig 认证失败不是同一个问题。

#### 第 9 步：更新和轮换凭证

Secret 内容更新后，已挂载文件可能延迟刷新，而两个程序也不承诺自动重建 Kubernetes Client。更新 Secret 后显式重启对应工作负载：

```bash
kubectl --context production -n ngd-ngg-system rollout restart deployment/prc
kubectl --context production -n ngd-ngg-system rollout restart daemonset/lldp-agent

kubectl --context production -n ngd-ngg-system rollout status deployment/prc --timeout=300s
kubectl --context production -n ngd-ngg-system rollout status daemonset/lldp-agent --timeout=300s
```

确认新实例正常后再吊销旧 Token 或证书，避免凭证切换期间所有实例同时失联。

当前 PRC Manager 在没有 ServiceAccount Namespace 文件时无法自动确定 Lease Namespace，因此 **Pod 使用 Kubeconfig 时部署器关闭 Leader Election，并保持 PRC 单副本**。不要同时启动第二套 PRC 处理同一个集群。LLDP 是 DaemonSet，每个 Worker 一个实例，不使用 Leader Election。

### 3.3 Prometheus 认证与自定义指标目录

没有认证时保持 `tokenFile/caFile/secretName` 为空。有认证时，例如：

```json
"secretName": "prometheus-auth",
"tokenFile": "secrets/prometheus.token",
"caFile": "secrets/prometheus-ca.crt",
"tlsServerName": "prometheus.internal",
"metricsConfigFile": ""
```

这些字段位于 `prometheus` 内；只需要 Token 时不填 `caFile`。在 Kubernetes 模式中，非空 `tokenFile/caFile` 表示启用相应 Secret Key，不读取本地凭证。创建 Secret：

```bash
kubectl --context production -n ngd-ngg-system create secret generic prometheus-auth \
  --from-file=token=deploy/secrets/prometheus.token \
  --from-file=ca.crt=deploy/secrets/prometheus-ca.crt
```

只使用 Token 时删去 `--from-file=ca.crt`。Secret Key 必须叫 `token` / `ca.crt`。证书校验保持开启。`metricsConfigFile` 非空时，将本地 JSON 指标目录挂载给 Algorithm；为空时使用代码内嵌目录。

## 4. 集群外部署

```text
管理服务器：Docker Compose
  PRC ──HTTP──> Algorithm ──HTTP(S)──> Prometheus
   │                ↑
   │ Kubeconfig     本地上层拓扑文件
   ↓
Kubernetes API：NGD、NGG、Node、Pod
   ↑
每个 Worker：LLDP Agent，Host Network + Kubeconfig
```

LLDP 必须部署到每个要采集的 Worker 主机。管理服务器和 Worker 都不需要为这些组件创建 Pod。

### 4.1 管理服务器配置

修改配置：

- `external.prcKubeconfig`：本地 PRC 身份文件，如 `secrets/prc.kubeconfig`。
- `external.prcSubject/lldpSubject`：两类 Kubeconfig 的实际认证身份，用于管理员 bootstrap 创建 RBAC。
- `prometheus.url`：必须从管理服务器容器可达；集群内部的 `.svc` 地址通常不能直接从集群外使用，需要管理员提供可达地址。
- `prometheus.tokenFile/caFile`：需要时填写本地文件路径；此模式不使用 `secretName`。
- `topologyFile`：管理服务器上的上层拓扑文件。

凭证需嵌入 CA/Client Certificate 数据，避免依赖原机器文件路径。Kubeconfig 使用 `exec` 登录插件时当前 scratch PRC 镜像没有对应外部程序，应提供可直接使用的专用 Token/客户端证书并安排续期。PRC/Algorithm 镜像默认 UID/GID 65532，挂载文件要允许该身份读取，例如在 Linux 部署机由管理员将所需文件组设为 65532，权限设为 `0640`。不要把凭证设为全员可读。

管理员首次执行：

```bash
python3 deploy/deploy.py external bootstrap
```

该命令只安装 CRD、Namespace 和外部身份 RBAC，不创建 Deployment、DaemonSet 或 ServiceAccount。`context` 是管理员连接上下文，`prcKubeconfig` 是运行时身份，二者用途不同。

启动管理服务：

```bash
python3 deploy/deploy.py external check
python3 deploy/deploy.py external up
```

自动生成 `deploy/generated/config.local/external/compose.json`，Compose 启动 Algorithm 和 PRC。PRC 通过同一 Compose 网络中的 `http://algorithm:8080` 调用算法。Algorithm HTTP 健康检查成功后才启动 PRC；完整数据是否准备好需要查看缓存状态。

对外端口默认只绑定管理机 `127.0.0.1:8080/8081`。内部接口没有用户认证，不要直接暴露公网。改变端口可修改 `external.algorithmPort/prcPort`。

**本模式 PRC 单实例、关闭选主。运行 `up` 会重建容器，使拓扑文件和认证更新生效；更新存在短暂中断。** 不要同时运行集群内 PRC；首次切换部署方式时先停掉旧实例，再启动新实例。

### 4.2 每个 Worker 配置

将发布包中的 `deploy/` 目录放到 Worker，准备该节点的 `secrets/lldp.kubeconfig`。Worker 只需要 Python、Docker/Compose、配置和 LLDP 镜像，不需要源码或 Algorithm。

```bash
# 若只复制了 deploy 目录，进入它的上一级目录执行。
python3 deploy/deploy.py init
```

填写 `images.lldp` 和 `lldp.kubeconfig`，然后执行：

```bash
python3 deploy/deploy.py node check --node-name worker-001
python3 deploy/deploy.py node up --node-name worker-001
```

`--node-name` 必须与 Kubernetes Node `metadata.name` 完全一致，也可以写入 `lldp.nodeName`。`lldp.interfaces` 空值使用现有网卡选择逻辑，也可填写例如 `bond0`。探测模式保持当前实现：主备只选活动 Slave，其余模式按现有多链路逻辑。

节点 Compose 自动配置 Host Network、只读挂载 `/sys` 到 `/host-sys`、Kubeconfig 和 `NODE_NAME`，只保留 `NET_RAW` capability。挂载整个 sysfs 是为了让 `/sys/class/net` 指向 `devices` 的符号链接能够解析。

多节点部署时用现有运维平台/Ansible 将该步骤下发到各 Worker，并为每台机器传入正确的 Node 名称。当前封装提供可批量调用的单节点入口，不内置 SSH 登录、凭证分发或 Ansible inventory。

## 5. 查看结果、更新和停止

| 目的 | 集群内 | 集群外管理机 | Worker |
|---|---|---|---|
| 生成配置 | `kubernetes render` | `external render` | `node render` |
| 本地检查 | `kubernetes check` | `external check` | `node check` |
| 启动/更新 | `kubernetes up` | `external up` | `node up` |
| 状态 | `kubernetes status` | `external status` | `node status` |
| 最近日志 | `kubernetes logs` | `external logs` | `node logs` |
| 停止并删除本方式服务 | `kubernetes down` | `external down` | `node down` |

表中命令前统一加 `python3 deploy/deploy.py`。自定义配置路径使用 `--config /absolute/path/config.json`；路径改变时本地挂载源也按新配置目录解析。

`down` 不删除 CRD、NGD、NGG、Namespace、RBAC、凭证和 Node 标记。它会停止对应计算/采集服务；旧 Node 标记会保留，重新启用采集后才能刷新。不要把保留的标签当作仍在实时采集的证明。

更新代码时发布新镜像 Tag，修改 `images` 后重新执行 `up`。Kubernetes Secret 内容更新后，Algorithm 会在启动时重新读取 Token，因此需执行 `kubectl rollout restart deployment/ngd-ngg-algorithm -n <namespace>`；PRC Kubeconfig 凭证更新也需重启对应进程。集群外 `up` 已强制重建容器。

检查真实业务结果（以下替换 context）：

```bash
kubectl --context production get nodes \
  -L topology.demo.ngg.io/leaf-set-id,topology.demo.ngg.io/leaf-count

kubectl --context production apply -f /path/to/production-ngd.yaml
kubectl --context production get nodegroupdemands.scheduling.platform.example.io -o yaml
kubectl --context production get nodegroupgrants.scheduling.platform.example.io -o yaml
```

Algorithm 缓存状态：集群外直接执行下面的 curl；集群内先在另一终端执行 `kubectl --context production -n ngd-ngg-system port-forward service/ngd-ngg-algorithm 8080:8080`。

```bash
curl -fsS http://127.0.0.1:8080/internal/v1/cache/status
```

`/healthz` 和 `/readyz` 不代表 Prometheus/Node 静态数据已齐全。验收要检查缓存状态、PRC 日志以及 NGD 是否生成了预期的 NGG。

## 6. 验证封装本身

以下检查不启动业务服务，也不创建集群资源：

```bash
python3 -m unittest discover -s deploy -p 'test_*.py' -v
python3 deploy/deploy.py kubernetes render --config deploy/config.example.json
python3 deploy/deploy.py external render --config deploy/config.example.json
python3 deploy/deploy.py node render --config deploy/config.example.json
docker compose -f deploy/generated/config.example/external/compose.json config --quiet
docker compose -f deploy/generated/config.example/node/node.compose.json config --quiet
```

测试覆盖 Namespace/身份/RBAC、凭证引用、单实例选主设置、拓扑更新重启摘要、Compose 服务通信、原始 socket 权限、sysfs 挂载、缺失文件和占位符拒绝。生产真实网卡、交换机和集群准入策略仍需在目标环境联调。
