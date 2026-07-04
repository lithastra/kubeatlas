package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"log/slog"

	kdiscovery "k8s.io/client-go/discovery"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/api"
	"github.com/lithastra/kubeatlas/pkg/collect"
	"github.com/lithastra/kubeatlas/pkg/crd"
	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/extractor/rego"
	"github.com/lithastra/kubeatlas/pkg/gatekeeper"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/multicluster"
	"github.com/lithastra/kubeatlas/pkg/otel"
	"github.com/lithastra/kubeatlas/pkg/snapshot"
	"github.com/lithastra/kubeatlas/pkg/store"
	"github.com/lithastra/kubeatlas/pkg/store/postgres"
	"github.com/lithastra/kubeatlas/pkg/telemetry"
	"github.com/lithastra/kubeatlas/pkg/version"
)

// componentStarter is the lifecycle shape every long-running runWatch
// component shares: Start(ctx) blocks until ctx is cancelled, returns
// ctx.Err() or an early failure. Both discovery.InformerManager and
// crd.Discovery already satisfy it; runWatch's multi-cluster branch
// swaps in adapters that do too.
type componentStarter interface {
	Start(ctx context.Context) error
}

// multiclusterStarter adapts a multicluster.Manager to componentStarter
// (P3-T21). It attaches every cluster from kubeconfigs on Start, then
// blocks on ctx so the existing 3-component result-loop pattern keeps
// working. Failure to attach EVERY cluster is the only fatal: a single
// bad kubeconfig is logged and the rest still run.
type multiclusterStarter struct {
	mgr         *multicluster.Manager
	kubeconfigs map[string][]byte
	onReady     func()
}

func (s *multiclusterStarter) Start(ctx context.Context) error {
	failures := s.mgr.AddFromSecret(ctx, s.kubeconfigs)
	attached := s.mgr.ListClusters()
	if len(attached) == 0 {
		return fmt.Errorf("multicluster: every member cluster failed to attach (%d failures)", len(failures))
	}
	slog.Info("multicluster manager started",
		"attached", attached, "failures", len(failures))
	if s.onReady != nil {
		s.onReady()
	}
	<-ctx.Done()
	s.mgr.Stop()
	return ctx.Err()
}

// noopStarter parks until ctx is cancelled. Used in place of
// crd.Discovery in multi-cluster mode, where per-cluster CRD discovery
// is deferred to a future task (P3-T21 focus: core GVR informers).
type noopStarter struct{}

func (noopStarter) Start(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// loadMulticlusterKubeconfigs reads every regular file under dir as a
// kubeconfig and returns map[clusterName]kubeconfig-bytes. The file's
// basename is the cluster name. K8s Secret mounts use dot-prefixed
// symlinks (..data, ..2026_05_20_...) for atomic rotation; those are
// skipped.
func loadMulticlusterKubeconfigs(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read multicluster dir %q: %w", dir, err)
	}
	out := make(map[string][]byte)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", name, err)
		}
		out[name] = data
	}
	return out, nil
}

// buildRegoEngine constructs the rego engine + supporting router /
// cache / metrics and loads the rule packs we ship at compile time
// (the embedded OpenShift pack via the rulePacks.openshift mode
// resolver) plus any extras the operator passed in via --rule-pack
// flags or the comma-separated KUBEATLAS_RULE_PACKS env var (Helm
// chart writes the latter from rulePacks.extras).
//
// Pack-load failures are logged at warn and the offending pack is
// skipped — anti-pattern #35: one bad pack must not kill boot.
func buildRegoEngine(ctx context.Context, disc kdiscoveryClient, extras []string) (*rego.Engine, error) {
	metrics := rego.NewMetrics()
	cache, err := rego.NewCache(0, metrics)
	if err != nil {
		return nil, fmt.Errorf("build rego cache: %w", err)
	}

	mode, err := crd.ParseRulePackMode(os.Getenv("KUBEATLAS_RULEPACK_OPENSHIFT"))
	if err != nil {
		return nil, fmt.Errorf("KUBEATLAS_RULEPACK_OPENSHIFT: %w", err)
	}
	load, detectErr := crd.ResolveOpenShiftLoad(mode, disc)
	if detectErr != nil {
		slog.Warn("openshift detector failed; assuming non-openshift",
			"mode", mode, "err", detectErr)
	}

	var packs []*rego.RulePack
	switch {
	case load:
		pack, err := rego.EmbeddedOpenShift()
		if err != nil {
			slog.Warn("embedded openshift pack failed to load; continuing without it",
				"err", err)
		} else {
			packs = append(packs, pack)
			slog.Info("OpenShift API group detected, loading openshift rule pack",
				"version", pack.Version, "modules", len(pack.Modules))
		}
	case mode == crd.RulePackModeAuto:
		slog.Info("No OpenShift detected, openshift rule pack not loaded")
	default:
		slog.Info("openshift rule pack disabled by config", "mode", mode)
	}

	ociOpts, err := loadOCIVerifyConfig()
	if err != nil {
		return nil, err
	}

	for _, ref := range extras {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		pack, err := loadExtraPack(ctx, ref, ociOpts...)
		if err != nil {
			// A signature-verification failure is FATAL — invariant
			// 2.9: an unverified pack must abort startup, never be
			// warned-and-skipped like an ordinary load error.
			// "Failed but continued" equals "not verified".
			if errors.Is(err, rego.ErrSignatureVerification) {
				return nil, fmt.Errorf("rule pack %s: %w", ref, err)
			}
			slog.Warn("extra rule pack failed to load; skipping",
				"ref", ref, "err", err)
			continue
		}
		packs = append(packs, pack)
		slog.Info("Loaded extra rule pack",
			"ref", ref, "name", pack.Name, "version", pack.Version,
			"modules", len(pack.Modules))
	}

	router := rego.FromRulePacks(packs...)
	engine := rego.New(
		rego.WithRouter(router),
		rego.WithCache(cache),
		rego.WithMetrics(metrics),
	)
	for _, p := range packs {
		if err := p.RegisterTo(ctx, engine); err != nil {
			slog.Warn("rule pack register failed; skipping",
				"pack", p.Name, "err", err)
		}
	}
	return engine, nil
}

// loadExtraPack picks the right loader for a --rule-pack value.
// "oci://...:tag" or "<host>/<repo>:tag" go through the OCI puller;
// anything else is treated as a directory path. Future schemes
// (file://, https:// for an unsigned tarball) plug in here.
//
// ociOpts carries the P3-T-COS signature-verification settings; they
// apply only to the OCI paths (a local directory pack has no
// signature to verify).
func loadExtraPack(ctx context.Context, ref string, ociOpts ...rego.OCIOption) (*rego.RulePack, error) {
	switch {
	case strings.HasPrefix(ref, "oci://"):
		return rego.LoadRulePackFromOCI(ctx, ref, ociOpts...)
	case strings.Contains(ref, ":") && strings.Contains(ref, "/"):
		// Bare registry/repo:tag form, e.g.
		// ghcr.io/lithastra/rules/openshift:0.1.0. Heuristic
		// distinguishes it from a Windows-style path because we
		// run linux/darwin only.
		return rego.LoadRulePackFromOCI(ctx, ref, ociOpts...)
	default:
		return rego.LoadRulePackFromDir(ref)
	}
}

// loadOCIVerifyConfig reads the P3-T-COS rule-pack signature settings
// from the environment. The Helm chart writes these from
// rulePacks.verifySignature / rulePacks.trustedIdentities.
//
// Recognized env vars:
//
//	KUBEATLAS_RULEPACK_VERIFY_SIGNATURE     "true" turns verification on
//	KUBEATLAS_RULEPACK_TRUSTED_IDENTITIES   JSON array of TrustPolicy
//
// A malformed trusted-identities JSON is fatal: a typo'd Helm value
// must fail loudly at startup, not silently fall back to "trust
// nothing" (or, worse, "trust everything").
func loadOCIVerifyConfig() ([]rego.OCIOption, error) {
	verify := os.Getenv("KUBEATLAS_RULEPACK_VERIFY_SIGNATURE") == "true"
	opts := []rego.OCIOption{rego.WithSignatureVerification(verify)}

	if raw := strings.TrimSpace(os.Getenv("KUBEATLAS_RULEPACK_TRUSTED_IDENTITIES")); raw != "" {
		var ids []rego.TrustPolicy
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			return nil, fmt.Errorf("KUBEATLAS_RULEPACK_TRUSTED_IDENTITIES: %w", err)
		}
		opts = append(opts, rego.WithTrustedIdentities(ids...))
	}
	return opts, nil
}

// rulePackRefs assembles the operator's rule-pack refs from both
// the repeated --rule-pack flag and the KUBEATLAS_RULE_PACKS env
// var (comma-separated). Both are unioned; duplicates are kept
// because dedup is the operator's responsibility, not ours.
func rulePackRefs(flagValues []string) []string {
	var out []string
	for _, v := range flagValues {
		if v != "" {
			out = append(out, v)
		}
	}
	if envVal := os.Getenv("KUBEATLAS_RULE_PACKS"); envVal != "" {
		for _, p := range strings.Split(envVal, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// rulePackFlag implements flag.Value so --rule-pack can be passed
// repeatedly: each invocation appends.
type rulePackFlag []string

func (r *rulePackFlag) String() string     { return strings.Join(*r, ",") }
func (r *rulePackFlag) Set(s string) error { *r = append(*r, s); return nil }

// kdiscoveryClient is the slice of k8s.io/client-go/discovery the
// rego bootstrap needs. Aliasing the upstream interface keeps
// buildRegoEngine's signature precise without growing main.go's
// already-busy import list.
type kdiscoveryClient = kdiscovery.DiscoveryInterface

// loadStoreConfig builds a store.Config from the process environment.
// The Helm chart sets KUBEATLAS_BACKEND when persistence.enabled is
// true; the Tier 1 default (empty / "memory") preserves the
// zero-config promise (guide §2.3, anti-pattern #10).
//
// Recognized env vars:
//
//	KUBEATLAS_BACKEND  "memory" (default) | "postgres"
//	PGCONN             full DSN, e.g. postgres://user:pass@host:5432/db?sslmode=disable
//
// Future Phase 2 work (P2-T6 CNPG integration) will refine PGCONN
// into individual fields sourced from a Kubernetes Secret.
func loadStoreConfig() store.Config {
	cfg := store.Config{Backend: store.Backend(os.Getenv("KUBEATLAS_BACKEND"))}
	if cfg.Backend == store.BackendPostgres {
		cfg.Postgres = postgres.Config{DSN: os.Getenv("PGCONN")}
	}
	return cfg
}

// loadSnapshotConfig reads the F-111 snapshot settings from the
// environment. The Helm chart sets these when snapshots.enabled is
// true; the schema in values.schema.json rejects the
// enabled-without-persistence combination so a Tier 1 install never
// reaches this code with enabled=true (invariant 2.2).
//
// Recognized env vars:
//
//	KUBEATLAS_SNAPSHOTS_ENABLED     "true" enables the writer
//	KUBEATLAS_SNAPSHOTS_QUEUE_SIZE  int; 0 / unset -> snapshot.DefaultQueueSize
//	KUBEATLAS_SNAPSHOTS_WORKERS     int; 0 / unset -> snapshot.DefaultWorkers
//	KUBEATLAS_SNAPSHOTS_RETENTION   "7d" / "24h" / ...; unset -> 7d
//
// A malformed retention string is fatal — a typo'd Helm value
// should fail loudly at startup, not silently default.
func loadSnapshotConfig() (enabled bool, cfg snapshot.Config, retention time.Duration) {
	enabled = os.Getenv("KUBEATLAS_SNAPSHOTS_ENABLED") == "true"
	if v, err := strconv.Atoi(os.Getenv("KUBEATLAS_SNAPSHOTS_QUEUE_SIZE")); err == nil {
		cfg.QueueSize = v
	}
	if v, err := strconv.Atoi(os.Getenv("KUBEATLAS_SNAPSHOTS_WORKERS")); err == nil {
		cfg.Workers = v
	}
	retention, err := snapshot.ParseRetention(os.Getenv("KUBEATLAS_SNAPSHOTS_RETENTION"))
	if err != nil {
		log.Fatalf("KUBEATLAS_SNAPSHOTS_RETENTION: %v", err)
	}
	return enabled, cfg, retention
}

func main() {
	// Subcommand dispatch — "rules-test" runs the offline rule pack
	// evaluator without touching kubeconfig or the API server,
	// "export" emits the cluster graph as DOT, and "snapshot"
	// drives the F-111 internal snapshot endpoint. All live before
	// flag.Parse so each subcommand can carry its own flag set.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "rules-test":
			os.Exit(runRulesTest(os.Args[2:]))
		case "export":
			os.Exit(runExport(os.Args[2:]))
		case "snapshot":
			os.Exit(runSnapshot(os.Args[2:]))
		case "diagnose":
			os.Exit(runDiagnose(os.Args[2:]))
		}
	}

	var rulePacks rulePackFlag
	var (
		once        = flag.Bool("once", false, "Run a single offline discovery pass (talks to the K8s API directly, no kubeatlas server) and exit.")
		format      = flag.String("format", "json", "Output format for -once: json (default) | dot | svg.")
		level       = flag.String("level", "resource", "Aggregation level: resource | namespace | workload | cluster.")
		namespace   = flag.String("namespace", "", "Filter by namespace (required for namespace/workload, optional for resource).")
		kind        = flag.String("kind", "", "Resource Kind (required for workload, and for resource when scoped to a single object).")
		name        = flag.String("name", "", "Resource name (required for workload, and for resource when scoped to a single object).")
		kubeconfig  = flag.String("kubeconfig", "", "Path to the kubeconfig file (local runs; overrides $KUBECONFIG).")
		kubeContext = flag.String("context", "", "kubeconfig context to use (local runs).")
		showVersion = flag.Bool("version", false, "Print build metadata (version, commit, date) and exit.")
	)
	flag.Var(&rulePacks, "rule-pack",
		"Extra rule pack to load (OCI ref like oci://ghcr.io/lithastra/rules/<pack>:<version> "+
			"or local directory). Repeatable; merged with the comma-separated "+
			"KUBEATLAS_RULE_PACKS env var the Helm chart writes.")
	flag.Parse()

	if *showVersion {
		fmt.Printf("kubeatlas %s (commit %s, built %s)\n", version.Version, version.Commit, version.Date)
		return
	}
	if *once {
		runOnce(*level, *namespace, *kind, *name, *format, *kubeconfig, *kubeContext)
		return
	}
	runWatch(rulePackRefs(rulePacks), *kubeconfig, *kubeContext)
}

// runOnce is the offline CLI mode: it walks every API resource
// through the kubeconfig — no running kubeatlas server needed —
// extracts edges, and emits the graph in the requested format.
//
// format selects the output:
//   - json — an aggregated View per -level (cluster / namespace /
//     workload), a single-resource one-hop view (level=resource with
//     -kind + -name), or the raw graph (level=resource, unscoped).
//   - dot  — the raw resource graph as Graphviz DOT.
//   - svg  — the raw resource graph rendered to SVG via graphviz.
//
// dot and svg render the resource graph, optionally narrowed to one
// -namespace; the -level aggregation applies to json only.
func runOnce(level, namespace, kind, name, format, kubeconfig, kubeContext string) {
	switch format {
	case "json", "dot", "svg":
	default:
		log.Fatalf("unknown -format=%q (want json | dot | svg)", format)
	}

	ctx := context.Background()

	graphStore, err := store.New(ctx, loadStoreConfig())
	if err != nil {
		log.Fatalf("failed to construct graph store: %v", err)
	}
	// collect.Cluster runs the offline scan — collect every resource
	// through the kubeconfig and derive the built-in edges into the
	// store — the same code path the kubectl-atlas plugin uses.
	if err := collect.Cluster(ctx, graphStore, kubeconfig, kubeContext); err != nil {
		log.Fatalf("offline scan: %v", err)
	}

	// dot / svg render the raw resource graph (optionally narrowed to
	// one -namespace) and stream it to stdout. The -level aggregation
	// applies to the json format only.
	if format == "dot" || format == "svg" {
		g, err := graphStore.Snapshot(ctx)
		if err != nil {
			log.Fatalf("failed to snapshot store: %v", err)
		}
		opts := graph.DOTOptions{Namespace: namespace}
		if format == "dot" {
			fmt.Print(graph.ToDOTOptions(g, opts))
			return
		}
		svg, err := graph.ToSVG(ctx, g, opts)
		if err != nil {
			if errors.Is(err, graph.ErrGraphvizNotFound) {
				log.Fatal("-format=svg needs the graphviz 'dot' binary on PATH; install the graphviz package")
			}
			log.Fatalf("render svg: %v", err)
		}
		if _, err := os.Stdout.Write(svg); err != nil {
			log.Fatalf("write svg: %v", err)
		}
		return
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	switch level {
	case "resource":
		// Two modes: scoped (kind+name+namespace given) → single-resource
		// one-hop view; unscoped → legacy raw graph dump + DOT artefact.
		if kind != "" || name != "" {
			if namespace == "" || kind == "" || name == "" {
				log.Fatal("-level=resource scoped mode requires -namespace, -kind, and -name")
			}
			view, err := (aggregator.ResourceAggregator{}).Aggregate(ctx, graphStore,
				aggregator.Scope{Namespace: namespace, Kind: kind, Name: name})
			if err != nil {
				log.Fatalf("resource aggregator: %v", err)
			}
			if err := enc.Encode(view); err != nil {
				log.Fatalf("failed to encode JSON: %v", err)
			}
			return
		}
		g, err := graphStore.Snapshot(ctx)
		if err != nil {
			log.Fatalf("failed to snapshot store: %v", err)
		}
		if err := enc.Encode(g); err != nil {
			log.Fatalf("failed to encode JSON: %v", err)
		}

	case "cluster":
		view, err := (aggregator.ClusterAggregator{}).Aggregate(ctx, graphStore, aggregator.Scope{})
		if err != nil {
			log.Fatalf("cluster aggregator: %v", err)
		}
		if err := enc.Encode(view); err != nil {
			log.Fatalf("failed to encode JSON: %v", err)
		}

	case "namespace":
		if namespace == "" {
			log.Fatal("-level=namespace requires -namespace=<name>")
		}
		view, err := (aggregator.NamespaceAggregator{}).Aggregate(ctx, graphStore,
			aggregator.Scope{Namespace: namespace})
		if err != nil {
			log.Fatalf("namespace aggregator: %v", err)
		}
		if err := enc.Encode(view); err != nil {
			log.Fatalf("failed to encode JSON: %v", err)
		}

	case "workload":
		if namespace == "" || kind == "" || name == "" {
			log.Fatal("-level=workload requires -namespace, -kind, and -name")
		}
		view, err := (aggregator.WorkloadAggregator{}).Aggregate(ctx, graphStore,
			aggregator.Scope{Namespace: namespace, Kind: kind, Name: name})
		if err != nil {
			log.Fatalf("workload aggregator: %v", err)
		}
		if err := enc.Encode(view); err != nil {
			log.Fatalf("failed to encode JSON: %v", err)
		}

	default:
		log.Fatalf("unknown -level=%q (want resource | namespace | workload | cluster)", level)
	}
}

// loadedPackNames returns the distinct rule-pack names currently loaded
// in the engine, derived from each module's "pack/module" name. Used by
// telemetry to report which packs are enabled (names only, no versions
// or contents).
func loadedPackNames(engine *rego.Engine) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, m := range engine.Loaded() {
		name := m.Name
		if i := strings.IndexByte(name, '/'); i >= 0 {
			name = name[:i]
		}
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// runWatch starts the informer and the API server in parallel and
// blocks until either errors or the process receives SIGINT/SIGTERM.
// Both shut down when the parent context is cancelled.
func runWatch(rulePackExtras []string, kubeconfig, kubeContext string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	client, err := discovery.NewClient(kubeconfig, kubeContext)
	if err != nil {
		log.Fatalf("failed to load kubeconfig: %v", err)
	}
	disc := discovery.NewDiscoveryFromClient(client)
	gvrs, err := discovery.FilterAvailableGVRs(ctx, disc, discovery.CoreGVRs)
	if err != nil {
		log.Fatalf("filter GVRs: %v", err)
	}
	storeCfg := loadStoreConfig()
	graphStore, err := store.New(ctx, storeCfg)
	if err != nil {
		log.Fatalf("failed to construct graph store: %v", err)
	}

	// F-111 snapshot writer (P3-T3). Only started on Tier 2 with
	// snapshots explicitly enabled. The values.schema.json gate
	// rejects enabled-without-persistence, so reaching here with
	// enabled=true on a memory backend means a hand-set env var —
	// log and skip rather than spin up a writer behind the lossy
	// Tier 1 ring buffer (invariant 2.2).
	var snapWriter *snapshot.Writer
	// snapRetention is lifted out of the if-init scope: the API
	// server below also needs it (WithSnapshots caps the diff
	// window at the retention limit).
	snapEnabled, snapCfg, snapRetention := loadSnapshotConfig()
	if snapEnabled {
		if storeCfg.Backend != store.BackendPostgres {
			slog.Warn("KUBEATLAS_SNAPSHOTS_ENABLED set but backend is not postgres; " +
				"snapshots require Tier 2 — skipping snapshot writer (invariant 2.2)")
		} else {
			snapWriter = snapshot.New(graphStore, snapCfg, snapshot.NewMetrics())
			snapWriter.Start(ctx)
			slog.Info("snapshot writer started",
				"queueSize", snapCfg.QueueSize, "workers", snapCfg.Workers)

			// Retention worker: hourly prune of resource_events rows
			// older than the retention window. Best-effort background
			// sweep — runs in its own goroutine, stopped by ctx; not
			// one of the three critical components below.
			go snapshot.NewRetainer(graphStore, snapRetention).Start(ctx)
			slog.Info("snapshot retention worker started", "retention", snapRetention)
		}
	}

	// F-204 OTLP trace receiver. Like snapshots, it is Tier 2 only —
	// spans persist to a PostgreSQL table (invariant 2.2 / 2.5) — and
	// opt-in (otel.enabled, default false). otelStarter stays a
	// noopStarter when disabled or on Tier 1, so the component loop
	// below has nothing to listen on and no goroutine is spawned —
	// zero overhead when off. otelMetrics is nil unless the receiver
	// runs; the API server only surfaces the /metrics otel block then.
	otelCfg, err := otel.LoadConfig()
	if err != nil {
		log.Fatalf("otel config: %v", err)
	}
	var otelStarter componentStarter = noopStarter{}
	var otelMetrics *otel.Metrics
	var otelReader api.OtelReader
	if otelCfg.Enabled {
		if storeCfg.Backend != store.BackendPostgres {
			slog.Warn("KUBEATLAS_OTEL_ENABLED set but backend is not postgres; " +
				"span storage requires Tier 2 — skipping otel receiver (invariant 2.2)")
		} else if pgStore, ok := graphStore.(*postgres.Store); !ok {
			// store.New returns *postgres.Store on Tier 2; this only
			// fires if that contract ever changes — fail loud.
			log.Fatalf("otel: backend=postgres but store is %T, not *postgres.Store", graphStore)
		} else {
			otelMetrics = otel.NewMetrics()
			otelStarter = otel.NewReceiver(otelCfg.GRPCAddr, pgStore, otelCfg.BufferSize, otelMetrics)
			// Span retention: hourly prune of otel_spans older than the
			// window. Detached background sweep like the snapshot one.
			go otel.NewSpanRetainer(pgStore, otelCfg.Retention, otelMetrics).Start(ctx)
			// Correlator (F-204 part 2, P5-T5): a detached background job
			// that folds recent spans into CALLS_AT_RUNTIME overlay edges
			// in otel_runtime_edges. It reads spans (pgStore) and the
			// declarative resources (graphStore, read-only via
			// ResourceLister) and writes runtime edges (pgStore) — never
			// the critical path (invariant 2.5).
			go otel.NewCorrelator(pgStore, graphStore, pgStore, otelCfg.Retention, otelMetrics).Start(ctx)
			// Overlay read seam for /api/v1/otel/* (spans + runtime edges).
			otelReader = pgStore
			slog.Info("otel receiver started",
				"addr", otelCfg.GRPCAddr, "retention", otelCfg.Retention)
		}
	}

	// Phase 2 wire-up: the rego engine handles CRD-driven edge
	// derivation; the built-in extractor.Default() still owns core
	// K8s GVRs. The two pipelines write to the same store but never
	// race on the same resource — InformerManager covers the GVRs
	// in CoreGVRs, crd.Discovery covers everything else through the
	// dynamic CRD informer.
	regoEngine, err := buildRegoEngine(ctx, disc, rulePackExtras)
	if err != nil {
		log.Fatalf("build rego engine: %v", err)
	}

	// Shared dynamic-informer metrics — the gatekeeper component's
	// DynamicInformerManager updates them; /metrics surfaces them. Built
	// here so the API server and the manager reference the same sink.
	dynMetrics := discovery.NewDynamicMetrics()

	// Opt-in telemetry (default off). The collector reads coarse,
	// non-identifying values through closures so this package stays free
	// of store/rego deps. clusterCount is a reassignable var so the
	// multicluster branch below can update it; the closures capture the
	// variable, not its value.
	k8sVersionStr := ""
	if info, verr := disc.ServerVersion(); verr == nil {
		k8sVersionStr = info.GitVersion
	}
	telemetryClusterCount := func() int { return 1 }
	telemetryCollector := telemetry.NewCollector(version.Version, telemetry.Providers{
		K8sVersion: func() string { return k8sVersionStr },
		Tier: func() string {
			if storeCfg.Backend == store.BackendPostgres {
				return "postgres"
			}
			return "memory"
		},
		ResourceCount: func(ctx context.Context) (int, error) {
			counts, err := graphStore.CountKindsByNamespace(ctx, nil)
			if err != nil {
				return 0, err
			}
			total := 0
			for _, byKind := range counts {
				for _, n := range byKind {
					total += n
				}
			}
			return total, nil
		},
		EnabledPacks: func() []string { return loadedPackNames(regoEngine) },
		ClusterCount: func() int { return telemetryClusterCount() },
		Platforms:    func() map[string]int { return map[string]int{"vanilla": telemetryClusterCount()} },
	})
	telemetrySender := telemetry.NewSender(telemetry.LoadConfig(), telemetryCollector, slog.Default())

	// API server options. snapWriter is nil unless Tier 2 + snapshots
	// enabled; when set, /metrics surfaces its counters + queue depth.
	apiOpts := []api.ServerOption{
		api.WithWebFS(webFS),
		api.WithRegoMetrics(regoEngine.Metrics(), regoEngine.ModuleCount),
		api.WithDynamicInformerMetrics(dynMetrics),
		api.WithTelemetry(telemetrySender),
	}
	if snapWriter != nil {
		apiOpts = append(apiOpts,
			api.WithSnapshotMetrics(snapWriter.Metrics(), snapWriter.QueueDepth),
			api.WithSnapshots(snapRetention))
	}
	// otelMetrics is nil unless the receiver is running (Tier 2 +
	// otel.enabled). Appended to apiOpts so it survives the multi-
	// cluster api.New rebuild below, which reuses this same slice.
	if otelMetrics != nil {
		apiOpts = append(apiOpts, api.WithOtelReceiverMetrics(otelMetrics))
	}
	if otelReader != nil {
		apiOpts = append(apiOpts, api.WithOtelOverlay(otelReader))
	}
	// KUBEATLAS_API_ADDR overrides the default ":8080" listen address.
	// The Helm chart never sets it (the Pod always serves :8080 on the
	// PodSpec port); local perf / chaos harnesses set it to avoid
	// clashing with anything else already on the host.
	apiAddr := os.Getenv("KUBEATLAS_API_ADDR")
	if apiAddr == "" {
		apiAddr = api.DefaultAddr
	}
	srv := api.New(apiAddr, graphStore, aggregator.NewRegistry(), apiOpts...)

	// Informer options. WithOnSynced / WithBroadcaster depend on srv,
	// so this list is built after srv exists. The snapshot sink is
	// appended only when the writer is running.
	//
	// Multi-cluster mode (P3-T21) shares the same options across every
	// member cluster, except WithOnSynced — readiness fires once for
	// the federation, not once per member cluster, so the multi-cluster
	// branch wires it on the manager's lifecycle instead.
	baseInformerOpts := []discovery.InformerOption{
		discovery.WithGVRs(gvrs),
		discovery.WithExtractor(extractor.Default()),
		discovery.WithBroadcaster(srv.Hub().BroadcastEvent),
	}
	if snapWriter != nil {
		baseInformerOpts = append(baseInformerOpts, discovery.WithSnapshotSink(snapWriter))
	}

	// Build the multicluster.Manager ahead of api.New so it can be
	// passed in via WithClusterLister (P3-T22). The Manager is the
	// federation surface's source-of-truth list of attached clusters.
	var (
		informerStarter componentStarter
		crdStarter      componentStarter
		gkStarter       componentStarter
		mcMgr           *multicluster.Manager
		mcKubeconfigs   map[string][]byte
	)
	if os.Getenv("KUBEATLAS_MULTICLUSTER_ENABLED") == "true" {
		mcDir := os.Getenv("KUBEATLAS_MULTICLUSTER_KUBECONFIG_DIR")
		if mcDir == "" {
			log.Fatalf("multicluster: KUBEATLAS_MULTICLUSTER_ENABLED=true requires KUBEATLAS_MULTICLUSTER_KUBECONFIG_DIR")
		}
		kubeconfigs, err := loadMulticlusterKubeconfigs(mcDir)
		if err != nil {
			log.Fatalf("multicluster: %v", err)
		}
		if len(kubeconfigs) == 0 {
			log.Fatalf("multicluster: no kubeconfig files found in %s", mcDir)
		}
		mcKubeconfigs = kubeconfigs
		mcMgr = multicluster.New(graphStore,
			multicluster.WithFactory(multicluster.DefaultInformerFactory(baseInformerOpts...)),
		)
		// CRD discovery and Gatekeeper discovery are per-cluster work
		// that doesn't fit the single-shared-informer model. P3-T21
		// ships the core-GVR federation; per-cluster CRD/policy
		// discovery is a follow-up.
		crdStarter = noopStarter{}
		gkStarter = noopStarter{}
		// Report the live attached-cluster count in telemetry.
		telemetryClusterCount = func() int { return len(mcMgr.ListClusters()) }
		slog.Info("multicluster enabled", "members", len(kubeconfigs))
	} else {
		informerStarter = discovery.NewInformerManager(
			client.Dynamic(), graphStore,
			append(baseInformerOpts, discovery.WithOnSynced(srv.Readiness().MarkReady))...,
		)
		crdStarter = crd.New(client.Dynamic(), graphStore,
			crd.WithRegoEvaluator(regoEngine),
		)
		// Gatekeeper: watch ConstraintTemplates and register a dynamic
		// informer per generated Constraint kind. The manager's metrics
		// are the dynMetrics surfaced on /metrics above.
		dynMgr := discovery.NewDynamicInformerManager(
			client.Dynamic(), discovery.WithDynamicMetrics(dynMetrics),
		)
		gkStarter = gatekeeper.New(client.Dynamic(), graphStore, extractor.Default(), dynMgr)
	}
	if mcMgr != nil {
		// Re-bind srv with the cluster lister now that mcMgr exists.
		// api.New is cheap (no listener until Start) so building it
		// twice in the multicluster branch is fine; baseInformerOpts
		// holds no reference to srv, so the swap is safe.
		apiOpts = append(apiOpts, api.WithClusterLister(mcMgr))
		srv = api.New(apiAddr, graphStore, aggregator.NewRegistry(), apiOpts...)
		informerStarter = &multiclusterStarter{
			mgr:         mcMgr,
			kubeconfigs: mcKubeconfigs,
			onReady:     srv.Readiness().MarkReady,
		}
	}

	// Opt-in telemetry runs as a detached background goroutine, NOT one
	// of the blocking components below: disabled telemetry returns
	// immediately, and a component that returns would cascade-cancel the
	// whole server. It stops when ctx is cancelled.
	go func() { _ = telemetrySender.Run(ctx) }()

	// Run all components under the same cancellable context. If any
	// returns a non-Canceled error (or the user hits Ctrl-C), the
	// cancel cascades and the others wind down.
	type result struct {
		who string
		err error
	}
	const componentCount = 5
	results := make(chan result, componentCount)
	go func() { results <- result{"informer", informerStarter.Start(ctx)} }()
	go func() { results <- result{"api", srv.Start(ctx)} }()
	go func() { results <- result{"crd-discovery", crdStarter.Start(ctx)} }()
	go func() { results <- result{"gatekeeper", gkStarter.Start(ctx)} }()
	go func() { results <- result{"otel", otelStarter.Start(ctx)} }()

	collected := make([]result, 0, componentCount)
	collected = append(collected, <-results)
	cancel()
	for len(collected) < componentCount {
		collected = append(collected, <-results)
	}

	// All components have stopped, so the informer can no longer
	// Enqueue. Drain the snapshot writer's buffered events into the
	// store before exit (best-effort — Stop honours the per-event
	// retry budget, it does not block forever).
	if snapWriter != nil {
		snapWriter.Stop()
	}

	for _, r := range collected {
		if r.err != nil && !errors.Is(r.err, context.Canceled) {
			log.Fatalf("%s: %v", r.who, r.err)
		}
	}
}
