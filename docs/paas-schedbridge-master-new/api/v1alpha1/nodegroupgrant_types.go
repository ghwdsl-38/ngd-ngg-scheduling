// Package v1alpha1 定义双 CRD 的 Go API 类型。
// 本文件为 NodeGroupGrant (NGG) 对应的 Go Struct，
// 与 crd-deploy/nodegroupgrant-crd.yaml 的 openAPIV3Schema 逐字段对齐。
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeGroupGrantSpec 是 NGG 的 spec 区域，由 B（PRC）写入供 C（Volcano 插件）消费。
type NodeGroupGrantSpec struct {
	// 该节点组授权归属的调度器名称。
	// Volcano 插件通过此字段过滤归属自己的 NGG。
	// 例如 "volcano"。
	SchedulerName string `json:"schedulerName"`

	// 契约版本号。当前为 "v1"。
	// 消费方启动时校验，不匹配则拒绝调度并报错。
	// 不兼容变更走新版本号，双方同时升级。
	Version string `json:"version"`

	// 负载数据快照时间（UTC RFC3339）。
	// 消费方用于时效校验：当前时间 - timestamp > expirySeconds 则判过期。
	Timestamp metav1.Time `json:"timestamp"`

	// 数据来源标识，告知消费方当前数据的可信度：
	//   - normal: 正常负载感知，数据可信；
	//   - degraded: 负载数据不可用，CR 含全节点但 score 为中性值 50；
	//   - disabled: 负载感知关闭，全节点放行。
	// +kubebuilder:validation:Enum=normal;degraded;disabled
	Source DataSource `json:"source"`

	// 关联的需求来源标识（可选）。
	// 可以是 NodeGroupDemand CR 名称、静态配置标识、或人工标注。
	// 用于追溯该 NGG 是由什么需求驱动的。
	// +optional
	DemandRef string `json:"demandRef,omitempty"`

	// 候选节点列表，已按 score 降序排列。
	// 这组节点是 PRC 根据拓扑需求筛选 + 负载感知过滤后的结果。
	// 消费方只应在这组节点内做调度决策。
	Nodes []NodeGroupGrantNode `json:"nodes"`
}

// DataSource 表示 NGG 负载数据来源的可信度。
type DataSource string

const (
	// SourceNormal 正常负载感知，数据可信。
	SourceNormal DataSource = "normal"
	// SourceDegraded 负载数据不可用，CR 含全节点但 score 为中性值 50。
	SourceDegraded DataSource = "degraded"
	// SourceDisabled 负载感知关闭，全节点放行。
	SourceDisabled DataSource = "disabled"
)

// NodeGroupGrantNode 表示 NGG 中的一个候选节点。
type NodeGroupGrantNode struct {
	// 节点名，对应 K8s Node 的 metadata.name。
	Name string `json:"name"`

	// 负载感知分数。0-100，越高表示节点越空闲、越适合调度。
	// 计算方式：基于节点 CPU/内存空闲度的加权综合评分。
	// 消费方在 ScoreFn 中使用 score x scoreWeight 作为负载分贡献。
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Score int32 `json:"score"`

	// 基于真实负载计算的可用资源（可选字段）。
	// 区别于 K8s node.status.allocatable（基于 request 计算）。
	// 用于信息展示和 ScoreFn 辅助，不用于 Filter 拦截。
	// +optional
	Resources *NodeResources `json:"resources,omitempty"`

	// 节点网络拓扑信息（可选）。
	// 用于信息展示和多 NGG 场景下的节点分组参考。
	// +optional
	Topology *NodeTopology `json:"topology,omitempty"`
}

// NodeResources 表示基于真实负载计算的节点可用资源。
type NodeResources struct {
	// 基于"实际负载"的可用 CPU 量。
	// 计算方式：节点 CPU 容量 - 实际 CPU 占用。示例值："8000m"。
	// +optional
	CPUAvailable string `json:"cpuAvailable,omitempty"`
	// 基于"实际负载"的可用内存量。
	// 计算方式：节点内存容量 - 实际内存占用。示例值："32Gi"。
	// +optional
	MemoryAvailable string `json:"memoryAvailable,omitempty"`
	// 5 分钟平均吞吐量。
	// +optional
	Throughput5mAvg string `json:"throughput5mAvg,omitempty"`
}

// NodeTopology 表示节点的网络拓扑信息。
type NodeTopology struct {
	// 数据中心标识。
	// +optional
	DataCenter string `json:"dataCenter,omitempty"`
	// 机房标识。
	// +optional
	Room string `json:"room,omitempty"`
	// 汇聚交换机标识（IP 或名称）。
	// +optional
	ConvergenceSwitch string `json:"convergenceSwitch,omitempty"`
	// 接入交换机标识（IP 或名称）。
	// +optional
	AccessSwitch string `json:"accessSwitch,omitempty"`
	// Border 交换机标识（IP 或名称）。
	// +optional
	BorderSwitch string `json:"borderSwitch,omitempty"`
	// Spine 交换机标识（IP 或名称）。
	// +optional
	SpineSwitch string `json:"spineSwitch,omitempty"`
	// Leaf 交换机标识（IP 或名称）。
	// +optional
	LeafSwitch string `json:"leafSwitch,omitempty"`
	// 子网标识。
	// +optional
	Subnet string `json:"subnet,omitempty"`
	// 机架标识。
	// +optional
	Rack string `json:"rack,omitempty"`
	// 网络跳数。
	// +optional
	Hops int32 `json:"hops,omitempty"`
	// 节点可到达的外部存储集群列表（如 ["ozone", "shuffle"]）。
	// +optional
	NetworkReachable []string `json:"networkReachable,omitempty"`
}

// GrantPhase 表示 NGG 的节点组生命周期状态。
type GrantPhase string

const (
	// GrantActive 正常可用，PRC 持续更新节点和负载数据。
	GrantActive GrantPhase = "Active"
	// GrantReclaimRequested 平台组发起回收，标记目标节点组。
	GrantReclaimRequested GrantPhase = "ReclaimRequested"
	// GrantDraining 数据组执行排空中。
	GrantDraining GrantPhase = "Draining"
	// GrantReturned 节点已归还，可重新分配。
	GrantReturned GrantPhase = "Returned"
)

// NodeGroupGrantStatus 是 NGG 的 status 区域。
// phase/resolvedCapacity 由 B（PRC）写入，consumer 由 C（数据组）写入。
type NodeGroupGrantStatus struct {
	// 节点组生命周期状态。MVP 阶段仅使用 Active。
	// +optional
	// +kubebuilder:validation:Enum=Active;ReclaimRequested;Draining;Returned
	Phase GrantPhase `json:"phase,omitempty"`

	// 当前 NGG 实际解析到的节点资源总量（PRC 写入）。
	// +optional
	ResolvedCapacity *ResolvedCapacity `json:"resolvedCapacity,omitempty"`

	// 消费方（数据组）反馈的使用状态。
	// 平台组只读此区域，用于了解资源使用情况和回收进展。
	// +optional
	Consumer *ConsumerStatus `json:"consumer,omitempty"`
}

// ResolvedCapacity 表示 NGG 实际解析到的节点资源总量。
type ResolvedCapacity struct {
	// 节点数。
	// +optional
	Nodes int32 `json:"nodes,omitempty"`
	// CPU 总量。
	// +optional
	CPU string `json:"cpu,omitempty"`
	// 内存总量。
	// +optional
	Memory string `json:"memory,omitempty"`
}

// ConsumerStatus 表示消费方（数据组）反馈的使用状态。
type ConsumerStatus struct {
	// 消费方已接受并生效的 NGG spec generation。
	// 用于平台组判断数据组是否已消费最新版本。
	// 若 acceptedGeneration < metadata.generation，说明消费方尚未消费最新变更。
	// +optional
	AcceptedGeneration int64 `json:"acceptedGeneration,omitempty"`

	// 当前实际使用量。
	// +optional
	Used *ResourceUsage `json:"used,omitempty"`

	// 回收排空状态（Phase 3）。
	// +optional
	Drain *DrainStatus `json:"drain,omitempty"`
}

// ResourceUsage 表示当前实际使用量。
type ResourceUsage struct {
	// CPU 使用量。
	// +optional
	CPU string `json:"cpu,omitempty"`
	// 内存使用量。
	// +optional
	Memory string `json:"memory,omitempty"`
}

// DrainStatus 表示回收排空状态。
type DrainStatus struct {
	// 回收请求 ID。
	// +optional
	RequestID string `json:"requestId,omitempty"`
	// 剩余未排空 Pod 数。
	// +optional
	RemainingPods int32 `json:"remainingPods,omitempty"`
	// 阻塞排空的原因列表。
	// +optional
	Blockers []string `json:"blockers,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=ngg
// +kubebuilder:subresource:status

// NodeGroupGrant 是 B（平台组 PRC）向 C（数据组 Volcano 插件）授权的可用节点组。
type NodeGroupGrant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeGroupGrantSpec   `json:"spec"`
	Status NodeGroupGrantStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeGroupGrantList 包含 NodeGroupGrant 的列表。
type NodeGroupGrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeGroupGrant `json:"items"`
}
