package controller

import (
	"testing"
	"time"
)

func TestRefreshControllerOptionsPreserveConfiguredValues(t *testing.T) {
	reconciler := &RefreshReconciler{
		RefreshInterval: 45 * time.Second,
		MaxConcurrent:   12,
	}

	options := reconciler.controllerOptions()
	if reconciler.RefreshInterval != 45*time.Second {
		t.Fatalf("refresh interval changed: %s", reconciler.RefreshInterval)
	}
	if options.MaxConcurrentReconciles != 12 || reconciler.MaxConcurrent != 12 {
		t.Fatalf("worker setting did not reach controller-runtime options: options=%d reconciler=%d",
			options.MaxConcurrentReconciles, reconciler.MaxConcurrent)
	}
}

func TestRefreshControllerOptionsApplyBackwardCompatibleDefaults(t *testing.T) {
	reconciler := &RefreshReconciler{}

	options := reconciler.controllerOptions()
	if reconciler.RefreshInterval != 15*time.Second {
		t.Fatalf("unexpected refresh default: %s", reconciler.RefreshInterval)
	}
	if options.MaxConcurrentReconciles != 5 || reconciler.MaxConcurrent != 5 {
		t.Fatalf("unexpected worker default: options=%d reconciler=%d",
			options.MaxConcurrentReconciles, reconciler.MaxConcurrent)
	}
}
