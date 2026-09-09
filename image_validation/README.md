# PRC与Algorithm发布镜像验证

本目录验证打包后的Docker容器，不直接启动源码中的PRC或Algorithm。
验证结果按时间写入`image_validation/results/<run-id>/`，与`images/`目录
处于同一层级。

## 1. 发布目标

Docker Hub使用一个Repository、两个Tag：

```text
ghwdsl/ngd-ngg-scheduling:prc-v0.6.1
ghwdsl/ngd-ngg-scheduling:algorithm-v0.6.1
```

每个正式Tag包含`linux/amd64`和`linux/arm64`。本地验证Tag额外带有
`-local-amd64`，不会被Push：

```text
ghwdsl/ngd-ngg-scheduling:prc-v0.6.1-local-amd64
ghwdsl/ngd-ngg-scheduling:algorithm-v0.6.1-local-amd64
```

## 2. 构建但不Push

```bash
cd /mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo

RELEASE_VERSION=v0.6.1 make release-images
```

执行顺序是：

```text
Go 1.25构建容器
  ├─ 预编译PRC linux/amd64、linux/arm64
  └─ 预编译Algorithm Go Server linux/amd64、linux/arm64
                    ↓
纯运行时Dockerfile（没有go build）
  ├─ 生成本机amd64测试镜像
  ├─ images/ngd-ngg-prc-v0.6.1-multiarch.oci.tar
  └─ images/ngd-ngg-algorithm-v0.6.1-multiarch.oci.tar
```

`images/`中的OCI包用于在Push前确认两个架构都完成了打包；当前流程不把
它作为生产离线交付物。

## 3. 运行镜像级验证

```bash
RELEASE_VERSION=v0.6.1 make release-verify
```

验证拓扑如下：

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

1. 两个OCI包均包含`linux/amd64`和`linux/arm64`；
2. 本机加载的两个测试镜像架构均为amd64；
3. Algorithm容器启动Go服务和Python Worker；
4. Algorithm从Mock Prometheus通过真实HTTP读取14项指标并通过Bearer认证；
5. PRC容器使用临时Kubeconfig连接envtest；
6. PRC向Algorithm上传1000 Node静态快照；
7. 创建NGD后，PRC通过HTTP调用Algorithm并把NGG写回envtest；
8. NGG为Active、NGD为Fulfilled且NGG包含Node。

每次执行生成：

```text
image_validation/results/<run-id>/
├── go-test.log
├── local-amd64-images.jsonl
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

测试生成的Kubeconfig含临时客户端私钥，仅保存在Go临时目录并在测试结束
后删除。结果目录只保留不含凭证的连接摘要。

## 4. 登录后Push

先由操作者登录，不要把Token写入项目：

```bash
docker login -u ghwdsl
```

登录成功后执行：

```bash
RELEASE_VERSION=v0.6.1 make release-push
```

该命令直接发布两个多架构Tag，并使用`docker buildx imagetools inspect`
检查远端Manifest。Push不会发布`-local-amd64`测试Tag。

## 5. 验证边界

本测试能验证真实容器入口、Kubeconfig、HTTP、Python Worker、Prometheus
客户端以及NGD到NGG主链路。envtest不包含真实ServiceAccount/RBAC、集群
DNS、CNI和真实Prometheus，因此这些仍需在目标集群联调。
