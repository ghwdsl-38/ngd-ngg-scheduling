# PRC应用封装说明

PRC现在以`pkg/application.Application`作为统一运行边界。正式进程和Go集成测试都只负责提供配置并启动Application，不再分别手工创建Manager、静态快照Controller、NGD Reconciler和共享状态。

```text
prc/cmd/main.go 或 Group3/Group4
        |
        | application.New(Config)
        v
pkg/application.Application
  |- controller-runtime Manager
  |- NodeStaticSnapshotReconciler
  |- NodeGroupDemandReconciler
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
| `pkg/controller/prc_controller.go` | Watch正式NGD，读取Node动态状态，调用Algorithm并创建正式NGG。 |
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

Group3和Group4均使用该封装，因此测试执行的是与正式`cmd/main.go`相同的Manager和Controller注册逻辑。
