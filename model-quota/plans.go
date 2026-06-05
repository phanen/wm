// Package modelquota fetches AI provider subscription quota / balance
// and joins them into a single line suitable for a status bar.
package modelquota

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
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

// initialLabel returns the first character of the display label,
// uppercased. Used as the group prefix that appears once per
// provider group in the rendered bar.
func (p Plan) initialLabel() string {
	l := p.displayLabel()
	if l == "" {
		return "?"
	}
	return strings.ToUpper(l[:1])
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

// PlanFetcher returns the rendered data segment for a plan (without
// a leading label — the caller prefixes the group initial).
type PlanFetcher func(ctx context.Context, p Plan) (string, error)

// FetchAll runs every configured plan fetcher concurrently, groups
// consecutive plans sharing the same (initial, kind), and returns
// the joined bar text. Each group gets its single-letter initial
// prefixed exactly once, even if it has multiple subscriptions.
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

	// Parallel fetch, preserving declared order.
	results := make([]string, len(plans))
	var wg sync.WaitGroup
	wg.Add(len(plans))
	for i, p := range plans {
		go func(i int, p Plan) {
			defer wg.Done()
			results[i] = fetchOne(parent, p)
		}(i, p)
	}
	wg.Wait()

	// Group consecutive non-failed plans by (initial, kind). A failed
	// plan in the middle breaks the chain so the next successful plan
	// starts a new group (failure shouldn't visually merge with
	// earlier successes).
	type groupKey struct {
		initial string
		kind    string
	}
	type group struct {
		key  groupKey
		data []string
	}
	var groups []group
	var lastKey groupKey
	var haveLast bool
	for i, p := range plans {
		if results[i] == "" {
			haveLast = false
			continue
		}
		k := groupKey{initial: p.initialLabel(), kind: p.Kind}
		if haveLast && lastKey == k {
			groups[len(groups)-1].data = append(groups[len(groups)-1].data, results[i])
		} else {
			groups = append(groups, group{key: k, data: []string{results[i]}})
			lastKey = k
			haveLast = true
		}
	}

	// Render each group: "X a b c" where X is the initial and the
	// remaining are the per-plan data joined with single spaces.
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.key.initial+" "+strings.Join(g.data, " "))
	}
	joined := strings.Join(out, sep)
	log.Printf("model-quota: %d/%d plans in %d groups, %d bytes", len(plans), len(plans), len(groups), len(joined))
	return joined
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
