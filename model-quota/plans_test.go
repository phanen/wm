package modelquota

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestFetchAllGroupsByLabelAndKind(t *testing.T) {
	t.Setenv("WM_PLANS", `[
		{"name":"d","label":"DeepSeek","kind":"_t1","token":"k1"},
		{"name":"m1","label":"MiniMax","kind":"_t2","token":"k2"},
		{"name":"m2","label":"MiniMax","kind":"_t2","token":"k3"},
		{"name":"m3","label":"MiniMax","kind":"_t2","token":"k4"}
	]`)
	t.Setenv("WM_PLANS_SEP", "")
	prev := planFetchers
	planFetchers["_t1"] = func(_ context.Context, p Plan) (string, error) { return "bal", nil }
	planFetchers["_t2"] = func(_ context.Context, p Plan) (string, error) { return "data-" + p.Name, nil }
	defer func() { planFetchers = prev }()
	got := FetchAll()
	// expected: D bal | M data-m1 data-m2 data-m3
	want := "D bal | M data-m1 data-m2 data-m3"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFetchAllFailedPlanBreaksGroup(t *testing.T) {
	t.Setenv("WM_PLANS", `[
		{"name":"m1","label":"MiniMax","kind":"_t2","token":"k2"},
		{"name":"x","label":"X","kind":"_unknown","token":"k"},
		{"name":"m2","label":"MiniMax","kind":"_t2","token":"k3"}
	]`)
	t.Setenv("WM_PLANS_SEP", "")
	prev := planFetchers
	planFetchers["_t2"] = func(_ context.Context, p Plan) (string, error) { return "data-" + p.Name, nil }
	defer func() { planFetchers = prev }()
	got := FetchAll()
	// m1 and m2 should NOT be merged because of the failed plan in between
	if !strings.Contains(got, "M data-m1") || !strings.Contains(got, "M data-m2") {
		t.Errorf("expected both m1 and m2 separately, got %q", got)
	}
	if strings.Contains(got, "data-m1 data-m2") {
		t.Errorf("expected m1 and m2 NOT merged across failed plan, got %q", got)
	}
}

func TestShortHours(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0h"},
		{3599, "0h"},
		{3600, "1h"},
		{18000, "5h"},
	}
	for _, c := range cases {
		if got := shortHours(c.in); got != c.want {
			t.Errorf("shortHours(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShortDays(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0d"},
		{86399, "0d"},
		{86400, "1d"},
		{604800, "7d"},
	}
	for _, c := range cases {
		if got := shortDays(c.in); got != c.want {
			t.Errorf("shortDays(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShortModel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"general", "g"},
		{"video", "v"},
		{"audio", "a"},
		{"music", "m"},
		{"image", "i"},
		{"weirdname", "w"},
		{"", "?"},
	}
	for _, c := range cases {
		if got := shortModel(c.in); got != c.want {
			t.Errorf("shortModel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPlanInitialLabel(t *testing.T) {
	if (Plan{Label: "DeepSeek"}).initialLabel() != "D" {
		t.Errorf("DeepSeek -> D")
	}
	if (Plan{Name: "minimax2"}).initialLabel() != "M" {
		t.Errorf("minimax2 -> M")
	}
}
