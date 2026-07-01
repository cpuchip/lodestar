package parse

// Helm charts are structured data (YAML), not code, so — like OpenAPI specs — they
// take a parallel handler off the tree-sitter path. The cross-service value in a
// chart is DIRECTIONAL and lives in two places:
//
//   - Chart.yaml `name` — the service this chart deploys. Emitted as a KindService
//     PRODUCER keyed on the normalized service name: "this world provides <name>".
//   - values.yaml env-style keys naming an UPSTREAM service — e.g.
//     AUTH_SERVICE_HOST: auth, DEVICE_SERVICE_HOST: device-headless,
//     ACCOUNT_SYSTEM_HOST: accountsystem-v2. The VALUE is a k8s service name (a
//     world). Emitted as a KindServiceRef CONSUMER keyed on the normalized target.
//
// resolve pairs KindService (producer) with KindServiceRef (consumer) on the shared
// key → a cross-service `connects_to` edge (consumer → producer). Chart.yaml
// `dependencies` are deliberately NOT used as edges: in practice they point almost
// entirely at a shared base/library chart (a universal dep = galaxy plumbing, not a
// service seam), so they'd be pure noise; the deployment topology is in the values.
//
// Generic mechanism, house profile: the key patterns and the value normalizer are
// conventional (k8s `<DEP>_HOST`/`_ADDR`/`_URL` env, `-headless` service variants),
// and are the tunable surface a downstream profile overrides for its own scheme.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/cpuchip/lodestar/internal/graph"
)

// reServiceRefKey matches env-style keys whose value names an upstream service:
// anything ending in _HOST / _ADDR / _ADDRESS / _URL / _ENDPOINT / _UPSTREAM / _SVC.
// Deliberately excludes _PORT* (a number, not a service) and everything else.
var reServiceRefKey = regexp.MustCompile(`(?i)_(HOST|ADDR|ADDRESS|URL|ENDPOINT|UPSTREAM|SVC)$`)

// reServiceLabel accepts a bare DNS-1123 service label (what a normalized k8s
// service name looks like). Rejects templates ({{…}}), URLs with paths, empty, and
// anything with characters a service name can't contain.
var reServiceLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// isHelmChartFile / isHelmValuesFile detect the two files we read. Case-insensitive
// on the conventional names; values-<env>.yaml overlays count as values too.
func isHelmChartFile(name string) bool {
	return strings.EqualFold(name, "Chart.yaml") || strings.EqualFold(name, "Chart.yml")
}

func isHelmValuesFile(name string) bool {
	l := strings.ToLower(name)
	return l == "values.yaml" || l == "values.yml" ||
		(strings.HasPrefix(l, "values-") && (strings.HasSuffix(l, ".yaml") || strings.HasSuffix(l, ".yml")))
}

// normalizeService reduces a chart name or a k8s service reference to a stable key:
// lowercased, and stripped of the `-headless` variant suffix so a headless Service
// and its clusterIP peer collapse to the same service.
func normalizeService(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), "-headless")
}

// serviceFromValue extracts a k8s service name from a values entry, or ("",false).
// Handles bare names (auth), headless variants (auth-headless), FQDNs
// (auth.ns.svc.cluster.local → auth), and URLs (http://auth:8080/x → auth). Rejects
// templated values, numbers, and anything that isn't a DNS-1123 label.
func serviceFromValue(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, "://"); i >= 0 {
		v = v[i+3:] // strip scheme
	}
	if i := strings.IndexAny(v, "/:"); i >= 0 {
		v = v[:i] // strip port / path
	}
	if i := strings.IndexByte(v, '.'); i >= 0 {
		v = v[:i] // FQDN → first label (the service name)
	}
	v = normalizeService(v)
	if v == "" || !reServiceLabel.MatchString(v) {
		return "", false
	}
	if strings.IndexFunc(v, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
		return "", false // all-digits (a port that slipped a key like FOO_ADDR: 8080)
	}
	return v, true
}

// parseHelmChart emits the chart's service-producer node (keyed on Chart.yaml name,
// falling back to the chart directory name).
func parseHelmChart(g *graph.Graph, world, rel, absPath string) error {
	src, err := os.ReadFile(absPath)
	if err != nil {
		return err
	}
	var doc struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil // malformed — skip, don't fail the walk
	}
	name := doc.Name
	if name == "" {
		name = filepath.Base(filepath.Dir(rel)) // charts/<svc>/Chart.yaml → <svc>
	}
	key := normalizeService(name)
	if key == "" {
		return nil
	}
	fileID := world + "::" + rel
	g.Nodes = append(g.Nodes, graph.Node{ID: fileID, World: world, Kind: graph.KindFile, Name: rel})
	p := &fileCtx{world: world, rel: rel, src: src, fileID: fileID, g: g, seen: map[string]bool{fileID: true}}
	p.addContract(graph.KindService, key, map[string]string{"source": "helm"})
	return nil
}

// parseHelmValues emits a service-ref consumer node for every env-style key whose
// value names an upstream service. Deterministic: refs are collected, de-duped, and
// sorted before emit.
func parseHelmValues(g *graph.Graph, world, rel, absPath string) error {
	src, err := os.ReadFile(absPath)
	if err != nil {
		return err
	}
	var root map[string]any
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil
	}
	refs := map[string]bool{}
	collectServiceRefs(root, refs)
	if len(refs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(refs))
	for k := range refs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fileID := world + "::" + rel
	g.Nodes = append(g.Nodes, graph.Node{ID: fileID, World: world, Kind: graph.KindFile, Name: rel})
	p := &fileCtx{world: world, rel: rel, src: src, fileID: fileID, g: g, seen: map[string]bool{fileID: true}}
	for _, k := range keys {
		p.addContract(graph.KindServiceRef, k, map[string]string{"source": "helm-values"})
	}
	return nil
}

// collectServiceRefs walks an unmarshaled YAML tree and records the normalized
// target service for every string value under a service-reference key.
func collectServiceRefs(node any, out map[string]bool) {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			if s, ok := child.(string); ok && reServiceRefKey.MatchString(k) {
				if svc, ok := serviceFromValue(s); ok {
					out[svc] = true
				}
			}
			collectServiceRefs(child, out)
		}
	case []any:
		for _, child := range v {
			collectServiceRefs(child, out)
		}
	}
}
