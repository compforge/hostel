package store

import (
	"testing"
)

func TestSelectionOverridesDefaultAndSharesObjectClient(t *testing.T) {
	cfg := Config{Backend: "auto", Bucket: "test", Endpoint: "http://127.0.0.1:1", Region: "us-east-1", AccessKeyID: "test", SecretAccessKey: "test"}
	selection, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for requested, want := range map[string]BackendKind{"": "auto", "noop": "noop", "s3": "s3", "cas": "s3", "pack": "pack", "tar": "tar"} {
		got, err := selection.Resolve(t.Context(), requested)
		if err != nil || got != want {
			t.Fatalf("select %q = %v, %v", requested, got, err)
		}
	}
	cas, _ := selection.backend(t.Context(), BackendS3)
	pack, _ := selection.backend(t.Context(), BackendPack)
	tar, _ := selection.backend(t.Context(), BackendTar)
	if cas.(*casStore).obj != pack.(*packStore).obj || cas.(*casStore).obj != tar.(*tarStore).obj {
		t.Fatal("backend formats opened separate object clients")
	}
	cfg.Backend = "noop"
	noop, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	s3, err := noop.Resolve(t.Context(), "s3")
	if err != nil || s3 != BackendS3 {
		t.Fatalf("API override of noop: %v %v", s3, err)
	}
	if noop.DefaultKind() != "noop" {
		t.Fatal("Bed override mutated instance default")
	}
	if _, err := selection.Resolve(t.Context(), "unknown"); err == nil {
		t.Fatal("unknown backend accepted")
	}
}
