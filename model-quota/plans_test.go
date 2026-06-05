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
	if !strings.Contains(got, "M data-m1") || !strings.Contains(got, "M data-m2") {
		t.Errorf("expected both m1 and m2 separately, got %q", got)
	}
	if strings.Contains(got, "data-m1 data-m2") {
		t.Errorf("expected m1 and m2 NOT merged across failed plan, got %q", got)
	}
}

func TestWindowPct(t *testing.T) {
	// 5h window: 18000000 ms total. All values in milliseconds.
	start := float64(1780624800000)
	end := float64(1780642800000) // start + 5h = 18000000 ms
	cases := []struct {
		name string
		rem  float64
		want int
	}{
		{"0h left", 0, 0},
		{"2.5h left (9000000 ms)", 9000000, 50},
		{"full 5h left (18000000 ms)", 18000000, 100},
		{"over 5h (20000000 ms)", 20000000, 100}, // clamped
		{"negative (clock skew)", -100, 0},        // clamped
	}
	for _, c := range cases {
		got := windowPct(c.rem, start, end)
		if got != c.want {
			t.Errorf("%s: windowPct(%v) = %d, want %d", c.name, c.rem, got, c.want)
		}
	}
}

func TestWindowPctMissingOrInvalid(t *testing.T) {
	if got := windowPct(1000, nil, float64(100)); got != 0 {
		t.Errorf("missing start: %d, want 0", got)
	}
	if got := windowPct(1000, float64(0), float64(0)); got != 0 {
		t.Errorf("zero window: %d, want 0", got)
	}
	if got := windowPct(1000, float64(200), float64(100)); got != 0 {
		t.Errorf("inverted: %d, want 0", got)
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
