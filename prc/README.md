# PRC应用封装说明

PRC现在以`pkg/application.Application`作为统一运行边界。正式进程和Go集成测试都只负责提供配置并启动Application，不再分别手工创建Manager、静态快照Controller、NGD Reconciler和共享状态。

```text
prc/cmd/main.go 或 Group3/Group4/Group5
        |
        | application.New(Config)
        v
pkg/application.Application
  |- controller-runtime Manager
  |- NodeStaticSnapshotReconciler
  |- NodeGroupDemandReconciler（只Watch NGD）
  |- RefreshScheduler（只产生立即/15秒GenericEvent）
  |- RefreshReconciler（原生WorkQueue和Worker）
  |- DemandProcessor（一次NGD到NGG业务处理）
  `- 私有StaticSnapshotState
        |
        | Application.Start(ctx)
        v
Kubernetes Watch/Reconcile + Algorithm HTTP调用
```

## 目录与职责

| 文件 | 职责 |
|---|---|
| `cmd/main.go` | 正式进程入口；只读取参数、创建`Application`并运行。 |
| `pkg/application/application.go` | 完整PRC组装和生命周期边界；注册全部Controller并统一提供启动与Ready等待。 |
| `pkg/controller/static_snapshot_controller.go` | 独立Watch Node/拓扑，构造内容Hash静态快照并通过HTTP PUT同步给Algorithm。 |
| `pkg/controller/refresh_controller.go` | NGD事件入口和独立刷新Controller；后者使用controller-runtime原生队列、并发Worker及错误重试。 |
| `pkg/controller/refresh_scheduler.go` | 维护每个NGD的下一次执行时间，只向Refresh Controller发送GenericEvent，不自行实现Worker Pool。 |
| `pkg/controller/prc_controller.go` | `DemandProcessor`业务实现：读取Node动态状态、调用Algorithm、通过SSA创建或更新正式NGG。 |
| `pkg/controller/algorithm_client.go` | PRC到Algorithm的HTTP协议客户端。 |
| `pkg/controller/snapshot.go` | 静态快照和Node动态调度状态构造。 |

## 对外使用方式

```go
app, err := application.New(application.Config{
    KubernetesConfig: restConfig,
    Scheme:           scheme,
    AlgorithmURL:     algorithmURL,
    ClusterID:        clusterID,
})
if err != nil {
    return err
}
return app.Start(ctx)
```

集成测试可在启动后调用`WaitForReady(ctx)`。它会同时等待Kubernetes informer cache同步，以及静态快照被当前Algorithm进程确认。`StaticSnapshotStatus()`只暴露只读状态，测试无法再直接修改PRC内部共享对象。

Group3、Group4和Group5均使用该封装，因此测试执行的是与正式`cmd/main.go`相同的Manager和Controller注册逻辑。生产默认每15秒刷新；Group5通过配置缩短周期，验证不修改NGD的循环刷新、NGD spec修改、`status.consumer`字段隔离以及NGD/NGG删除清理。
