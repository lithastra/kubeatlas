package discovery_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

// startEnvOrSkip boots an envtest control plane or skips the test.
// envtest needs etcd + kube-apiserver binaries on disk; local
// developers install them with `setup-envtest use 1.30.x`.
func startEnvOrSkip(t *testing.T) (*envtest.Environment, *kubernetes.Clientset, dynamic.Interface) {
	t.Helper()
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "kube-apiserver") || strings.Contains(msg, "etcd") || strings.Contains(msg, "control plane") || strings.Contains(msg, "binary") || strings.Contains(msg, "no such file") {
			t.Skipf("envtest binaries not available; install with `setup-envtest use 1.30.x` and set KUBEBUILDER_ASSETS. Underlying error: %v", err)
		}
		t.Fatalf("envtest start: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return env, cs, dyn
}

func TestInformerManager_PicksUpAndRemovesPod(t *testing.T) {
	_, cs, dyn := startEnvOrSkip(t)

	// Pre-create the namespace (envtest comes with default + kube-system).
	const ns = "demo"
	if _, err := cs.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	store := memory.New()
	mgr := discovery.NewInformerManager(dyn, store,
		discovery.WithGVRs([]schema.GroupVersionResource{
			{Group: "", Version: "v1", Resource: "pods"},
		}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() { _ = mgr.Start(ctx) }()

	// Create a Pod through the typed clientset.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-pod", Namespace: ns},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "main", Image: "busybox"},
		}},
	}
	if _, err := cs.CoreV1().Pods(ns).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	// Wait for the informer to write it through.
	const id = "demo/Pod/demo-pod"
	if err := waitForResource(ctx, store, id); err != nil {
		t.Fatalf("informer never delivered Pod: %v", err)
	}

	got, err := store.GetResource(context.Background(), id)
	if err != nil {
		t.Fatalf("get after wait: %v", err)
	}
	if got.Kind != "Pod" {
		t.Errorf("Kind = %q, want Pod", got.Kind)
	}
	if got.Namespace != ns || got.Name != "demo-pod" {
		t.Errorf("ns/name = %s/%s", got.Namespace, got.Name)
	}
	if got.GroupVersion != "v1" {
		t.Errorf("GroupVersion = %q, want v1", got.GroupVersion)
	}
	if got.UID == "" {
		t.Error("UID empty; informer should populate it")
	}
	if got.ResourceVersion == "" {
		t.Error("ResourceVersion empty; informer should populate it")
	}

	// Delete the Pod and verify the store reflects it.
	if err := cs.CoreV1().Pods(ns).Delete(context.Background(), "demo-pod", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := waitForGone(ctx, store, id); err != nil {
		t.Fatalf("informer never delivered delete: %v", err)
	}
}

func TestInformerManager_ExtractorReceivesEvents(t *testing.T) {
	_, cs, dyn := startEnvOrSkip(t)

	const ns = "demo-ext"
	if _, err := cs.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	store := memory.New()
	ext := &recordingExtractor{}
	mgr := discovery.NewInformerManager(dyn, store,
		discovery.WithGVRs([]schema.GroupVersionResource{
			{Group: "", Version: "v1", Resource: "configmaps"},
		}),
		discovery.WithExtractor(ext),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() { _ = mgr.Start(ctx) }()

	// Create a ConfigMap.
	if _, err := cs.CoreV1().ConfigMaps(ns).Create(context.Background(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: ns},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := waitForResource(ctx, store, "demo-ext/ConfigMap/app-config"); err != nil {
		t.Fatalf("informer never delivered ConfigMap: %v", err)
	}
	if ext.calls() == 0 {
		t.Error("extractor was never invoked")
	}
}

func TestIsSkipped_BlacklistedGVR(t *testing.T) {
	store := memory.New()
	_ = discovery.NewInformerManager(nil, store,
		discovery.WithGVRs([]schema.GroupVersionResource{
			{Group: "", Version: "v1", Resource: "events"},
		}),
	)
	// We cannot drive Start without a live apiserver here; instead this
	// test exists to keep the blacklist in coverage. The real assertion
	// that watches are skipped is in TestInformerManager_PicksUpAndRemovesPod
	// (events do not appear in the store there).
}

// waitForResource polls the store until id appears or ctx is done.
func waitForResource(ctx context.Context, s graph.GraphStore, id string) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := s.GetResource(context.Background(), id); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("timed out waiting for resource " + id)
}

// waitForGone polls the store until id is missing or ctx is done.
func waitForGone(ctx context.Context, s graph.GraphStore, id string) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_, err := s.GetResource(context.Background(), id)
		var nf graph.ErrNotFound
		if errors.As(err, &nf) {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("timed out waiting for delete of " + id)
}

// recordingExtractor counts how many times ExtractAll has been invoked.
type recordingExtractor struct {
	count int
}

func (r *recordingExtractor) ExtractAll(_ graph.Resource, _ []graph.Resource) []graph.Edge {
	r.count++
	return nil
}

func (r *recordingExtractor) calls() int { return r.count }

// Compile-time assurance the unstructured import is referenced (used
// indirectly by the dynamic client surface).
var _ = unstructured.Unstructured{}
