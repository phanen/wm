package modelquota

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPlansFromEnv(t *testing.T) {
	t.Setenv("WM_PLANS", `[{"name":"x","label":"X","kind":"deepseek_balance","token":"sek"}]`)
	t.Setenv("WM_PLANS_FILE", "")
	plans := LoadPlans()
	if len(plans) != 1 {
		t.Fatalf("got %d plans, want 1", len(plans))
	}
	if plans[0].Name != "x" || plans[0].Kind != "deepseek_balance" || plans[0].Token != "sek" {
		t.Errorf("unexpected plan: %+v", plans[0])
	}
}

func TestLoadPlansBadJSON(t *testing.T) {
	t.Setenv("WM_PLANS", "not json")
	if got := LoadPlans(); got != nil {
		t.Errorf("expected nil for bad JSON, got %v", got)
	}
}

func TestLoadPlansFromFile(t *testing.T) {
	t.Setenv("WM_PLANS", "")
	dir := t.TempDir()
	path := dir + "/plans.json"
	if err := os.WriteFile(path, []byte(`[{"name":"a","kind":"deepseek_balance","token":"sek"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WM_PLANS_FILE", path)
	plans := LoadPlans()
	if len(plans) != 1 || plans[0].Token != "sek" {
		t.Errorf("unexpected: %+v", plans)
	}
}

func TestFetchAllEmpty(t *testing.T) {
	t.Setenv("WM_PLANS", "")
	t.Setenv("WM_PLANS_FILE", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("HOME", t.TempDir())
	if got := FetchAll(); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestFetchAllSkipsUnknownKindAndEmptyToken(t *testing.T) {
	t.Setenv("WM_PLANS", `[
		{"name":"u","label":"U","kind":"unknown","token":"x"},
		{"name":"m","label":"M","kind":"deepseek_balance","token":""}
	]`)
	t.Setenv("WM_PLANS_FILE", filepath.Join(t.TempDir(), "missing.json"))
	got := FetchAll()
	if got != "" {
		t.Errorf("expected empty when all plans invalid, got %q", got)
	}
}

func TestFetchAllJoinsByDefaultSeparator(t *testing.T) {
	t.Setenv("WM_PLANS", `[
		{"name":"a","label":"A","kind":"_test_ok","token":"a"},
		{"name":"b","label":"B","kind":"_test_ok","token":"b"}
	]`)
	t.Setenv("WM_PLANS_SEP", "")
	prev := planFetchers
	planFetchers["_test_ok"] = func(_ context.Context, p Plan) (string, error) {
		return p.displayLabel() + "-ok", nil
	}
	defer func() { planFetchers = prev }()
	got := FetchAll()
	if got != "A-ok | B-ok" {
		t.Errorf("got %q, want %q", got, "A-ok | B-ok")
	}
}
