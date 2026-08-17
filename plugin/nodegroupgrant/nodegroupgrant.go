/*
Copyright 2026 The Volcano Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package nodegroupgrant limits managed Pods to the task-level candidate Nodes
// published by the Pool Resource Controller.
package nodegroupgrant

import (
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/framework"
)

const (
	PluginName       = "nodegroupgrant"
	DemandAnnotation = "scheduling.demo.ngg.io/node-group-demand"
	SchedulerName    = "volcano"
)

var grantGVR = schema.GroupVersionResource{
	Group: "scheduling.demo.ngg.io", Version: "v1alpha1", Resource: "nodegroupgrants",
}

type grant struct {
	objectKey          string
	namespace          string
	demandName         string
	demandUID          types.UID
	taskUID            types.UID
	revision           int64
	generation         int64
	observedRevision   int64
	observedGeneration int64
	phase              string
	activeGroupState   string
	activeGroupID      string
	activeGroupRank    int64
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

type storeSnapshot struct {
	ready     bool
	errorText string
	byDemand  map[string]grant
}

type grantStore struct {
	mu        sync.RWMutex
	ready     bool
	errorText string
	byObject  map[string]grant
}

func newGrantStore() *grantStore {
	return &grantStore{byObject: map[string]grant{}}
}

func (s *grantStore) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errorText = err.Error()
}

func (s *grantStore) setReady() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = true
	s.errorText = ""
}

func (s *grantStore) upsert(obj interface{}) {
	g, err := parseGrant(obj)
	if err != nil {
		klog.Warningf("plugin=%s ignored invalid NGG: %v", PluginName, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byObject[g.objectKey] = g
}

func (s *grantStore) remove(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		klog.Warningf("plugin=%s failed to obtain deleted NGG key: %v", PluginName, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byObject, key)
}

func (s *grantStore) snapshot(now time.Time) storeSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := storeSnapshot{
		ready: s.ready, errorText: s.errorText, byDemand: map[string]grant{},
	}
	for _, g := range s.byObject {
		if g.valid(now) {
			result.byDemand[g.namespace+"/"+g.demandName] = g
		}
	}
	return result
}

func (s *grantStore) run() {
	config, err := rest.InClusterConfig()
	if err != nil {
		s.setError(fmt.Errorf("build in-cluster config: %w", err))
		return
	}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		s.setError(fmt.Errorf("build dynamic client: %w", err))
		return
	}
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		client, 0, metav1.NamespaceAll, nil,
	)
	informer := factory.ForResource(grantGVR).Informer()
	_, err = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    s.upsert,
		UpdateFunc: func(_, newObj interface{}) { s.upsert(newObj) },
		DeleteFunc: s.remove,
	})
	if err != nil {
		s.setError(fmt.Errorf("register NGG handler: %w", err))
		return
	}
	stop := make(chan struct{})
	factory.Start(stop)
	if !cache.WaitForCacheSync(stop, informer.HasSynced) {
		s.setError(fmt.Errorf("NGG informer cache did not sync"))
		return
	}
	s.setReady()
	klog.Infof("plugin=%s NGG informer cache synced", PluginName)
	select {}
}

func parseGrant(obj interface{}) (grant, error) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return grant{}, fmt.Errorf("unexpected object type %T", obj)
	}
	key, err := cache.MetaNamespaceKeyFunc(u)
	if err != nil {
		return grant{}, err
	}
	demandName, found, err := unstructured.NestedString(u.Object, "spec", "demandRef", "name")
	if err != nil || !found || demandName == "" {
		return grant{}, fmt.Errorf("%s has no spec.demandRef.name", key)
	}
	demandUID, _, _ := unstructured.NestedString(u.Object, "spec", "demandRef", "uid")
	taskUID, found, err := unstructured.NestedString(u.Object, "spec", "taskRef", "uid")
	if err != nil || !found || taskUID == "" {
		return grant{}, fmt.Errorf("%s has no spec.taskRef.uid", key)
	}
	schedulerName, _, _ := unstructured.NestedString(u.Object, "spec", "schedulerName")
	if schedulerName != SchedulerName {
		return grant{}, fmt.Errorf("%s targets scheduler %q", key, schedulerName)
	}
	revision, found, err := unstructured.NestedInt64(u.Object, "spec", "revision")
	if err != nil || !found {
		return grant{}, fmt.Errorf("%s has no integer spec.revision", key)
	}
	validUntilRaw, found, err := unstructured.NestedString(u.Object, "spec", "validUntil")
	if err != nil || !found {
		return grant{}, fmt.Errorf("%s has no spec.validUntil", key)
	}
	validUntil, err := time.Parse(time.RFC3339, validUntilRaw)
	if err != nil {
		return grant{}, fmt.Errorf("%s has invalid validUntil: %w", key, err)
	}
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	activeGroupState, _, _ := unstructured.NestedString(u.Object, "status", "activeGroupState")
	observedGeneration, _, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	observedRevision, _, _ := unstructured.NestedInt64(u.Object, "status", "observedRevision")
	activeGroupID, found, err := unstructured.NestedString(u.Object, "spec", "activeGroupRef", "groupId")
	if err != nil || !found || activeGroupID == "" {
		return grant{}, fmt.Errorf("%s has no spec.activeGroupRef.groupId", key)
	}
	activeGroupRank, found, err := unstructured.NestedInt64(u.Object, "spec", "activeGroupRef", "rank")
	if err != nil || !found || activeGroupRank < 1 || activeGroupRank > 3 {
		return grant{}, fmt.Errorf("%s has invalid spec.activeGroupRef.rank", key)
	}
	groupItems, found, err := unstructured.NestedSlice(u.Object, "spec", "candidateNodeGroups")
	if err != nil || !found || len(groupItems) == 0 || len(groupItems) > 3 {
		return grant{}, fmt.Errorf("%s has invalid spec.candidateNodeGroups", key)
	}
	var nodeItems []interface{}
	for _, item := range groupItems {
		group, ok := item.(map[string]interface{})
		if !ok {
			return grant{}, fmt.Errorf("%s contains malformed candidate group", key)
		}
		groupID, _ := group["groupId"].(string)
		rank, _ := group["rank"].(int64)
		if groupID == activeGroupID && rank == activeGroupRank {
			var nodesOK bool
			nodeItems, nodesOK = group["nodes"].([]interface{})
			if !nodesOK {
				return grant{}, fmt.Errorf("%s active group contains malformed nodes", key)
			}
			break
		}
	}
	if len(nodeItems) == 0 {
		return grant{}, fmt.Errorf("%s activeGroupRef does not resolve to a candidate group", key)
	}
	nodes := map[string]types.UID{}
	for _, item := range nodeItems {
		node, ok := item.(map[string]interface{})
		if !ok {
			return grant{}, fmt.Errorf("%s contains malformed node entry", key)
		}
		name, _ := node["name"].(string)
		uid, _ := node["uid"].(string)
		if name == "" || uid == "" {
			return grant{}, fmt.Errorf("%s contains node without name or uid", key)
		}
		nodes[name] = types.UID(uid)
	}
	return grant{
		objectKey:          key,
		namespace:          u.GetNamespace(),
		demandName:         demandName,
		demandUID:          types.UID(demandUID),
		taskUID:            types.UID(taskUID),
		revision:           revision,
		generation:         u.GetGeneration(),
		observedRevision:   observedRevision,
		observedGeneration: observedGeneration,
		phase:              phase,
		activeGroupState:   activeGroupState,
		activeGroupID:      activeGroupID,
		activeGroupRank:    activeGroupRank,
		validUntil:         validUntil,
		nodes:              nodes,
	}, nil
}

var (
	globalStoreOnce sync.Once
	globalStore     *grantStore
)

func processGrantStore() *grantStore {
	globalStoreOnce.Do(func() {
		globalStore = newGrantStore()
		go globalStore.run()
	})
	return globalStore
}

type plugin struct {
	store *grantStore
}

func New(_ framework.Arguments) framework.Plugin {
	return &plugin{store: processGrantStore()}
}

func (p *plugin) Name() string { return PluginName }

func (p *plugin) OnSessionOpen(ssn *framework.Session) {
	snapshot := p.store.snapshot(time.Now())
	klog.V(4).Infof("plugin=%s session snapshot ready=%t grants=%d error=%q", PluginName, snapshot.ready, len(snapshot.byDemand), snapshot.errorText)
	ssn.AddPredicateFn(p.Name(), func(task *api.TaskInfo, node *api.NodeInfo) error {
		return evaluate(snapshot, task, node)
	})
}

func (p *plugin) OnSessionClose(_ *framework.Session) {}

func evaluate(snapshot storeSnapshot, task *api.TaskInfo, node *api.NodeInfo) error {
	if task == nil || task.Pod == nil {
		return fmt.Errorf("plugin=%s task has no Pod", PluginName)
	}
	demandName := task.Pod.Annotations[DemandAnnotation]
	if demandName == "" {
		// The plugin is opt-in: unmanaged Pods retain normal Volcano behavior.
		return nil
	}
	if !snapshot.ready {
		return fmt.Errorf("NGG cache is not ready: %s", snapshot.errorText)
	}
	g, found := snapshot.byDemand[task.Namespace+"/"+demandName]
	if !found {
		return fmt.Errorf("no active NGG for demand %s/%s", task.Namespace, demandName)
	}
	if task.Pod.Spec.SchedulerName != SchedulerName {
		return fmt.Errorf("Pod schedulerName %q does not match NGG scheduler %q", task.Pod.Spec.SchedulerName, SchedulerName)
	}
	if !ownedBy(task.Pod, g.taskUID) {
		return fmt.Errorf("Pod is not owned by NGG task UID %s", g.taskUID)
	}
	if node == nil || node.Node == nil {
		return fmt.Errorf("candidate Node is unavailable")
	}
	expectedUID, found := g.nodes[node.Name]
	if !found {
		return fmt.Errorf("Node %s is outside NGG revision %d", node.Name, g.revision)
	}
	if node.Node.UID != expectedUID {
		return fmt.Errorf("Node %s UID does not match NGG", node.Name)
	}
	return nil
}

func ownedBy(pod *corev1.Pod, uid types.UID) bool {
	for _, owner := range pod.OwnerReferences {
		if owner.UID == uid {
			return true
		}
	}
	return false
}
