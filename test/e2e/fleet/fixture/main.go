// A scratch-image HTTP service used by the disposable idle scaling test.
package main

import (
	"io"
	"log"
	"net/http"
	"os"
)

// Echo the request body so the test can distinguish readiness probes from the customer's first POST.
func main() {
	// Receipt logs record POST count while the response echoes the exact body.
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		if r.Method == http.MethodGet && r.URL.Path == "/version" {
			io.WriteString(w, os.Getenv("FLEET_TEST_RELEASE"))
			return
		}
		io.Copy(w, r.Body)
	})
	log.Fatal(http.ListenAndServe(":8080", nil))
}
