package parse

import "testing"

func TestGRPCMockFilter(t *testing.T) {
	// Real service ctors/regs still extract.
	for _, c := range []struct{ name, want string }{
		{"NewCamcomPublicServiceClient", "CamcomPublicService"},
		{"NewAuthClient", "Auth"},
	} {
		if svc, ok := grpcServiceFromClientCtor(c.name); !ok || svc != c.want {
			t.Errorf("client ctor %q → (%q,%v), want (%q,true)", c.name, svc, ok, c.want)
		}
	}
	if svc, ok := grpcServiceFromServerReg("RegisterAuthServer"); !ok || svc != "Auth" {
		t.Errorf("server reg RegisterAuthServer → (%q,%v), want (Auth,true)", svc, ok)
	}

	// Test doubles are filtered on both sides.
	for _, name := range []string{"NewMockVideoCloudClient", "NewFakeAuthClient", "NewStubClient", "NewMockedFooClient", "NewSpyBarClient"} {
		if svc, ok := grpcServiceFromClientCtor(name); ok {
			t.Errorf("client ctor %q should be filtered as a mock, got %q", name, svc)
		}
	}
	if _, ok := grpcServiceFromServerReg("RegisterMockFooServer"); ok {
		t.Errorf("server reg RegisterMockFooServer should be filtered as a mock")
	}
}

func TestSkipGeneratedGateway(t *testing.T) {
	// Generated glue (protoc, grpc-gateway, protoc-gen-validate, connect-go) is skipped
	// so it can't emit artifactual clients; hand-written code is not.
	for _, f := range []string{"svc.pb.go", "svc.pb.gw.go", "svc.pb.validate.go", "svc.connect.go", "svc.gen.go"} {
		if !skipFile(f) {
			t.Errorf("%q should be skipped (generated)", f)
		}
	}
	for _, f := range []string{"service.go", "client.go", "handler.go"} {
		if skipFile(f) {
			t.Errorf("%q is hand-written and must NOT be skipped", f)
		}
	}
}

// isTestFile gates contract extraction (lodestar #9). Precision matters in BOTH
// directions: every real test file must be caught (else fixtures re-forge false
// cross-service edges), and NO production file may be caught (else real contracts
// vanish). The false-positive cases are the point — Latest.java / Contest.cs /
// latest.go all END IN "test" as a substring but are production code.
func TestIsTestFile(t *testing.T) {
	tests := []string{
		// Go, Python, TS/JS (lowercase conventions)
		"pkg/client_test.go", "internal/svc/grpc_test.go",
		"tests/test_catalog.py", "app/catalog_test.py", "conftest.py",
		"src/api.test.ts", "src/api.spec.ts", "web/App.test.tsx", "web/App.spec.jsx",
		"components/button.test.js",
		// Java / C# (CamelCase Test/Tests)
		"src/main/java/com/x/CatalogServiceTest.java", "x/OrderTests.java",
		"Services/CartServiceTests.cs", "Api/CheckoutTest.cs",
		// Directory conventions
		"testdata/fixture.go", "__tests__/foo.ts", "__mocks__/bar.js",
		"src/test/java/com/x/Helper.java",
	}
	for _, f := range tests {
		if !isTestFile(f) {
			t.Errorf("isTestFile(%q) = false, want true (test file)", f)
		}
	}
	production := []string{
		// The substring traps — end in "test" but are NOT tests.
		"pkg/Latest.java", "svc/Contest.cs", "internal/latest.go",
		"api/greatest.py", "web/protest.ts",
		// Ordinary production files.
		"internal/svc/grpc.go", "app/catalog.py", "src/api.ts", "Services/CartService.cs",
		"src/main/java/com/x/CatalogService.java",
		// "test" as a mid-path segment that is NOT src/test or a fixture dir.
		"cmd/testrunner/main.go", "internal/attest/sign.go",
	}
	for _, f := range production {
		if isTestFile(f) {
			t.Errorf("isTestFile(%q) = true, want false (production code)", f)
		}
	}
}
