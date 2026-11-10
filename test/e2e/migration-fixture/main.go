// A disposable, nonroot stateful service for the runtime migration VM gate.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// main serves an isolated stateful application or executes its readiness probe.
func main() {
	// Reject incompatible state before continuing the protected migration step.
	if len(os.Args) > 1 && os.Args[1] == "health" {
		probeHealth()
		return
	}
	http.HandleFunc("/ready", serveReady)
	http.HandleFunc("/data", serveData)
	http.HandleFunc("/storage", serveStorage)
	http.HandleFunc("/", serveIdentity)
	log.Fatal(http.ListenAndServe(":"+os.Getenv("PORT"), nil))
}

// probeHealth is shared by Docker exec and Kubernetes sidecar startup probes.
func probeHealth() {
	client := http.Client{Timeout: 3 * time.Second}
	res, err := client.Get(os.Args[2])
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil || res.StatusCode != 200 {
		os.Exit(1)
	}
	res.Body.Close()
}

// serveReady verifies actual process confinement instead of manifest intent.
func serveReady(w http.ResponseWriter, r *http.Request) {
	// Preserve failure before advancing to the next external-effect boundary.
	if err := readiness(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	fmt.Fprint(w, "ready")
}

// readiness verifies the nonroot process, read-only root and bounded writable scratch.
func readiness() error {
	status, err := os.ReadFile("/proc/self/status")
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if !processConfined(string(status)) {
		return fmt.Errorf("confinement")
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := os.WriteFile("/must-stay-readonly", []byte("bad"), 0600); err == nil {
		return fmt.Errorf("root filesystem writable")
	}
	f, err := os.CreateTemp("/tmp", "scratch-")
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return fmt.Errorf("scratch unavailable")
	}
	f.Close()
	os.Remove(f.Name())
	// Preserve failure before advancing to the next external-effect boundary.
	if err := scratchReady(); err != nil {
		return err
	}
	return credentialsReady()
}

// processConfined checks effective UID, privilege transitions and effective capabilities.
func processConfined(status string) bool {
	return os.Getuid() == 1000 && strings.Contains(status, "NoNewPrivs:\t1") && strings.Contains(status, "CapEff:\t0000000000000000")
}

// scratchReady checks the requested sticky permissions on the actual tmpfs mount.
func scratchReady() error {
	info, err := os.Stat("/tmp")
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil || info.Mode().Perm() != 0777 || info.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("scratch mode")
	}
	return nil
}

// credentialsReady verifies both pinned environment and file-secret materialization.
func credentialsReady() error {
	// Reject incompatible state before continuing the protected migration step.
	if os.Getenv("TOKEN") == "" {
		return nil
	}
	secret, err := os.ReadFile("/run/secrets/credential")
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil || string(secret) != "migration-file-secret" || os.Getenv("TOKEN") != "migration-env-secret" {
		return fmt.Errorf("pinned credentials")
	}
	return nil
}

// serveData exposes a small durable value for before/after/rollback comparisons.
func serveData(w http.ResponseWriter, r *http.Request) {
	data := filepath.Join(os.Getenv("DATA_PATH"), "value")
	// Reject incompatible state before continuing the protected migration step.
	if r.Method == "POST" {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		// Preserve failure before advancing to the next external-effect boundary.
		if err != nil || os.WriteFile(data, body, 0600) != nil {
			http.Error(w, "write failed", 500)
			return
		}
	}
	body, err := os.ReadFile(data)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		http.Error(w, "data unavailable", 500)
		return
	}
	w.Write(body)
}

// serveStorage verifies the sidecar's separate volume through the shared network namespace.
func serveStorage(w http.ResponseWriter, r *http.Request) {
	request, err := http.NewRequest(r.Method, "http://127.0.0.1:8081/data", r.Body)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		http.Error(w, "request", 500)
		return
	}
	response, err := http.DefaultClient.Do(request)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		http.Error(w, "sidecar unavailable", 500)
		return
	}
	defer response.Body.Close()
	w.WriteHeader(response.StatusCode)
	io.Copy(w, response.Body)
}

// serveIdentity exposes only public fixture identity.
func serveIdentity(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]any{"uid": os.Getuid(), "port": os.Getenv("PORT")})
}
