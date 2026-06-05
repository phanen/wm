package modelquota

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// fetchMinimaxCodingPlan queries the MiniMax "Coding Plan" (a.k.a. Token
// Plan) quota endpoint and renders each model's remaining percent and
// reset time as a compact fixed-width segment `<model>:xx%/yy%/Xh/Yd`,
// joined by spaces. Boosts and status fields are ignored.
//
// The renderer returns data WITHOUT a leading label; the caller adds
// the single-letter label once per group, avoiding repeats when the
// same model is subscribed to multiple times.
func fetchMinimaxCodingPlan(ctx context.Context, p Plan) (string, error) {
	base := "https://api.minimax.io"
	if p.Region == "cn" || p.Region == "minimax_cn" {
		base = "https://api.minimaxi.com"
	}
	if p.Token == "" {
		return "", fmt.Errorf("token unset")
	}
	data, err := httpGetJSON(ctx, base+"/v1/api/openplatform/coding_plan/remains", map[string]string{
		"Authorization": "Bearer " + p.Token,
		"Accept":        "application/json",
		"User-Agent":    UserAgent,
	})
	if err != nil {
		return "", err
	}
	if baseResp, ok := data["base_resp"].(map[string]any); ok {
		if code, _ := asNumber(baseResp["status_code"]); code != 0 {
			msg, _ := baseResp["status_msg"].(string)
			if code == 1004 {
				return "", fmt.Errorf("token invalid or expired")
			}
			return "", fmt.Errorf("api error %v: %s", code, msg)
		}
	}
	raw, _ := data["model_remains"].([]any)
	if len(raw) == 0 {
		return "", fmt.Errorf("no model_remains (no active plan?)")
	}
	parts := make([]string, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["model_name"].(string)
		p5, has5 := asNumber(m["current_interval_remaining_percent"])
		pw, hasw := asNumber(m["current_weekly_remaining_percent"])
		r5, _ := asNumber(m["remains_time"])
		rw, _ := asNumber(m["weekly_remains_time"])
		if has5 && hasw {
			// Width-pinned format: model(1) + ":"(1) + "100%"(4) +
			// "/"(1) + "100%"(4) + "/"(1) + "Xh"(2) + "/"(1) + "Xd"(2)
			// = 17 chars per model, stable.
			parts = append(parts, fmt.Sprintf("%s:%3d%%/%3d%%/%s/%s", shortModel(name), int(p5), int(pw), shortHours(r5), shortDays(rw)))
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("no usable model_remains entries")
	}
	return strings.Join(parts, " "), nil
}

// fetchDeepseekBalance queries the DeepSeek account balance endpoint
// and returns just the formatted balance (e.g. "¥3.58") WITHOUT a
// leading label — the caller adds the single-letter initial.
func fetchDeepseekBalance(ctx context.Context, p Plan) (string, error) {
	if p.Token == "" {
		return "", fmt.Errorf("token unset")
	}
	data, err := httpGetJSON(ctx, "https://api.deepseek.com/user/balance", map[string]string{
		"Authorization": "Bearer " + p.Token,
		"Accept":        "application/json",
		"User-Agent":    UserAgent,
	})
	if err != nil {
		return "", err
	}
	if avail, _ := data["is_available"].(bool); !avail {
		return "", fmt.Errorf("account not available")
	}
	raw, _ := data["balance_infos"].([]any)
	if len(raw) == 0 {
		return "", fmt.Errorf("no balance info")
	}
	first, _ := raw[0].(map[string]any)
	cur, _ := first["currency"].(string)
	if cur == "" {
		cur = "CNY"
	}
	bal, _ := first["total_balance"].(string)
	sym := "¥"
	if cur != "CNY" {
		sym = cur + " "
	}
	return sym + bal, nil
}

// shortModel collapses long model names to a single char so the bar
// stays compact. Unknown names fall through as their first letter.
func shortModel(name string) string {
	switch name {
	case "general":
		return "g"
	case "video":
		return "v"
	case "audio", "speech":
		return "a"
	case "music":
		return "m"
	case "image":
		return "i"
	}
	if name == "" {
		return "?"
	}
	return strings.ToLower(name[:1])
}

// shortHours rounds seconds down to whole hours. Always 2 chars
// ("0h".."5h") for the 5-hour window so segment width is stable.
func shortHours(seconds float64) string {
	h := int(seconds / 3600)
	if h < 0 {
		h = 0
	}
	if h > 9 {
		h = 9
	}
	return fmt.Sprintf("%dh", h)
}

// shortDays rounds seconds down to whole days. Always 2 chars
// ("0d".."7d") for the weekly window so segment width is stable.
func shortDays(seconds float64) string {
	d := int(seconds / 86400)
	if d < 0 {
		d = 0
	}
	if d > 9 {
		d = 9
	}
	return fmt.Sprintf("%dd", d)
}

// keep log imported for future conditional logging
var _ = log.Printf

// planFetchers is the registry of known provider fetchers. New providers
// are added by extending this map.
var planFetchers = map[string]PlanFetcher{
	"minimax_coding_plan": fetchMinimaxCodingPlan,
	"deepseek_balance":    fetchDeepseekBalance,
}
