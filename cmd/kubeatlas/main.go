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
	"strconv"
	"strings"
	"syscall"
	"time"

	"log/slog"

	kdiscovery "k8s.io/client-go/discovery"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/api"
	"github.com/lithastra/kubeatlas/pkg/crd"
	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/extractor/rego"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/snapshot"
	"github.com/lithastra/kubeatlas/pkg/store"
	"github.com/lithastra/kubeatlas/pkg/store/postgres"
	"github.com/lithastra/kubeatlas/pkg/version"
)

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
		runOnce(*level, *namespace, *kind, *name, *format)
		return
	}
	runWatch(rulePackRefs(rulePacks))
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
func runOnce(level, namespace, kind, name, format string) {
	switch format {
	case "json", "dot", "svg":
	default:
		log.Fatalf("unknown -format=%q (want json | dot | svg)", format)
	}

	ctx := context.Background()

	client, err := discovery.NewClient()
	if err != nil {
		log.Fatalf("failed to load kubeconfig: %v", err)
	}
	resources, err := client.CollectAll()
	if err != nil {
		log.Fatalf("failed to collect resources: %v", err)
	}

	graphStore, err := store.New(ctx, loadStoreConfig())
	if err != nil {
		log.Fatalf("failed to construct graph store: %v", err)
	}
	for _, r := range resources {
		if err := graphStore.UpsertResource(ctx, r); err != nil {
			log.Fatalf("failed to upsert resource %s: %v", r.ID(), err)
		}
	}

	// Extract edges through the typed extractor.Registry, the same path
	// the informer uses, so -once mode and the watch loop produce the
	// same eight edge types from the same code. Every resource is
	// already in graphStore, so the extractors query it directly.
	reg := extractor.Default()
	for _, r := range resources {
		edges, err := reg.ExtractAll(ctx, r, graphStore)
		if err != nil {
			log.Fatalf("failed to extract edges for %s: %v", r.ID(), err)
		}
		for _, e := range edges {
			if err := graphStore.UpsertEdge(ctx, e); err != nil {
				log.Fatalf("failed to upsert edge %s -> %s: %v", e.From, e.To, err)
			}
		}
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

// runWatch starts the informer and the API server in parallel and
// blocks until either errors or the process receives SIGINT/SIGTERM.
// Both shut down when the parent context is cancelled.
func runWatch(rulePackExtras []string) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	client, err := discovery.NewClient()
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

	// API server options. snapWriter is nil unless Tier 2 + snapshots
	// enabled; when set, /metrics surfaces its counters + queue depth.
	apiOpts := []api.ServerOption{
		api.WithWebFS(webFS),
		api.WithRegoMetrics(regoEngine.Metrics(), regoEngine.ModuleCount),
	}
	if snapWriter != nil {
		apiOpts = append(apiOpts,
			api.WithSnapshotMetrics(snapWriter.Metrics(), snapWriter.QueueDepth),
			api.WithSnapshots(snapRetention))
	}
	srv := api.New(api.DefaultAddr, graphStore, aggregator.NewRegistry(), apiOpts...)

	// Informer options. WithOnSynced / WithBroadcaster depend on srv,
	// so this list is built after srv exists. The snapshot sink is
	// appended only when the writer is running.
	informerOpts := []discovery.InformerOption{
		discovery.WithGVRs(gvrs),
		discovery.WithExtractor(extractor.Default()),
		discovery.WithOnSynced(srv.Readiness().MarkReady),
		discovery.WithBroadcaster(srv.Hub().BroadcastEvent),
	}
	if snapWriter != nil {
		informerOpts = append(informerOpts, discovery.WithSnapshotSink(snapWriter))
	}
	mgr := discovery.NewInformerManager(client.Dynamic(), graphStore, informerOpts...)
	crdDiscovery := crd.New(client.Dynamic(), graphStore,
		crd.WithRegoEvaluator(regoEngine),
	)

	// Run all three components under the same cancellable context.
	// If any returns a non-Canceled error (or the user hits Ctrl-C),
	// the cancel cascades and the others wind down.
	type result struct {
		who string
		err error
	}
	results := make(chan result, 3)
	go func() { results <- result{"informer", mgr.Start(ctx)} }()
	go func() { results <- result{"api", srv.Start(ctx)} }()
	go func() { results <- result{"crd-discovery", crdDiscovery.Start(ctx)} }()

	first := <-results
	cancel()
	second := <-results
	third := <-results

	// All three components have stopped, so the informer can no
	// longer Enqueue. Drain the snapshot writer's buffered events
	// into the store before exit (best-effort — Stop honours the
	// per-event retry budget, it does not block forever).
	if snapWriter != nil {
		snapWriter.Stop()
	}

	for _, r := range []result{first, second, third} {
		if r.err != nil && !errors.Is(r.err, context.Canceled) {
			log.Fatalf("%s: %v", r.who, r.err)
		}
	}
}
