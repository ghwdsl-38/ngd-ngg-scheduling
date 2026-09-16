# PRC、Algorithm与LLDP Agent In-Cluster部署

本目录部署PRC、Algorithm和LLDP Agent。PRC与LLDP Agent使用Kubernetes
自动注入的ServiceAccount Token访问API Server，不需要Kubeconfig文件；
Algorithm不访问Kubernetes API。

## 清单内容

目录中的清单包括：

1. `lldp-agent.yaml`：LLDP ServiceAccount、Node最小权限RBAC和DaemonSet；
2. `algorithm.yaml`：上层拓扑ConfigMap、Algorithm Service和Deployment；
3. `prc.yaml`：PRC ServiceAccount、NGD/NGG等权限RBAC和Deployment；
4. `kustomization.yaml`：将三个组件作为一套清单部署。

```text
LLDP Agent Pod
  ├─ Host Network + NET_RAW接收LLDP报文
  ├─ 只读读取宿主机/sys
  ├─ 使用ServiceAccount Token连接API Server
  └─ Get/Patch本Pod所在的Kubernetes Node

PRC Pod
  ├─ 使用ServiceAccount Token Watch NGD、读取Node/Pod
  ├─ 通过HTTP调用ngd-ngg-algorithm:8080
  └─ 创建/更新NGG并维护NGD/NGG Status

Algorithm Pod
  ├─ 从ConfigMap读取Leaf以上静态拓扑
  ├─ 每15秒从Prometheus刷新节点指标
  └─ 接收PRC请求并调用Python Worker计算候选节点组
```

## 当前配置

```text
镜像: hlcsq-registry.cucloud.cn/paas/scheduling/lldp:v0.6.2
接口: 自动发现（未显式传入 --interfaces）
单轮探测: 65秒
执行周期: 180秒
Node选择器: 无
容忍: virtual-kubelet.io/provider=dubhe:NoSchedule
```

三个组件当前使用以下`v0.6.2`镜像：

```text
hlcsq-registry.cucloud.cn/paas/scheduling/lldp:v0.6.2
huailaitst-registry.cucloud.cn:30028/paas/scheduling/prc:v0.6.2
huailaitst-registry.cucloud.cn:30028/paas/scheduling/algorithm:v0.6.2
```

部署前检查：

- Algorithm 已指向当前集群的 VictoriaMetrics vmselect，并挂载适配`paas_node_ip`的14项指标目录；
- `algorithm.yaml`中ConfigMap里的真实上层拓扑；
- `prc.yaml`中的`CLUSTER_ID`；
- NGD和NGG CRD必须已经安装到集群。

部署前可执行`kubectl get nodes --show-labels`了解Node分布。当前LLDP清单未显式指定
`--interfaces`，由Agent自动发现可用接口。

## 部署

DaemonSet默认不使用`nodeSelector`，会尝试覆盖所有未被Taint阻止的Node。
如果只希望部署到部分Node，可以再按真实标签增加`nodeSelector`。

先查看最终清单：

```bash
kubectl kustomize deploy-lldp-incluster
```

部署完整三组件：

```bash
kubectl apply -k deploy-lldp-incluster
```

也可以分别部署：

```bash
kubectl apply -f deploy-lldp-incluster/algorithm.yaml
kubectl apply -f deploy-lldp-incluster/prc.yaml
kubectl apply -f deploy-lldp-incluster/lldp-agent.yaml
```

## 验证

```bash
kubectl -n welkin-system get daemonset/lldp-agent
kubectl -n welkin-system get deployment/platform-resource-controller deployment/ngd-ngg-algorithm
kubectl -n welkin-system get service/ngd-ngg-algorithm
kubectl -n welkin-system get pods -l app=ngd-ngg-lldp-agent -o wide
kubectl -n welkin-system logs daemonset/lldp-agent --tail=200
kubectl -n welkin-system logs deployment/ngd-ngg-algorithm --tail=200
kubectl -n welkin-system logs deployment/platform-resource-controller --tail=200

kubectl auth can-i get nodes \
  --as=system:serviceaccount:welkin-system:lldp-agent
kubectl auth can-i patch nodes \
  --as=system:serviceaccount:welkin-system:lldp-agent

kubectl auth can-i list nodegroupdemands.scheduling.platform.example.io \
  --as=system:serviceaccount:welkin-system:prc
kubectl auth can-i create nodegroupgrants.scheduling.platform.example.io \
  --as=system:serviceaccount:welkin-system:prc

kubectl get nodes \
  -L topology.demo.ngg.io/leaf-switch,topology.demo.ngg.io/leaf-set-id,topology.demo.ngg.io/leaf-count
```

四条`auth can-i`命令都应返回`yes`。

## 更新与删除

```bash
kubectl apply -k deploy-lldp-incluster
kubectl -n welkin-system rollout status deployment/ngd-ngg-algorithm --timeout=300s
kubectl -n welkin-system rollout status deployment/platform-resource-controller --timeout=300s
kubectl -n welkin-system rollout status daemonset/lldp-agent --timeout=300s

kubectl delete -k deploy-lldp-incluster
```

删除清单不会删除NGD、NGG、CRD，也不会自动清理Node上已有的拓扑标签和注解。
