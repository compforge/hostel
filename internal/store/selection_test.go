package store

import (
	"testing"
)

func TestSelectionOverridesDefaultAndSharesObjectClient(t *testing.T) {
	cfg := Config{Kind: "auto", Bucket: "test", Endpoint: "http://127.0.0.1:1", Region: "us-east-1", AccessKeyID: "test", SecretAccessKey: "test"}
	selection, err := NewManager(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for requested, want := range map[string]Kind{"": "auto", "noop": "noop", "cas": "cas", "pack": "pack", "tar": "tar"} {
		got, err := selection.Resolve(t.Context(), requested)
		if err != nil || got != want {
			t.Fatalf("select %q = %v, %v", requested, got, err)
		}
	}
	cas, _ := selection.forKind(t.Context(), KindCAS)
	pack, _ := selection.forKind(t.Context(), KindPack)
	tar, _ := selection.forKind(t.Context(), KindTar)
	if cas.(*autoStore).cas.obj != pack.(*autoStore).pack.obj || cas.(*autoStore).cas.obj != tar.(*autoStore).tar.obj {
		t.Fatal("Store formats opened separate object clients")
	}
	cfg.Kind = "noop"
	noop, err := NewManager(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	casKind, err := noop.Resolve(t.Context(), "cas")
	if err != nil || casKind != KindCAS {
		t.Fatalf("API override of noop: %v %v", casKind, err)
	}
	if noop.DefaultKind() != "noop" {
		t.Fatal("Bed override mutated instance default")
	}
	if _, err := selection.Resolve(t.Context(), "unknown"); err == nil {
		t.Fatal("unknown backend accepted")
	}
}

// S3 describes the remote backend; it is not a synchronization policy.
func TestS3IsNotAStoreKind(t *testing.T) {
	cfg := testS3Config()
	cfg.Kind = "s3"
	if _, err := NewManager(t.Context(), cfg); err == nil {
		t.Fatal("accepted s3 as a store kind")
	}
	cfg.Kind = "auto"
	mgr, err := NewManager(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Resolve(t.Context(), "s3"); err == nil {
		t.Fatal("accepted s3 API alias")
	}
}

func TestAllPoliciesWithoutS3ResolveToNoop(t *testing.T) {
	mgr, err := NewManager(t.Context(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"noop", "auto", "cas", "pack", "tar"} {
		got, err := mgr.Resolve(t.Context(), kind)
		if err != nil || got != KindNoop {
			t.Fatalf("resolve %s = %s, %v", kind, got, err)
		}
	}
}
