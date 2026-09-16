# 测试目录

本目录集中保存项目级测试、Python单元测试与集群验收用例。

```text
test/
├── go/       当前正式Go测试、envtest、镜像验证测试和3000 Node性能测试
├── ngd/      真实集群NGD/NGG资源、拓扑和失败根因测试
├── python/   Algorithm Python Worker单元测试
└── legacy/   早期Python包装的三组测试，仅保留用于历史对照
```

## 正式Go测试

推荐通过项目根目录的Makefile执行：

```bash
make go-test-all
make benchmark-3000-all
```

单组命令、输入、预期输出、实际结果和Debug方法见[`go/README.md`](go/README.md)。

## 集群NGD测试

测试用例和集群执行方式见[`ngd/README.md`](ngd/README.md)。在演示机`/root/ghw`下同步项目后，测试目录是`/root/ghw/test/ngd`。

## Python测试

```bash
PYTHONPATH=algorithm_server/python python -m unittest discover -s test/python -p 'test_*.py'
```

## 旧测试

`legacy/`是正式Go测试之前的Python包装测试。它依赖已经清理的旧消费模块，当前不作为可执行测试入口；保留它只用于查看早期测试设计和结果格式。

## 组件内Go测试

以下Go测试按包结构保留在组件源码旁：

- `prc/**/*_test.go`
- `algorithm_server/go/**/*_test.go`
- `topology_agent/**/*_test.go`

Go测试要求测试文件与被测包位于同一模块或包结构中，移动这些文件会破坏包级测试和内部符号访问。
