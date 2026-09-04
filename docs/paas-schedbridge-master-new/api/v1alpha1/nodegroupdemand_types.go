// Package v1alpha1 定义双 CRD 的 Go API 类型。
// 本文件为 NodeGroupDemand (NGD) 对应的 Go Struct，
// 与 crd-deploy/nodegroupdemand-crd.yaml 的 openAPIV3Schema 逐字段对齐。
//
// 约定：
//   - spec.topologyLabels 的 key 统一使用 topology.kubernetes.io/ 前缀；
//   - value 为具体标识表示"指定位置"（硬性过滤到该位置）；
//   - value 为 "requiredSame" 表示"同一位置（任意）"。
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeGroupDemandSpec 是 NGD 的 spec 区域，由 A（需求方）写入。
type NodeGroupDemandSpec struct {
	// 目标调度器名称。PRC 根据 schedulerName 确定为哪个调度器创建 NGG。
	// 例如 "volcano"。
	SchedulerName string `json:"schedulerName"`

	// 节点筛选条件，与 K8s LabelSelector 语义一致。
	// PRC 遍历集群 Node，仅匹配的节点进入候选集。
	// +optional
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`

	// 拓扑硬性约束标签（可选）。
	// 用于表达同数据中心/同机房/同汇聚/同接入/Border/Spine/Leaf 交换机等拓扑硬性约束，
	// PRC 据此筛选拓扑位置一致的节点。
	// key 统一使用 topology.kubernetes.io/ 前缀。
	// value 语义：
	//   - 具体标识：指定位置，候选节点必须位于该拓扑位置；
	//   - "requiredSame"：同一位置（任意），PRC 挑选一个分组，候选集节点全部落在同一分组内。
	// +optional
	TopologyLabels map[string]string `json:"topologyLabels,omitempty"`

	// 候选节点数量上限（可选）。
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaxNodes *int32 `json:"maxNodes,omitempty"`

	// 资源配额上限（可选）。
	// +optional
	Quota *ResourceRequirements `json:"quota,omitempty"`

	// 最低吞吐量要求（按 5 分钟平均水平计算）。
	// 不满足的节点被过滤，不进入候选集。
	// +optional
	MinThroughput string `json:"minThroughput,omitempty"`

	// 跨集群互访 IP 亲和约束（可选）。
	// PRC 根据 IP 列表反查汇聚层，筛选同汇聚层的节点。
	// +optional
	CrossClusterAffinity *CrossClusterAffinity `json:"crossClusterAffinity,omitempty"`

	// 本集群互访 IP 亲和约束（可选）。
	// 强制与指定 IP 列表同汇聚层，用于扩容场景。
	// +optional
	IntraClusterAffinity *IntraClusterAffinity `json:"intraClusterAffinity,omitempty"`

	// 节点组最小资源保障（可选）。
	// PRC 校验候选集总资源是否满足，不足则 NGD status.phase=Failed。
	// +optional
	MinResources *ResourceRequirements `json:"minResources,omitempty"`

	// 外部存储集群网络互通要求（可选）。
	// PRC 据此筛选能与指定存储集群网络互通的节点。
	// +optional
	NetworkReachability *NetworkReachability `json:"networkReachability,omitempty"`

	// 子网偏好（可选，软性）。
	// 优先调度到该子网的节点，但不作为硬性过滤条件。
	// +optional
	PreferredSubnet string `json:"preferredSubnet,omitempty"`
}

// ResourceRequirements 表示 CPU/内存资源要求（字符串形式的 Kubernetes quantity）。
type ResourceRequirements struct {
	// CPU 量，如 "100"、"8000m"。
	// +optional
	CPU string `json:"cpu,omitempty"`
	// 内存量，如 "400Gi"。
	// +optional
	Memory string `json:"memory,omitempty"`
}

// CrossClusterAffinity 表示跨集群互访 IP 亲和约束。
type CrossClusterAffinity struct {
	// 互访实例 IP 列表。
	// +optional
	IPList []string `json:"ipList,omitempty"`
	// 是否强制与互访实例同汇聚层。
	// +kubebuilder:default=false
	// +optional
	Enforce bool `json:"enforce,omitempty"`
}

// IntraClusterAffinity 表示本集群互访 IP 亲和约束。
type IntraClusterAffinity struct {
	// 互访实例 IP 列表。
	// +optional
	IPList []string `json:"ipList,omitempty"`
}

// NetworkReachability 表示外部存储集群网络互通要求。
type NetworkReachability struct {
	// 外部存储集群标识列表，如 ["ozone", "shuffle"]。
	// +optional
	Targets []string `json:"targets,omitempty"`
}

// DemandPhase 表示 NGD 的需求处理状态。
type DemandPhase string

const (
	// DemandPending 表示 PRC 尚未处理。
	DemandPending DemandPhase = "Pending"
	// DemandFulfilled 表示已创建/更新对应 NGG，需求已满足。
	DemandFulfilled DemandPhase = "Fulfilled"
	// DemandUpdating 表示需求变更后正在重新处理。
	DemandUpdating DemandPhase = "Updating"
	// DemandFailed 表示处理失败（如筛选条件无匹配节点）。
	DemandFailed DemandPhase = "Failed"
)

// NodeGroupDemandStatus 是 NGD 的 status 区域，由 B（PRC）写入供 A 查看。
type NodeGroupDemandStatus struct {
	// 需求处理状态。
	// +optional
	// +kubebuilder:validation:Enum=Pending;Fulfilled;Updating;Failed
	Phase DemandPhase `json:"phase,omitempty"`

	// PRC 为该需求创建的 NodeGroupGrant CR 名称。
	// A 可通过此引用查看授权详情。
	// +optional
	GrantRef string `json:"grantRef,omitempty"`

	// 实际解析到的候选节点数。可能小于 maxNodes（集群中匹配节点不足）。
	// +optional
	ResolvedNodeCount int32 `json:"resolvedNodeCount,omitempty"`

	// PRC 最后一次处理该需求的时间。
	// +optional
	LastUpdated metav1.Time `json:"lastUpdated,omitempty"`

	// 人类可读的状态信息（如失败原因）。
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=ngd
// +kubebuilder:subresource:status

// NodeGroupDemand 是 A（数据组/业务方）向 B（平台组 PRC）声明的节点组需求。
type NodeGroupDemand struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeGroupDemandSpec   `json:"spec"`
	Status NodeGroupDemandStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeGroupDemandList 包含 NodeGroupDemand 的列表。
type NodeGroupDemandList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeGroupDemand `json:"items"`
}
