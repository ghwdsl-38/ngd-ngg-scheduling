# kube-scheduler NodeGroupGrant 插件

`nodegroupgrant/plugin.go` 是 Kubernetes v1.35 调度框架的 Filter 实现，语义与 Volcano 插件一致：

- 非受管 Pod 跳过；
- 受管 Pod 缺少有效 NGG 时 Fail Closed；
- 只允许 `activeGroupRef` 指向的节点组；
- 校验任务 UID、Node UID、generation、revision 和 TTL；
- NGG Informer 更新后主动 Activate 之前被拒绝的 Pod。

Kubernetes v1.35 把主要调度框架接口移动到了 `k8s.io/kube-scheduler/framework`，因此该源码必须随锁定的 Kubernetes v1.35.x 源码编译，不能作为运行时动态插件注入官方镜像。

注册入口使用：

```go
app.NewSchedulerCommand(
    app.WithPlugin(nodegroupgrant.Name, nodegroupgrant.New),
)
```

当前九 Worker 主演示先验证 Volcano 完整闭环。待 Docker `data-root` 迁入数据盘后，再加入 Kubernetes 源码 Worktree、注册补丁、构建脚本和 Kubernetes Job 端到端用例。
