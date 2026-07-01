package parse

import (
	"reflect"
	"sort"
	"testing"
)

func TestServiceFromValue(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"auth", "auth", true},
		{"auth-headless", "auth", true},                          // headless variant collapses
		{"Auth-Headless", "auth", true},                          // case-insensitive
		{"accountsystem-v2-headless", "accountsystem-v2", true},  // versioned name kept, headless stripped
		{"http://auth:8080/v1", "auth", true},                    // URL → host label
		{"device.default.svc.cluster.local", "device", true},     // FQDN → first label
		{"auth:8399", "auth", true},                              // host:port
		{"{{ .Values.authHost }}", "", false},                    // template
		{"8399", "", false},                                      // all-digits (a port)
		{"", "", false},                                          // empty
		{"UPPER_CASE_THING", "", false},                          // underscores aren't a DNS label
	}
	for _, c := range cases {
		got, ok := serviceFromValue(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("serviceFromValue(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestServiceRefKeyGate(t *testing.T) {
	match := []string{"AUTH_SERVICE_HOST", "ACCOUNT_SYSTEM_HOST", "device_addr", "FOO_URL", "X_ENDPOINT", "Y_SVC"}
	skip := []string{"AUTH_SERVICE_PORT_GRPC", "PORT", "REPLICAS", "IMAGE_TAG", "hostname_prefix"}
	for _, k := range match {
		if !reServiceRefKey.MatchString(k) {
			t.Errorf("key %q should match the service-ref gate", k)
		}
	}
	for _, k := range skip {
		if reServiceRefKey.MatchString(k) {
			t.Errorf("key %q should NOT match the service-ref gate", k)
		}
	}
}

func TestCollectServiceRefs(t *testing.T) {
	// A nested values.yaml shape: env under a deployment, plus a port (skipped) and
	// a template (skipped). Infra (postgres) IS collected here — it's the resolver's
	// isServiceRefNoise that drops it, not the parser.
	values := map[string]any{
		"deployment": map[string]any{
			"env": map[string]any{
				"AUTH_SERVICE_HOST":     "auth-headless",
				"DEVICE_SERVICE_HOST":   "device",
				"AUTH_SERVICE_PORT_GRPC": "8399",             // _PORT → skipped by key gate
				"DB_HOST":               "postgres",          // collected; dropped at resolve
				"TEMPLATED_HOST":        "{{ .Values.x }}",   // template → skipped by value gate
			},
		},
		"replicas": 3,
	}
	got := map[string]bool{}
	collectServiceRefs(values, got)
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"auth", "device", "postgres"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("collectServiceRefs = %v, want %v", keys, want)
	}
}

func TestHelmFileDetect(t *testing.T) {
	if !isHelmChartFile("Chart.yaml") || !isHelmChartFile("Chart.yml") {
		t.Error("Chart.yaml/.yml should be detected as a chart")
	}
	if !isHelmValuesFile("values.yaml") || !isHelmValuesFile("values-prod.yaml") {
		t.Error("values.yaml / values-<env>.yaml should be detected")
	}
	if isHelmValuesFile("config.yaml") || isHelmChartFile("values.yaml") {
		t.Error("misclassification")
	}
}
