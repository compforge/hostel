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
	for requested, want := range map[string]string{"": "auto", "noop": "noop", "s3": "s3", "cas": "s3", "pack": "pack", "tar": "tar"} {
		got, err := Select(t.Context(), selection, requested)
		if err != nil || got.Name() != want {
			t.Fatalf("select %q = %v, %v", requested, got, err)
		}
	}
	cas, _ := Select(t.Context(), selection, "s3")
	pack, _ := Select(t.Context(), selection, "pack")
	tar, _ := Select(t.Context(), selection, "tar")
	if cas.(*casStore).obj != pack.(*packStore).obj || cas.(*casStore).obj != tar.(*tarStore).obj {
		t.Fatal("backend formats opened separate object clients")
	}
	cfg.Backend = "noop"
	noop, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	s3, err := Select(t.Context(), noop, "s3")
	if err != nil || s3.Name() != "s3" {
		t.Fatalf("API override of noop: %v %v", s3, err)
	}
	if noop.Name() != "noop" {
		t.Fatal("Bed override mutated instance default")
	}
	if _, err := Select(t.Context(), selection, "unknown"); err == nil {
		t.Fatal("unknown backend accepted")
	}
}
