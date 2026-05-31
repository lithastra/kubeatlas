package discovery_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/lithastra/kubeatlas/pkg/discovery"
)

var configMapGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}

// TestDynamicInformerManager_AddBeforeStart verifies the not-started
// guard without needing a control plane.
func TestDynamicInformerManager_AddBeforeStart(t *testing.T) {
	m := discovery.NewDynamicInformerManager(nil)
	err := m.Add(configMapGVR, cache.ResourceEventHandlerFuncs{})
	if err != discovery.ErrManagerNotStarted {
		t.Fatalf("Add before Start: err = %v, want ErrManagerNotStarted", err)
	}
}

// TestDynamicInformerManager_AddRemoveLifecycle drives the full path:
// Add starts an informer that delivers events, idempotent Add is a
// no-op, and Remove stops tracking. Metrics track the active gauge.
func TestDynamicInformerManager_AddRemoveLifecycle(t *testing.T) {
	_, cs, dyn := startEnvOrSkip(t)

	metrics := discovery.NewDynamicMetrics()
	m := discovery.NewDynamicInformerManager(dyn, discovery.WithDynamicMetrics(metrics))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() { _ = m.Start(ctx) }()

	var added atomic.Int64
	handler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(_ any) { added.Add(1) },
	}
	// Start binds the base context asynchronously; retry the real Add
	// (with the real handler) until it takes. The first successful call
	// registers the informer; idempotency makes the retry safe.
	waitUntil(t, 5*time.Second, "manager started and informer registered", func() bool {
		return m.Add(configMapGVR, handler) == nil
	})
	// Idempotent: a second Add for the same GVR is a no-op.
	if err := m.Add(configMapGVR, handler); err != nil {
		t.Fatalf("second Add: %v", err)
	}
	if got := m.ActiveGVRs(); len(got) != 1 {
		t.Fatalf("ActiveGVRs = %v, want exactly one", got)
	}
	if !m.Has(configMapGVR) {
		t.Fatal("Has(configmaps) = false, want true")
	}
	if got := metrics.Snapshot().Active; got != 1 {
		t.Errorf("metrics active = %d, want 1", got)
	}

	// Create a ConfigMap and confirm the informer delivers it.
	if _, err := cs.CoreV1().ConfigMaps("default").Create(context.Background(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "default"},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 15*time.Second, "informer delivered the ConfigMap", func() bool {
		return added.Load() > 0
	})

	// Remove stops tracking and drops the gauge.
	m.Remove(configMapGVR)
	if m.Has(configMapGVR) {
		t.Error("Has(configmaps) = true after Remove, want false")
	}
	if got := len(m.ActiveGVRs()); got != 0 {
		t.Errorf("ActiveGVRs len = %d after Remove, want 0", got)
	}
	if got := metrics.Snapshot().Active; got != 0 {
		t.Errorf("metrics active = %d after Remove, want 0", got)
	}
	// Remove is idempotent.
	m.Remove(configMapGVR)
}

func waitUntil(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for: %s", timeout, what)
}
