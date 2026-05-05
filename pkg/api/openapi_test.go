package api_test

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/api"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestOpenAPI_TopLevelShape(t *testing.T) {
	base, _, stop := seedAndServe(t, nil)
	defer stop()

	resp, body := getJSON(t, base+"/api/v1alpha1/openapi.json", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", resp.StatusCode, body)
	}
	var spec map[string]any
	if err := json.Unmarshal(body, &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := spec["openapi"].(string); !strings.HasPrefix(got, "3.0") {
		t.Errorf("openapi version = %q, want 3.0.x", got)
	}
	info, _ := spec["info"].(map[string]any)
	if info == nil {
		t.Fatal("missing 'info' object")
	}
	if title, _ := info["title"].(string); title == "" {
		t.Error("info.title empty")
	}
	if version, _ := info["version"].(string); version == "" {
		t.Error("info.version empty")
	}
	if _, ok := spec["paths"].(map[string]any); !ok {
		t.Error("missing 'paths' object")
	}
	if comps, _ := spec["components"].(map[string]any); comps == nil {
		t.Error("missing 'components' object")
	} else if _, ok := comps["schemas"].(map[string]any); !ok {
		t.Error("missing 'components.schemas' object")
	}
}

// TestOpenAPI_PathsMatchRegisteredRoutes is the load-bearing contract
// test: every registered route appears in the spec, and every spec
// path traces back to a registered route. By design the two share the
// same source (Server.Routes()), but this guards against regressions
// where someone bypasses the route table.
func TestOpenAPI_PathsMatchRegisteredRoutes(t *testing.T) {
	srv := api.New(":0", memory.New(), aggregator.NewRegistry())

	// Every route the server registers.
	registered := srv.PathPatterns()

	// Every path the spec advertises.
	spec := pullSpec(t)
	pathsObj, _ := spec["paths"].(map[string]any)
	specPaths := make([]string, 0, len(pathsObj))
	for p := range pathsObj {
		specPaths = append(specPaths, p)
	}
	sort.Strings(specPaths)

	if len(registered) != len(specPaths) {
		t.Errorf("registered %d routes but spec has %d paths\nregistered=%v\nspec=%v",
			len(registered), len(specPaths), registered, specPaths)
	}
	for i := range registered {
		if i >= len(specPaths) {
			break
		}
		if registered[i] != specPaths[i] {
			t.Errorf("path mismatch at index %d: registered=%q spec=%q", i, registered[i], specPaths[i])
		}
	}
}

func TestOpenAPI_EveryRouteHasAGetOperation(t *testing.T) {
	spec := pullSpec(t)
	paths, _ := spec["paths"].(map[string]any)
	for path, raw := range paths {
		entry, ok := raw.(map[string]any)
		if !ok {
			t.Errorf("path %q: not an object", path)
			continue
		}
		if _, ok := entry["get"].(map[string]any); !ok {
			t.Errorf("path %q: missing 'get' operation", path)
		}
	}
}

func TestOpenAPI_GraphOperationDocumentsLevelEnum(t *testing.T) {
	spec := pullSpec(t)
	paths, _ := spec["paths"].(map[string]any)
	graphPath, _ := paths["/api/v1alpha1/graph"].(map[string]any)
	op, _ := graphPath["get"].(map[string]any)
	params, _ := op["parameters"].([]any)
	for _, p := range params {
		pm, _ := p.(map[string]any)
		if pm["name"] != "level" {
			continue
		}
		schema, _ := pm["schema"].(map[string]any)
		enum, _ := schema["enum"].([]any)
		if len(enum) != 4 {
			t.Errorf("level enum length = %d, want 4 (cluster|namespace|workload|resource); got %v",
				len(enum), enum)
		}
		return
	}
	t.Error("graph operation has no 'level' parameter")
}

func TestOpenAPI_StandardErrorResponsesPresent(t *testing.T) {
	spec := pullSpec(t)
	paths, _ := spec["paths"].(map[string]any)
	resourceOp, _ := paths["/api/v1alpha1/resources/{namespace}/{kind}/{name}"].(map[string]any)
	op, _ := resourceOp["get"].(map[string]any)
	resps, _ := op["responses"].(map[string]any)
	for _, code := range []string{"400", "404", "500"} {
		if _, ok := resps[code]; !ok {
			t.Errorf("resource detail operation missing response code %q", code)
		}
	}
}

func TestOpenAPI_WatchUses101SwitchingProtocols(t *testing.T) {
	spec := pullSpec(t)
	paths, _ := spec["paths"].(map[string]any)
	watch, _ := paths["/api/v1alpha1/watch"].(map[string]any)
	op, _ := watch["get"].(map[string]any)
	resps, _ := op["responses"].(map[string]any)
	if _, ok := resps["101"]; !ok {
		t.Errorf("watch endpoint should advertise 101 (Switching Protocols), got responses=%v", resps)
	}
}

func TestOpenAPI_ComponentsCoverAllReferencedSchemas(t *testing.T) {
	spec := pullSpec(t)
	comps, _ := spec["components"].(map[string]any)
	schemas, _ := comps["schemas"].(map[string]any)
	defined := map[string]bool{}
	for k := range schemas {
		defined[k] = true
	}

	// Walk the spec and collect every $ref string.
	refs := map[string]bool{}
	collectRefs(spec, refs)
	for ref := range refs {
		const prefix = "#/components/schemas/"
		if !strings.HasPrefix(ref, prefix) {
			t.Errorf("unexpected $ref shape: %q", ref)
			continue
		}
		name := ref[len(prefix):]
		if !defined[name] {
			t.Errorf("$ref %q points at undefined schema", ref)
		}
	}
}

// pullSpec calls /openapi.json against a fresh server and decodes the
// response.
func pullSpec(t *testing.T) map[string]any {
	t.Helper()
	base, _, stop := seedAndServe(t, nil)
	defer stop()
	resp, body := getJSON(t, base+"/api/v1alpha1/openapi.json", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var spec map[string]any
	if err := json.Unmarshal(body, &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return spec
}

// collectRefs walks v recursively and adds every "$ref": "..." value to refs.
func collectRefs(v any, refs map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		if r, ok := x["$ref"].(string); ok {
			refs[r] = true
		}
		for _, val := range x {
			collectRefs(val, refs)
		}
	case []any:
		for _, val := range x {
			collectRefs(val, refs)
		}
	}
}
