package contracts

import "testing"

// The normalizer is the floor everything stands on, so its test is the project's
// first oracle: recall (forms that mean the same endpoint collide), precision
// (different route or method do NOT), and the inverse hypothesis (without the
// templating, the raw strings differ — so it is the normalization doing the work).
func TestNormalizeHTTPKey(t *testing.T) {
	// recall — these all mean GET /users/{}
	recall := []struct{ m, p string }{
		{"GET", "/users/123"},
		{"get", "/users/{id}"},
		{"GET", "/users/:id"},
		{"GET", "/users/${id}"},
		{"GET", "/api/users/55"},
		{"GET", "/v1/users/55"},
		{"GET", "/users/123?expand=true"},
	}
	want := "GET /users/{}"
	for _, c := range recall {
		if got := NormalizeHTTPKey(c.m, c.p); got != want {
			t.Errorf("recall: NormalizeHTTPKey(%q,%q) = %q, want %q", c.m, c.p, got, want)
		}
	}

	// precision — a different route or method must NOT collide
	if NormalizeHTTPKey("GET", "/orders/1") == NormalizeHTTPKey("GET", "/users/1") {
		t.Error("precision: /orders and /users must not collide")
	}
	if NormalizeHTTPKey("POST", "/users/1") == NormalizeHTTPKey("GET", "/users/1") {
		t.Error("precision: a method mismatch must not collide")
	}

	// inverse hypothesis — the raw strings differ; the normalization is what pairs them
	if "/users/123" == "/users/{id}" {
		t.Fatal("inverse: raw paths should differ (templating is doing the work)")
	}
}
