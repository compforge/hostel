package store

import (
	"testing"
)

func TestSelectionOverridesDefaultAndSharesObjectClient(t *testing.T) {
	cfg := Config{Sync: "auto", Bucket: "test", Endpoint: "http://127.0.0.1:1", Region: "us-east-1", AccessKeyID: "test", SecretAccessKey: "test"}
	selection, err := NewManager(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for requested, want := range map[string]SyncKind{"": "auto", "noop": "noop", "cas": "cas", "pack": "pack", "tar": "tar"} {
		got, err := selection.Resolve(t.Context(), requested)
		if err != nil || got != want {
			t.Fatalf("select %q = %v, %v", requested, got, err)
		}
	}
	remote := selection.remote
	for _, kind := range []SyncKind{SyncCAS, SyncPack, SyncTar} {
		first, err := selection.forSync(t.Context(), kind)
		if err != nil {
			t.Fatal(err)
		}
		again, err := selection.forSync(t.Context(), kind)
		if err != nil || first != again || selection.remote != remote {
			t.Fatalf("selection did not reuse store/client for %s: %v", kind, err)
		}
	}
	cfg.Sync = "noop"
	noop, err := NewManager(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	casKind, err := noop.Resolve(t.Context(), "cas")
	if err != nil || casKind != SyncCAS {
		t.Fatalf("API override of noop: %v %v", casKind, err)
	}
	if noop.DefaultSync() != "noop" {
		t.Fatal("Bed override mutated instance default")
	}
	if _, err := selection.Resolve(t.Context(), "unknown"); err == nil {
		t.Fatal("unknown backend accepted")
	}
}

// S3 describes the remote backend; it is not a synchronization policy.
func TestS3IsNotAStoreSync(t *testing.T) {
	cfg := testS3Config()
	cfg.Sync = "s3"
	if _, err := NewManager(t.Context(), cfg); err == nil {
		t.Fatal("accepted s3 as a store kind")
	}
	cfg.Sync = "auto"
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
		if err != nil || got != SyncNoop {
			t.Fatalf("resolve %s = %s, %v", kind, got, err)
		}
	}
}
