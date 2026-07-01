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
