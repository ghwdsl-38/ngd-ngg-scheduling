# 集群 DNS 排查结论

## 结论

当前发现三个相互独立的 DNS 问题。master3 的 `clusterDomain` 已修复，PRC 已恢复通过 Service DNS 调用 Algorithm，并通过实际 NGD/NGG 计算验证。Algorithm 查询 Prometheus 仍使用固定 ClusterIP，因此剩余 DNS 问题不影响明天的演示。

### 1. kubelet 的 clusterDomain 配置错误（master3 已修复）

> 修复状态（2026-09-14）：master3 已完成配置备份、`clusterDomain` 修正和 kubelet 重启。PRC、Algorithm Pod 已重建，完整 Service FQDN 已成功解析。其他节点的 kubelet 配置尚未修改。

master3 修复前的 kubelet 配置为：

```yaml
clusterDNS: [240.5.0.10]
clusterDomain: paas-hl-tstmix7.paas-hl-tstmix7.test.cucloud.unicom.cn
```

`paas-hl-tstmix7` 重复了一次。PRC Pod 实际生成的 `/etc/resolv.conf` 为：

```text
search welkin-system.svc.paas-hl-tstmix7.paas-hl-tstmix7.test.cucloud.unicom.cn svc.paas-hl-tstmix7.paas-hl-tstmix7.test.cucloud.unicom.cn paas-hl-tstmix7.paas-hl-tstmix7.test.cucloud.unicom.cn
nameserver 240.5.0.10
options ndots:5
```

修复并重建 Pod 后，PRC 和 Algorithm 实际使用：

```text
search welkin-system.svc.paas-hl-tstmix7.test.cucloud.unicom.cn svc.paas-hl-tstmix7.test.cucloud.unicom.cn paas-hl-tstmix7.test.cucloud.unicom.cn
nameserver 240.5.0.10
options ndots:5
```

master3 原配置备份位置：

```text
/root/bink8s/cfg/kubelet.config.bak-codex-20260914
```

而 `kube-system/paasdns` 的 CoreDNS `kubernetes` 插件实际管理：

```text
paas-hl-tstmix7.test.cucloud.unicom.cn
```

结果是 Pod 根据搜索域拼接出的 Service 域名进入错误域，返回 `NXDOMAIN`。

Algorithm 原先配置的 `vmselect.prometheus-system.svc.cluster.local` 也不适用于这个集群，因为这里的集群域不是 `cluster.local`。应使用短名称 `vmselect.prometheus-system.svc`，或使用完整的自定义域名：

```text
vmselect.prometheus-system.svc.paas-hl-tstmix7.test.cucloud.unicom.cn
```

### 2. paasdns Service 包含 master3 无法访问的后端

DNS Service：

```text
kube-system/paasdns = 240.5.0.10
```

后端：

```text
10.129.195.155  master1  可达
10.129.195.196  master2  可达
10.129.195.253  master3  可达
10.129.175.100  master4  从 master3 访问 UDP/53 超时
```

对正确 Service FQDN 逐个发送 DNS 查询时，前三个后端都在 1ms 内返回一条记录，master4 超时。`paasdns` Service 的 `internalTrafficPolicy` 是 `Cluster`，会把请求分配到四个 Endpoint，因此查询可能随机超时。测试中 `240.5.0.10` 已出现同样超时。

### 3. paasdns 没有外部域名转发配置

CoreDNS 根域 `.:53` 包含 `kubernetes`、`cache`、`loop` 和 `loadbalance`，但没有 `forward`。查询 `registry-1.docker.io` 时，所有可达 DNS 后端均返回：

```text
RCODE=2 (SERVFAIL)
```

`cucloud.cn` 使用独立的 `hosts` zone，只配置了 `obs-hlcsq.cucloud.cn`，也没有 `fallthrough` 或上游转发。因此 Pod 不能依靠 `paasdns` 正常解析其他公网或内部 `cucloud.cn` 域名。

### 4. 节点主机 DNS 是另一条链路

master3 `/etc/resolv.conf` 使用：

```text
nameserver 100.127.194.100
nameserver 100.127.194.200
```

该主机 DNS 能解析 `hlcsq-registry.cucloud.cn`，但不能解析 `registry-1.docker.io`。containerd 拉取镜像使用节点主机 DNS，因此 Docker Hub 拉取失败与 Pod 的 `240.5.0.10` 搜索域错误不是同一条链路。

## 建议修复顺序

### 第一步：统一 clusterDomain（master3 已完成）

master3 已修正；其余运行工作负载的节点仍应核对，并将所有 kubelet 的 `clusterDomain` 统一为 CoreDNS 当前管理的域：

```yaml
clusterDomain: paas-hl-tstmix7.test.cucloud.unicom.cn
clusterDNS:
  - 240.5.0.10
```

修改后需要按集群运维规范滚动重启 kubelet，并重建业务 Pod，使 Pod 的 `/etc/resolv.conf` 重新生成。

### 第二步：修复 master4 DNS 后端连通性

检查 master3/其他节点到 `10.129.175.100` 的路由、防火墙和 UDP/TCP 53 端口。在恢复连通前，应避免该不可达 Pod 继续成为 `paasdns` Ready Endpoint；否则 DNS Service 仍会随机超时。

### 第三步：为根域增加上游转发

`paasdns` ConfigMap 已保存上游地址：

```text
10.172.49.5
10.172.49.6
```

根域需要在现有 Corefile 中加入类似配置：

```text
forward . 10.172.49.5 10.172.49.6
```

更具体的 `cucloud.cn` zone 也需要在 `hosts` 中启用 `fallthrough`，并配置相同上游，否则该 zone 会截获全部 `*.cucloud.cn` 查询。

示意配置：

```text
cucloud.cn {
    errors
    log
    loadbalance
    hosts {
        10.129.194.253 obs-hlcsq.cucloud.cn
        fallthrough
    }
    forward . 10.172.49.5 10.172.49.6
}
```

变更 CoreDNS 前应先备份 ConfigMap，并在测试节点验证内部 Service、`cucloud.cn` 和外部域名解析。本次已修正 master3 的 kubelet 配置；尚未修改 `paasdns`、其他节点的 kubelet 配置或节点主机 DNS。

## 修复后的验证命令

检查 DNS 服务及后端：

```bash
kubectl get svc,endpoints,endpointslice -n kube-system -l k8s-app=paas-dns -o wide
kubectl get pods -n kube-system -l k8s-app=paas-dns -o wide
```

检查 DNS 日志：

```bash
kubectl logs -n kube-system -l k8s-app=paas-dns --tail=100 --prefix
```

PRC 已恢复使用以下 Service DNS，并通过实际 NGD/NGG 计算验证：

```text
http://ngd-ngg-algorithm.welkin-system.svc:8080
```

Algorithm 到 Prometheus 目前仍使用固定 ClusterIP。完成剩余 DNS 修复后，可恢复为：

```text
http://vmselect.prometheus-system.svc:8481/select/0/prometheus
```
