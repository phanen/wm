// Package modelquota fetches AI provider subscription quota / balance
// and joins them into a single line suitable for a status bar.
package modelquota

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultSeparator = " | "
	UserAgent        = "wm-bar/1.0"
	PerPlanTimeout   = 10 * time.Second
	DefaultTTL       = 60
)

// Plan describes a single subscription to poll. Token is the actual
// bearer value, embedded directly in WM_PLANS (or plans.json). The
// secrets-management layer (sops) provides the JSON; this struct
// does no further resolution.
type Plan struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Kind   string `json:"kind"`
	Token  string `json:"token"`
	Region string `json:"region"`
	TTL    int    `json:"ttl"`
}

func (p Plan) displayLabel() string {
	if p.Label != "" {
		return p.Label
	}
	if p.Name != "" {
		return p.Name
	}
	return "?"
}

// LoadPlans reads WM_PLANS (JSON) or falls back to $WM_PLANS_FILE or
// ~/.config/wm/plans.json. Returns nil if nothing is configured.
func LoadPlans() []Plan {
	var source string
	raw := os.Getenv("WM_PLANS")
	if raw != "" {
		source = "$WM_PLANS"
	} else {
		path := os.Getenv("WM_PLANS_FILE")
		if path == "" {
			if h, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(h, ".config", "wm", "plans.json")
			}
		}
		if path != "" {
			if data, err := os.ReadFile(path); err == nil {
				raw = string(data)
				source = path
			} else if !os.IsNotExist(err) {
				log.Printf("model-quota: read %s: %v", path, err)
			}
		}
	}
	if raw == "" {
		log.Printf("model-quota: no plans configured (set $WM_PLANS or write ~/.config/wm/plans.json)")
		return nil
	}
	var plans []Plan
	if err := json.Unmarshal([]byte(raw), &plans); err != nil {
		log.Printf("model-quota: parse %s: %v", source, err)
		return nil
	}
	log.Printf("model-quota: loaded %d plans from %s", len(plans), source)
	return plans
}

// PlanFetcher returns the rendered segment for a plan. The bearer
// token is taken from p.Token (resolved upstream by whoever built
// WM_PLANS).
type PlanFetcher func(ctx context.Context, p Plan) (string, error)

// FetchAll runs every configured plan fetcher concurrently, preserving
// the declared order, and returns the joined line. Failed plans are
// logged to stderr and omitted. Returns "" if all plans fail or none
// are configured.
func FetchAll() string {
	return FetchAllContext(context.Background())
}

func FetchAllContext(parent context.Context) string {
	plans := LoadPlans()
	if len(plans) == 0 {
		return ""
	}
	sep := os.Getenv("WM_PLANS_SEP")
	if sep == "" {
		sep = DefaultSeparator
	}
	results := make([]string, len(plans))
	var wg sync.WaitGroup
	wg.Add(len(plans))
	plans = dedupLabels(plans)
	log.Printf("model-quota: fetching %d plans (sep=%q)", len(plans), sep)
	for i, p := range plans {
		go func(i int, p Plan) {
			defer wg.Done()
			results[i] = fetchOne(parent, p)
		}(i, p)
	}
	wg.Wait()
	out := make([]string, 0, len(results))
	for _, s := range results {
		if s != "" {
			out = append(out, s)
		}
	}
	joined := strings.Join(out, sep)
	log.Printf("model-quota: %d/%d plans succeeded, %d bytes", len(out), len(plans), len(joined))
	return joined
}

// dedupLabels appends `·N` to plans whose label collides with another
// plan in the same list, so the bar can tell them apart. The first
// occurrence keeps the bare label; subsequent ones get a suffix.
func dedupLabels(plans []Plan) []Plan {
	counts := map[string]int{}
	for _, p := range plans {
		if p.Label != "" {
			counts[p.Label]++
		}
	}
	seen := map[string]int{}
	out := make([]Plan, len(plans))
	for i, p := range plans {
		out[i] = p
		if p.Label != "" && counts[p.Label] > 1 {
			seen[p.Label]++
			out[i].Label = p.Label + "·" + strconv.Itoa(seen[p.Label])
		}
	}
	return out
}

func fetchOne(parent context.Context, p Plan) string {
	if p.Kind == "" {
		log.Printf("model-quota: [%s] missing kind", p.Name)
		return ""
	}
	f, ok := planFetchers[p.Kind]
	if !ok {
		log.Printf("model-quota: [%s] unknown kind: %s", p.Name, p.Kind)
		return ""
	}
	if p.Token == "" {
		log.Printf("model-quota: [%s] token empty in WM_PLANS", p.Name)
		return ""
	}
	ctx, cancel := context.WithTimeout(parent, PerPlanTimeout)
	defer cancel()
	s, err := f(ctx, p)
	if err != nil {
		log.Printf("model-quota: [%s] %v", p.Name, err)
		return ""
	}
	return s
}
