package localui

import (
	"hostlier/internal/model"
	"net/http"
	"net/url"
	"strconv"
)

// historyReadPath forwards only validated selectors to the controller-owned history.
func historyReadPath(r *http.Request) (string, error) {
	f, err := model.ParseHistoryFilter(r.URL.Query())
	if err != nil {
		return "", err
	}
	q := url.Values{"limit": {strconv.Itoa(f.Limit)}}
	for _, key := range []string{"from", "to", "q", "level", "before", "beforeTime", "app", "operationId"} {
		value := r.URL.Query().Get(key)
		if value == "" {
			continue
		}
		if (key == "app" || key == "operationId") && !validReadIdentity(value) {
			return "", model.Err("INVALID_REQUEST", "Invalid history identity")
		}
		q.Set(key, value)
	}
	return "/v1/activity?" + q.Encode(), nil
}
