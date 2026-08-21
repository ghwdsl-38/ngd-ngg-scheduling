# ChangeLog

本文件将记录本项目的重要变更。

## 2026-08-05

### Changed
- 更新调度策略文档《调度需求分析-拓扑感知调度》中 H9 项名称，由「外部存储网络互通」调整为「外部网络互通」

## 2026-07-31

### Added
- 项目初始化，新增 README.md
- 新增《双 CRD 接口设计文档》，定义 NodeGroupDemand (NGD) 与 NodeGroupGrant (NGG) 两个 CRD 的三方协作模型（A 需求方 / B 供给方 PRC / C 消费方 Volcano）、数据流方向及完整字段规范
- 新增《调度需求分析-拓扑感知调度》文档，梳理 9 项硬性调度需求（H1-H9）、7 项软性调度需求（S1-S7）、拓扑层级模型及集群创建场景需求
- 新增 CRD 部署文件及示例：
  - `crd-deploy/nodegroupdemand-crd.yaml`（NGD CRD 定义）
  - `crd-deploy/nodegroupdemand-cr-example.yaml`（NGD 示例）
  - `crd-deploy/nodegroupgrant-crd.yaml`（NGG CRD 定义）
  - `crd-deploy/nodegroupgrant-cr-example.yaml`（NGG 示例）