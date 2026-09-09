# 镜像产物目录

本目录保存PRC与Algorithm在Push前生成的本地镜像产物。生成文件不提交Git，
只保留本说明和`.gitkeep`。

```bash
RELEASE_VERSION=v0.6.1 make release-images
```

当前版本会生成：

```text
ngd-ngg-prc-v0.6.1-multiarch.oci.tar
ngd-ngg-algorithm-v0.6.1-multiarch.oci.tar
SHA256SUMS
local-amd64-image-inspect.jsonl
```

两个OCI包都包含`linux/amd64`和`linux/arm64`。它们用于Push前检查，不是
本项目的离线交付格式。完整构建、验证和Push流程见
[`../image_validation/README.md`](../image_validation/README.md)。
