package common

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	extensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

// TestEnvironment持有本组独立的API Server和etcd生命周期。
type TestEnvironment struct {
	Environment *envtest.Environment
	Config      *rest.Config
	Scheme      *runtime.Scheme
}

// StartEnvTest安装联通正式NGD/NGG CRD。
func StartEnvTest(t *testing.T) *TestEnvironment {
	t.Helper()
	ctrl.SetLogger(logr.Discard())
	root := ProjectRoot()
	paths := []string{
		filepath.Join(root, "docs", "paas-schedbridge-master-new", "crd-deploy", "nodegroupdemand-crd.yaml"),
		filepath.Join(root, "docs", "paas-schedbridge-master-new", "crd-deploy", "nodegroupgrant-crd.yaml"),
	}
	crds := make([]*extensionsv1.CustomResourceDefinition, 0, len(paths))
	for _, path := range paths {
		crd, err := readCRD(path)
		if err != nil {
			t.Fatalf("read CRD %s: %v", path, err)
		}
		crds = append(crds, crd)
	}
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		assets = filepath.Join(root, ".cache", "envtest", "1.35.5")
	}
	environment := &envtest.Environment{CRDs: crds, BinaryAssetsDirectory: assets}
	config, err := environment.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	return &TestEnvironment{Environment: environment, Config: config, Scheme: scheme}
}

func readCRD(path string) (*extensionsv1.CustomResourceDefinition, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	jsonRaw, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return nil, err
	}
	var crd extensionsv1.CustomResourceDefinition
	if err := json.Unmarshal(jsonRaw, &crd); err != nil {
		return nil, err
	}
	if crd.Name == "" {
		return nil, fmt.Errorf("file does not contain a CRD")
	}
	return &crd, nil
}

func (e *TestEnvironment) Stop(t *testing.T) {
	t.Helper()
	if err := e.Environment.Stop(); err != nil {
		t.Errorf("stop envtest: %v", err)
	}
}

// CreateKubernetesInputs在计时前创建1000个Node和Pod，并通过Status子资源写入状态。
func CreateKubernetesInputs(ctx context.Context, apiClient client.Client, fixture *Fixture) error {
	if err := apiClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ngd-ngg-test"}}); err != nil {
		return fmt.Errorf("create test Namespace: %w", err)
	}
	for index := range fixture.Nodes {
		source := fixture.Nodes[index].DeepCopy()
		status := source.Status.DeepCopy()
		source.UID = ""
		source.ResourceVersion = ""
		source.CreationTimestamp = metav1Zero()
		source.Status = corev1.NodeStatus{}
		if err := apiClient.Create(ctx, source); err != nil {
			return fmt.Errorf("create Node %s: %w", source.Name, err)
		}
		source.Status = *status
		if err := apiClient.Status().Update(ctx, source); err != nil {
			return fmt.Errorf("update Node %s status: %w", source.Name, err)
		}
	}
	for index := range fixture.Pods {
		source := fixture.Pods[index].DeepCopy()
		status := source.Status.DeepCopy()
		source.UID = ""
		source.ResourceVersion = ""
		source.CreationTimestamp = metav1Zero()
		source.Status = corev1.PodStatus{}
		if err := apiClient.Create(ctx, source); err != nil {
			return fmt.Errorf("create Pod %s: %w", source.Name, err)
		}
		source.Status = *status
		if err := apiClient.Status().Update(ctx, source); err != nil {
			return fmt.Errorf("update Pod %s status: %w", source.Name, err)
		}
	}
	return nil
}

func metav1Zero() metav1.Time { return metav1.Time{} }

// Eventually轮询真实API状态，超时后返回最后一次错误。
func Eventually(ctx context.Context, interval time.Duration, condition func(context.Context) (bool, error)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastErr error
	for {
		ok, err := condition(ctx)
		if err != nil {
			lastErr = err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("timeout: %w; last error: %v", ctx.Err(), lastErr)
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
