package modelquota

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPlansFromEnv(t *testing.T) {
	t.Setenv("WM_PLANS", `[{"name":"x","label":"X","kind":"deepseek_balance","token_env":"FOO"}]`)
	t.Setenv("WM_PLANS_FILE", "")
	plans := LoadPlans()
	if len(plans) != 1 {
		t.Fatalf("got %d plans, want 1", len(plans))
	}
	if plans[0].Name != "x" || plans[0].Kind != "deepseek_balance" {
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
	if err := os.WriteFile(path, []byte(`[{"name":"a","kind":"deepseek_balance","token_env":"X"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WM_PLANS_FILE", path)
	plans := LoadPlans()
	if len(plans) != 1 || plans[0].Name != "a" {
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

func TestFetchAllSkipsUnknownKindAndMissingToken(t *testing.T) {
	t.Setenv("WM_PLANS", `[
		{"name":"u","label":"U","kind":"unknown","token_env":"X"},
		{"name":"m","label":"M","kind":"deepseek_balance","token_env":""}
	]`)
	t.Setenv("WM_PLANS_FILE", filepath.Join(t.TempDir(), "missing.json"))
	got := FetchAll()
	if got != "" {
		t.Errorf("expected empty when all plans invalid, got %q", got)
	}
}

func TestAsNumber(t *testing.T) {
	cases := []struct {
		in   any
		want float64
		ok   bool
	}{
		{float64(1.5), 1.5, true},
		{int(2), 2, true},
		{int64(3), 3, true},
		{json.Number("4.25"), 4.25, true},
		{"nope", 0, false},
		{nil, 0, false},
	}
	for _, c := range cases {
		got, ok := asNumber(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("asNumber(%v) = (%v, %v), want (%v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestFmtDur(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0s"},
		{45, "45s"},
		{60, "1m"},
		{130, "2m"},
		{3600, "1h"},
		{3700, "1h1m"},
		{86400, "1d0h"},
		{90000, "1d1h"},
	}
	for _, c := range cases {
		if got := fmtDur(c.in); got != c.want {
			t.Errorf("fmtDur(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFetchAllJoinsByDefaultSeparator(t *testing.T) {
	// Inject two in-process fetchers so we can assert separator logic
	// without touching the network.
	t.Setenv("WM_PLANS", `[
		{"name":"a","label":"A","kind":"_test_ok","token_env":"_test_a"},
		{"name":"b","label":"B","kind":"_test_ok","token_env":"_test_b"}
	]`)
	t.Setenv("_test_a", "x")
	t.Setenv("_test_b", "y")
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
