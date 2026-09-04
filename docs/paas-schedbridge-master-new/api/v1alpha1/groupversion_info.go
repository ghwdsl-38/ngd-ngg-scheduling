// Package v1alpha1 包含双 CRD（NodeGroupDemand / NodeGroupGrant）的 Go API 类型。
// 本文件提供 Group/Version 标识与 Scheme 注册入口，供 controller-gen 生成 deepcopy 代码。
// +kubebuilder:object:generate=true
// +groupName=scheduling.platform.example.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion 是调度双 CRD 的 Group/Version。
var GroupVersion = schema.GroupVersion{Group: "scheduling.platform.example.io", Version: "v1alpha1"}

// SchemeBuilder 用于将本包类型注册到 runtime.Scheme。
var SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

// AddToScheme 将本包类型注册到给定的 Scheme。
var AddToScheme = SchemeBuilder.AddToScheme

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&NodeGroupDemand{},
		&NodeGroupDemandList{},
		&NodeGroupGrant{},
		&NodeGroupGrantList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

// Resource 返回给定资源名对应的 GroupResource。
func Resource(resource string) schema.GroupResource {
	return GroupVersion.WithResource(resource).GroupResource()
}
