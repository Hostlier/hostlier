package controller

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hostlier/internal/model"
	"hostlier/internal/nginx"
)

func TestNginxFailureStageStaysInProtectedLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hostlier.log")
	op := &Operation{ID: "operation-1", Kind: "deploy", Name: "app"}
	c := &Controller{opts: Options{LogPath: path, Now: time.Now}, state: State{Operations: map[string]*Operation{op.ID: op}}}
	err := &nginx.ActivationError{Stage: "tls-cutover", Err: &nginx.ActivationError{Stage: "certificate-issuance", Err: errors.New("private certbot response")}}
	c.failOperation(op, err)
	if op.Error == nil || op.Error.Code != "RUNTIME_FAILED" || strings.Contains(op.Error.Message, "certificate") {
		t.Fatal("private activation detail leaked into operation response", op.Error)
	}
	for _, event := range c.state.Events {
		if strings.Contains(event.Message, "certificate") || strings.Contains(event.Message, "private") {
			t.Fatal("private activation detail leaked into public events", event)
		}
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || !strings.Contains(string(data), "tls-cutover/certificate-issuance") || strings.Contains(string(data), "private certbot response") {
		t.Fatal("protected diagnostic did not retain only the fixed stage", readErr, string(data))
	}
	info, statErr := os.Stat(path)
	if statErr != nil || info.Mode().Perm() != 0600 {
		t.Fatal("diagnostic log is not owner-only", statErr)
	}
	if stage := protectedFailureStage(model.Err("OTHER", "private")); stage != "" {
		t.Fatal("untrusted runtime error became an activation stage", stage)
	}
}
