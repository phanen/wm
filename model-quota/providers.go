package modelquota

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// fetchMinimaxCodingPlan queries the MiniMax "Coding Plan" (a.k.a. Token
// Plan) quota endpoint and renders each model's 5-hour and weekly
// remaining percentage as a compact `xx%/yy%` segment, joined by
// spaces. Boosts and status fields are ignored.
func fetchMinimaxCodingPlan(ctx context.Context, p Plan) (string, error) {
	base := "https://api.minimax.io"
	if p.Region == "cn" || p.Region == "minimax_cn" {
		base = "https://api.minimaxi.com"
	}
	token := p.token()
	if token == "" {
		return "", fmt.Errorf("token unset")
	}
	data, err := httpGetJSON(ctx, base+"/v1/api/openplatform/coding_plan/remains", map[string]string{
		"Authorization": "Bearer " + token,
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
		p5, has5 := asNumber(m["current_interval_remaining_percent"])
		pw, hasw := asNumber(m["current_weekly_remaining_percent"])
		if has5 && hasw {
			parts = append(parts, fmt.Sprintf("%d%%/%d%%", int(p5), int(pw)))
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("no usable model_remains entries")
	}
	return p.displayLabel() + " " + strings.Join(parts, " "), nil
}

// fetchDeepseekBalance queries the DeepSeek account balance endpoint
// and formats it as "<label> <sym><balance>".
func fetchDeepseekBalance(ctx context.Context, p Plan) (string, error) {
	token := p.token()
	if token == "" {
		return "", fmt.Errorf("token unset")
	}
	data, err := httpGetJSON(ctx, "https://api.deepseek.com/user/balance", map[string]string{
		"Authorization": "Bearer " + token,
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
	return p.displayLabel() + " " + sym + bal, nil
}

// keep log imported for future conditional logging
var _ = log.Printf

// planFetchers is the registry of known provider fetchers. New providers
// are added by extending this map.
var planFetchers = map[string]PlanFetcher{
	"minimax_coding_plan": fetchMinimaxCodingPlan,
	"deepseek_balance":    fetchDeepseekBalance,
}
