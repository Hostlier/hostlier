package model

import (
	"net/url"
	"testing"
)

func TestHistoryFilterBounds(t *testing.T) {
	for _, query := range []string{"from=no-date", "from=2026-11-02T10:00:00Z&to=2026-11-02T09:00:00Z", "from=2026-01-01T00:00:00Z&to=2026-11-02T00:00:00Z", "limit=201", "limit=-1", "before=-1", "beforeTime=2026-11-02T00:00:00Z", "level=unknown", "q=a&q=b"} {
		q, _ := url.ParseQuery(query)
		if _, err := ParseHistoryFilter(q); err == nil {
			t.Fatal("accepted", query)
		}
	}
	q, _ := url.ParseQuery("from=2026-11-02T08:00:00Z&to=2026-11-02T10:00:00Z&q=hello%25&level=ERROR&limit=20")
	f, err := ParseHistoryFilter(q)
	if err != nil || f.Search != "hello%" || f.Level != "error" || f.Limit != 20 {
		t.Fatal(f, err)
	}
}
