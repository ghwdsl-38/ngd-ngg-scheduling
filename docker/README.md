# Docker镜像构建文件

本目录集中管理PRC、Algorithm和LLDP三个组件的Dockerfile。所有构建命令都以项目根目录作为Docker构建上下文。

```text
docker/
├── prc/
│   ├── Dockerfile
│   └── Dockerfile.release
├── algorithm/
│   ├── Dockerfile
│   └── Dockerfile.release
└── lldp/
    ├── Dockerfile
    └── Dockerfile.release
```

每个组件中的文件用途如下：

- `Dockerfile`：普通本地构建入口，由对应组件的构建脚本调用。
- `Dockerfile.release`：发布打包入口，使用`build/release/<version>/linux-<arch>/`中的预编译二进制生成amd64和arm64镜像。

常用命令：

```bash
make prc-image
make algorithm-image
make lldp-agent-image

RELEASE_VERSION=v0.6.2 make release-images
RELEASE_VERSION=v0.6.2 make release-verify
RELEASE_VERSION=v0.6.2 make release-push
```

只构建单个组件时使用：

```bash
RELEASE_VERSION=v0.6.2 make release-prc-image
RELEASE_VERSION=v0.6.2 make release-algorithm-image
RELEASE_VERSION=v0.6.2 make release-lldp-image
```

直接执行Docker构建时，必须从项目根目录提供上下文：

```bash
docker build -f docker/prc/Dockerfile -t ngd-ngg-prc:local .
docker build -f docker/algorithm/Dockerfile -t ngd-ngg-algorithm:local .
docker build -f docker/lldp/Dockerfile -t ngd-ngg-lldp:local .
```
