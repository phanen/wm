package modelquota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// httpGetJSON issues a GET with the supplied headers and decodes the
// response body as JSON. Non-2xx responses are returned as errors with a
// short snippet of the body for diagnostics.
func httpGetJSON(ctx context.Context, url string, headers map[string]string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// asNumber coerces a JSON-decoded value to float64. Returns (0, false) for
// non-numeric or missing values.
func asNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// fmtDur renders a seconds count as a short human-readable string.
// Examples: 45 -> "45s", 130 -> "2m", 3700 -> "1h1m", 90000 -> "1d1h".
func fmtDur(seconds float64) string {
	s := max(int(seconds), 0)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm", s/60)
	case s < 86400:
		h, rem := s/3600, s%3600
		m := rem / 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		d, rem := s/86400, s%86400
		return fmt.Sprintf("%dd%dh", d, rem/3600)
	}
}
