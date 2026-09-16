# 镜像构建与验证

本目录统一保存PRC、Algorithm和LLDP的本地镜像产物、构建信息、镜像验证脚本及验证结果。镜像归档和运行结果不提交Git，只保留说明、验证脚本和目录占位文件。普通组件构建产生的镜像信息也写入`images/results/`。

## 1. 发布目标

Docker Hub使用一个Repository和三个Tag：

```text
ghwdsl/ngd-ngg-scheduling:prc-v0.6.2
ghwdsl/ngd-ngg-scheduling:algorithm-v0.6.2
ghwdsl/ngd-ngg-scheduling:lldp-v0.6.2
```

每个正式Tag包含`linux/amd64`和`linux/arm64`。本地验证Tag额外带有`-local-amd64`，不会被Push：

```text
ghwdsl/ngd-ngg-scheduling:prc-v0.6.2-local-amd64
ghwdsl/ngd-ngg-scheduling:algorithm-v0.6.2-local-amd64
ghwdsl/ngd-ngg-scheduling:lldp-v0.6.2-local-amd64
```

## 2. 构建但不Push

```bash
RELEASE_VERSION=v0.6.2 make release-images
```

执行顺序如下：

```text
Go 1.25构建容器
  ├─ 预编译PRC linux/amd64、linux/arm64
  ├─ 预编译Algorithm Go Server linux/amd64、linux/arm64
  └─ 预编译LLDP Agent linux/amd64、linux/arm64
                    ↓
纯运行时Dockerfile（没有go build）
  ├─ 生成本机amd64测试镜像
  ├─ images/ngd-ngg-prc-v0.6.2-multiarch.oci.tar
  ├─ images/ngd-ngg-algorithm-v0.6.2-multiarch.oci.tar
  └─ images/ngd-ngg-lldp-v0.6.2-multiarch.oci.tar
```

同时生成：

```text
images/SHA256SUMS
images/local-amd64-image-inspect.jsonl
```

三个OCI包用于Push前确认两个架构均已完成打包，不作为生产离线交付物。

## 3. 运行镜像级验证

```bash
RELEASE_VERSION=v0.6.2 make release-verify
```

`make release-verify`调用`images/verify.sh`。验证拓扑如下：

```text
Mock Prometheus（14项指标、Bearer认证、PromQL校验）
                    ↑ HTTP
Algorithm amd64容器（Go Server → Python Worker）
                    ↑ HTTP
PRC amd64容器（显式Kubeconfig）
                    ↓ Watch/Create/Status
envtest（真实kube-apiserver + etcd + 联通NGD/NGG CRD）
```

测试使用1000个模拟Node，检查：

1. 三个OCI包均包含`linux/amd64`和`linux/arm64`；
2. 本机加载的三个测试镜像架构均为amd64；
3. LLDP容器入口可执行，并提供Kubeconfig、网卡与超时参数；
4. Algorithm容器启动Go服务和Python Worker；
5. Algorithm从Mock Prometheus通过真实HTTP读取14项指标并通过Bearer认证；
6. PRC容器使用临时Kubeconfig连接envtest；
7. PRC向Algorithm上传1000个Node的静态快照；
8. 创建NGD后，PRC通过HTTP调用Algorithm并把NGG写回envtest；
9. NGG为Active、NGD为Fulfilled且NGG包含Node。

普通组件构建会在该目录写入：

```text
images/results/prc-image.txt
images/results/algorithm-image.txt
images/results/lldp-agent-image.txt
```

每次镜像级验证生成：

```text
images/results/<run-id>/
├── go-test.log
├── local-amd64-images.jsonl
├── lldp-help.txt
├── oci-platforms.txt
├── network-topology.yaml
├── ngd-input.yaml
├── summary.json
├── envtest-connection-summary.json
└── actual/
    ├── algorithm-cache.json
    ├── algorithm-container.log
    ├── mock-prometheus-requests.json
    ├── ngd-status.yaml
    ├── ngg.yaml
    └── prc-container.log
```

测试生成的Kubeconfig包含临时客户端私钥，仅保存在Go临时目录，并在测试结束后删除。结果目录只保留不含凭证的连接摘要。

## 4. 登录后Push

先由操作者登录，不要把Token写入项目：

```bash
docker login -u ghwdsl
```

登录成功后执行：

```bash
RELEASE_VERSION=v0.6.2 make release-push
```

该命令默认发布三个多架构Tag，并使用`docker buildx imagetools inspect`检查远端Manifest。Push不会发布`-local-amd64`测试Tag。

只发布LLDP时使用：

```bash
RELEASE_VERSION=v0.6.2 RELEASE_COMPONENTS=lldp make release-push
```

## 5. 验证边界

本测试能验证真实容器入口、Kubeconfig、HTTP、Python Worker、Prometheus客户端以及NGD到NGG主链路。envtest不包含真实ServiceAccount/RBAC、集群DNS、CNI和真实Prometheus，因此这些仍需在目标集群联调。
