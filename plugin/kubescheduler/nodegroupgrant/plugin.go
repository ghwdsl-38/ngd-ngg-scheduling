// Package nodegroupgrant implements the kube-scheduler v1.35 Filter side of
// the shared NGG API. It is compiled into a custom kube-scheduler binary.
package nodegroupgrant

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	framework "k8s.io/kube-scheduler/framework"
)

const (
	Name             = "NodeGroupGrant"
	demandAnnotation = "scheduling.demo.ngg.io/node-group-demand"
	schedulerName    = "ngg-scheduler"
	defaultScheduler = "default-scheduler"
)

var grantGVR = schema.GroupVersionResource{
	Group: "scheduling.demo.ngg.io", Version: "v1alpha1", Resource: "nodegroupgrants",
}

type grant struct {
	namespace          string
	demandName         string
	taskUID            types.UID
	schedulerName      string
	revision           int64
	generation         int64
	observedRevision   int64
	observedGeneration int64
	phase              string
	activeGroupState   string
	validUntil         time.Time
	nodes              map[string]types.UID
}

func (g grant) valid(now time.Time) bool {
	return g.phase == "Active" &&
		(g.activeGroupState == "Trying" || g.activeGroupState == "Locked") &&
		g.generation == g.observedGeneration &&
		g.revision == g.observedRevision &&
		len(g.nodes) > 0 && now.Before(g.validUntil)
}

type plugin struct {
	mu       sync.RWMutex
	ready    bool
	byObject map[string]grant
	pending  map[types.UID]*corev1.Pod
	handle   framework.Handle
}

var _ framework.FilterPlugin = &plugin{}

func New(ctx context.Context, _ runtime.Object, handle framework.Handle) (framework.Plugin, error) {
	client, err := dynamic.NewForConfig(handle.KubeConfig())
	if err != nil {
		return nil, fmt.Errorf("build NGG dynamic client: %w", err)
	}
	p := &plugin{
		byObject: map[string]grant{},
		pending:  map[types.UID]*corev1.Pod{},
		handle:   handle,
	}
	factory := dynamicinformer.NewDynamicSharedInformerFactory(client, 0)
	informer := factory.ForResource(grantGVR).Informer()
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: p.upsert,
		UpdateFunc: func(_, newObj interface{}) {
			p.upsert(newObj)
		},
		DeleteFunc: p.remove,
	}); err != nil {
		return nil, fmt.Errorf("register NGG informer handler: %w", err)
	}
	factory.Start(ctx.Done())
	go func() {
		if cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
			p.mu.Lock()
			p.ready = true
			p.mu.Unlock()
			p.activatePending()
		}
	}()
	return p, nil
}

func (p *plugin) Name() string { return Name }

func (p *plugin) Filter(
	_ context.Context,
	_ framework.CycleState,
	pod *corev1.Pod,
	nodeInfo framework.NodeInfo,
) *framework.Status {
	demandName := pod.Annotations[demandAnnotation]
	if demandName == "" {
		return nil
	}
	p.mu.RLock()
	ready := p.ready
	g, found := p.findGrantLocked(pod.Namespace, demandName, time.Now())
	p.mu.RUnlock()
	if !ready {
		p.rememberPending(pod)
		return framework.NewStatus(framework.Error, "NGG informer cache is not ready")
	}
	if !found {
		p.rememberPending(pod)
		return framework.NewStatus(
			framework.UnschedulableAndUnresolvable,
			fmt.Sprintf("no active NGG for demand %s/%s", pod.Namespace, demandName),
		)
	}
	if pod.Spec.SchedulerName != g.schedulerName {
		return framework.NewStatus(framework.UnschedulableAndUnresolvable, "schedulerName does not match NGG")
	}
	if !ownedBy(pod, g.taskUID) {
		return framework.NewStatus(framework.UnschedulableAndUnresolvable, "Pod owner UID does not match NGG task UID")
	}
	node := nodeInfo.Node()
	if node == nil {
		return framework.NewStatus(framework.Error, "NodeInfo has no Node")
	}
	expectedUID, allowed := g.nodes[node.Name]
	if !allowed {
		return framework.NewStatus(framework.UnschedulableAndUnresolvable, fmt.Sprintf("Node %s is outside activeGroup", node.Name))
	}
	if node.UID != expectedUID {
		return framework.NewStatus(framework.UnschedulableAndUnresolvable, "Node UID does not match NGG")
	}
	p.mu.Lock()
	delete(p.pending, pod.UID)
	p.mu.Unlock()
	return nil
}

func (p *plugin) findGrantLocked(namespace, demandName string, now time.Time) (grant, bool) {
	for _, g := range p.byObject {
		if g.namespace == namespace && g.demandName == demandName && g.valid(now) {
			return g, true
		}
	}
	return grant{}, false
}

func (p *plugin) rememberPending(pod *corev1.Pod) {
	p.mu.Lock()
	p.pending[pod.UID] = pod.DeepCopy()
	p.mu.Unlock()
}

func (p *plugin) activatePending() {
	p.mu.RLock()
	pods := make(map[string]*corev1.Pod, len(p.pending))
	for uid, pod := range p.pending {
		pods[string(uid)] = pod.DeepCopy()
	}
	p.mu.RUnlock()
	if len(pods) > 0 {
		p.handle.Activate(klog.Background(), pods)
	}
}

func (p *plugin) upsert(obj interface{}) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}
	target, _, _ := unstructured.NestedString(u.Object, "spec", "schedulerName")
	if target != schedulerName && target != defaultScheduler {
		// This informer sees grants for Volcano as well. They are valid objects,
		// just outside this scheduler instance's responsibility.
		return
	}
	g, err := parseGrant(u)
	if err != nil {
		klog.ErrorS(err, "ignoring malformed NGG", "name", u.GetName())
		return
	}
	key, err := cache.MetaNamespaceKeyFunc(u)
	if err != nil {
		return
	}
	p.mu.Lock()
	p.byObject[key] = g
	p.mu.Unlock()
	p.activatePending()
}

func (p *plugin) remove(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		return
	}
	p.mu.Lock()
	delete(p.byObject, key)
	p.mu.Unlock()
}

func parseGrant(u *unstructured.Unstructured) (grant, error) {
	demandName, found, _ := unstructured.NestedString(u.Object, "spec", "demandRef", "name")
	if !found || demandName == "" {
		return grant{}, fmt.Errorf("missing spec.demandRef.name")
	}
	scheduler, _, _ := unstructured.NestedString(u.Object, "spec", "schedulerName")
	if scheduler != schedulerName && scheduler != defaultScheduler {
		return grant{}, fmt.Errorf("grant targets scheduler %q", scheduler)
	}
	taskUID, found, _ := unstructured.NestedString(u.Object, "spec", "taskRef", "uid")
	if !found || taskUID == "" {
		return grant{}, fmt.Errorf("missing spec.taskRef.uid")
	}
	revision, found, _ := unstructured.NestedInt64(u.Object, "spec", "revision")
	if !found {
		return grant{}, fmt.Errorf("missing spec.revision")
	}
	validUntilRaw, found, _ := unstructured.NestedString(u.Object, "spec", "validUntil")
	if !found {
		return grant{}, fmt.Errorf("missing spec.validUntil")
	}
	validUntil, err := time.Parse(time.RFC3339, validUntilRaw)
	if err != nil {
		return grant{}, fmt.Errorf("parse validUntil: %w", err)
	}
	activeID, found, _ := unstructured.NestedString(u.Object, "spec", "activeGroupRef", "groupId")
	if !found || activeID == "" {
		return grant{}, fmt.Errorf("missing activeGroupRef.groupId")
	}
	activeRank, found, _ := unstructured.NestedInt64(u.Object, "spec", "activeGroupRef", "rank")
	if !found {
		return grant{}, fmt.Errorf("missing activeGroupRef.rank")
	}
	groups, found, _ := unstructured.NestedSlice(u.Object, "spec", "candidateNodeGroups")
	if !found || len(groups) == 0 || len(groups) > 3 {
		return grant{}, fmt.Errorf("invalid candidateNodeGroups")
	}
	nodes := map[string]types.UID{}
	for _, raw := range groups {
		group, ok := raw.(map[string]interface{})
		if !ok || group["groupId"] != activeID || group["rank"] != activeRank {
			continue
		}
		items, ok := group["nodes"].([]interface{})
		if !ok {
			return grant{}, fmt.Errorf("active group has malformed nodes")
		}
		for _, rawNode := range items {
			node, ok := rawNode.(map[string]interface{})
			if !ok {
				return grant{}, fmt.Errorf("malformed active group node")
			}
			name, _ := node["name"].(string)
			uid, _ := node["uid"].(string)
			if name == "" || uid == "" {
				return grant{}, fmt.Errorf("active group node has no name or UID")
			}
			nodes[name] = types.UID(uid)
		}
	}
	if len(nodes) == 0 {
		return grant{}, fmt.Errorf("activeGroupRef does not resolve")
	}
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	activeState, _, _ := unstructured.NestedString(u.Object, "status", "activeGroupState")
	observedGeneration, _, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	observedRevision, _, _ := unstructured.NestedInt64(u.Object, "status", "observedRevision")
	return grant{
		namespace: u.GetNamespace(), demandName: demandName, taskUID: types.UID(taskUID),
		schedulerName: scheduler,
		revision:      revision, generation: u.GetGeneration(), observedRevision: observedRevision,
		observedGeneration: observedGeneration, phase: phase, activeGroupState: activeState,
		validUntil: validUntil, nodes: nodes,
	}, nil
}

func ownedBy(pod *corev1.Pod, uid types.UID) bool {
	for _, owner := range pod.OwnerReferences {
		if owner.UID == uid {
			return true
		}
	}
	return false
}
