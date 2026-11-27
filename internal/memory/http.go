package memory

import (
	"net/http"
	"net/url"
	"strings"
)

func validDestination(r *http.Request, destination, prefix string) bool {
	u, e := url.Parse(destination)
	if e != nil || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return false
	}
	if u.Host != "" && u.Host != r.Host {
		return false
	}
	return strings.HasPrefix(u.Path, prefix+"/")
}
