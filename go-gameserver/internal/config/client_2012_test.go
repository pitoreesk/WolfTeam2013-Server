package config

import "testing"

func TestClient2012EnvFlag(t *testing.T) {
	t.Setenv("WT_CLIENT_2012", "")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv default: %v", err)
	}
	if cfg.Client2012 {
		t.Fatalf("expected default Client2012=false")
	}

	t.Setenv("WT_CLIENT_2012", "true")
	cfg, err = FromEnv()
	if err != nil {
		t.Fatalf("FromEnv true: %v", err)
	}
	if !cfg.Client2012 {
		t.Fatalf("expected Client2012=true")
	}

	t.Setenv("WT_CLIENT_2012", "not-a-bool")
	if _, err := FromEnv(); err == nil {
		t.Fatalf("expected error for invalid WT_CLIENT_2012 value")
	}
}
