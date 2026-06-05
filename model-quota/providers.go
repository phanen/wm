package modelquota

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// fetchMinimaxCodingPlan queries the MiniMax "Coding Plan" (a.k.a. Token
// Plan) quota endpoint and formats the 5-hour and weekly windows as
// "<model>:5h=N%(reset) wk=N%(reset)" segments, one per model_remains entry.
//
// The response also reports a permille "boost" multiplier and several
// status fields; these are intentionally ignored because the rendered
// percent and reset duration are the only signals a status bar needs.
func fetchMinimaxCodingPlan(ctx context.Context, p Plan) (string, error) {
	base := "https://api.minimax.io"
	if p.Region == "cn" || p.Region == "minimax_cn" {
		base = "https://api.minimaxi.com"
	}
	token := p.token()
	if token == "" {
		return "", fmt.Errorf("token unset")
	}
	url := base + "/v1/api/openplatform/coding_plan/remains"
	log.Printf("model-quota: [%s] GET %s", p.Name, url)
	data, err := httpGetJSON(ctx, url, map[string]string{
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
			log.Printf("model-quota: [%s] api status=%v msg=%s", p.Name, code, msg)
			if code == 1004 {
				return "", fmt.Errorf("token invalid or expired")
			}
			return "", fmt.Errorf("api error %v: %s", code, msg)
		}
	}
	raw, _ := data["model_remains"].([]any)
	log.Printf("model-quota: [%s] %d model_remains entries", p.Name, len(raw))
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
		if name == "" {
			name = "?"
		}
		p5, has5 := asNumber(m["current_interval_remaining_percent"])
		pw, hasw := asNumber(m["current_weekly_remaining_percent"])
		r5, _ := asNumber(m["remains_time"])
		rw, _ := asNumber(m["weekly_remains_time"])
		if has5 && hasw {
			parts = append(parts, fmt.Sprintf("%s:5h=%d%%/%s wk=%d%%/%s", name, int(p5), fmtDur(r5), int(pw), fmtDur(rw)))
		} else {
			parts = append(parts, name+":--")
		}
	}
	return p.displayLabel() + " " + strings.Join(parts, " "), nil
}

// fetchDeepseekBalance queries the DeepSeek account balance endpoint and
// formats it as "<label> <sym><balance>".
func fetchDeepseekBalance(ctx context.Context, p Plan) (string, error) {
	token := p.token()
	if token == "" {
		return "", fmt.Errorf("token unset")
	}
	url := "https://api.deepseek.com/user/balance"
	log.Printf("model-quota: [%s] GET %s", p.Name, url)
	data, err := httpGetJSON(ctx, url, map[string]string{
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
	log.Printf("model-quota: [%s] %d balance entries", p.Name, len(raw))
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

// planFetchers is the registry of known provider fetchers. New providers
// are added by extending this map.
var planFetchers = map[string]PlanFetcher{
	"minimax_coding_plan": fetchMinimaxCodingPlan,
	"deepseek_balance":    fetchDeepseekBalance,
}
