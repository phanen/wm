package modelquota

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// fetchMinimaxCodingPlan queries the MiniMax "Coding Plan" (a.k.a. Token
// Plan) quota endpoint and renders each model as
// `<model>:<5h%>/<wk%>/<5h-time%>/<wk-time%>`, all four values
// right-aligned to 3 chars (so segment width is stable at 17 chars),
// no `%` sign anywhere. The time fields are the percent of the
// window still remaining (e.g. 4h left in a 5h window = 80), so the
// bar drops when the window is about to reset.
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
			// Natural-width format: 4 fields × (1-3 chars) joined by `/`
			// = 10-17 chars per model, hard-capped by the field count
			// (no leading spaces; smaller values pack tighter). The
			// total bar width is therefore bounded by 17 chars/model.
			parts = append(parts, fmt.Sprintf("%s:%d/%d/%d/%d",
				shortModel(name), int(p5), int(pw),
				windowPct(r5, m["start_time"], m["end_time"]),
				windowPct(rw, m["weekly_start_time"], m["weekly_end_time"]),
			))
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

// windowPct returns the percent of the total window still
// remaining. All three values (remaining, startMs, endMs) are in
// the same unit (milliseconds — see the API's `remains_time`,
// `start_time`, `end_time` fields), so no conversion is needed;
// just clamp to [0, 100] for stable bar width.
func windowPct(remainingMs float64, startMs, endMs any) int {
	start, sok := asNumber(startMs)
	end, eok := asNumber(endMs)
	if !sok || !eok || end <= start {
		return 0
	}
	totalMs := end - start
	if totalMs <= 0 {
		return 0
	}
	pct := int(100 * remainingMs / totalMs)
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// keep log imported for future conditional logging
var _ = log.Printf

// planFetchers is the registry of known provider fetchers. New providers
// are added by extending this map.
var planFetchers = map[string]PlanFetcher{
	"minimax_coding_plan": fetchMinimaxCodingPlan,
	"deepseek_balance":    fetchDeepseekBalance,
}
