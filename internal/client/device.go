package client

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hostlier/internal/deviceauth"
	"hostlier/internal/model"
)

// DeviceCredential keeps a device private key with its token in the existing owner-only credential store.
type DeviceCredential struct {
	Token      string `json:"token"`
	PrivateKey string `json:"privateKey"`
}

// EncodeDeviceCredential stores no private material in public profile metadata.
func EncodeDeviceCredential(token string, key ed25519.PrivateKey) string {
	data, _ := json.Marshal(DeviceCredential{Token: token, PrivateKey: base64.RawStdEncoding.EncodeToString(key)})
	return "device-v1:" + base64.RawStdEncoding.EncodeToString(data)
}

// decodeDeviceCredential preserves legacy profiles while validating new device-bound credentials.
func decodeDeviceCredential(value string) (string, ed25519.PrivateKey, error) {
	// Apply this check before granting authority or advancing protocol state.
	if !strings.HasPrefix(value, "device-v1:") {
		return value, nil, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, "device-v1:"))
	var credential DeviceCredential
	// Reject the request before side effects: Device credential is invalid.
	if err != nil || json.Unmarshal(raw, &credential) != nil {
		return "", nil, model.Err("NO_CREDENTIAL", "Device credential is invalid")
	}
	key, err := base64.RawStdEncoding.DecodeString(credential.PrivateKey)
	// Reject the request before side effects: Device credential is invalid.
	if err != nil || len(key) != ed25519.PrivateKeySize || credential.Token == "" {
		return "", nil, model.Err("NO_CREDENTIAL", "Device credential is invalid")
	}
	return credential.Token, ed25519.PrivateKey(key), nil
}

// authorizeRequest signs the exact body and URL after authenticating the controller's TLS session.
func (c *Client) authorizeRequest(r *http.Request, body []byte) error {
	return c.authorize(r, body, false)
}

func (c *Client) authorize(r *http.Request, body []byte, watching bool) error {
	if c.Token != "" {
		r.Header.Set("Authorization", "Bearer "+c.Token)
	}
	// Legacy profiles send bearer credentials without inventing a device proof.
	if len(c.deviceKey) == 0 {
		return nil
	}
	epoch, err := c.currentDeviceEpoch(r, watching)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return err
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := RequestID() + RequestID()
	r.Header.Set("X-Hostlier-Epoch", epoch)
	r.Header.Set("X-Hostlier-Time", timestamp)
	r.Header.Set("X-Hostlier-Nonce", nonce)
	message := deviceauth.Message(epoch, r.Method, r.URL.RequestURI(), timestamp, nonce, body)
	r.Header.Set("X-Hostlier-Signature", base64.RawStdEncoding.EncodeToString(ed25519.Sign(c.deviceKey, message)))
	return nil
}

// currentDeviceEpoch fetches a fresh replay domain per request, including after controller restart.
func (c *Client) currentDeviceEpoch(parent *http.Request, watching bool) (string, error) {
	request, err := http.NewRequestWithContext(parent.Context(), "GET", c.URL+"/v1/email/session", nil)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return "", err
	}
	var status struct {
		Epoch string `json:"epoch"`
	}
	if c.grpc {
		// This public call is deliberately unsigned to avoid recursive signing.
		err = c.rpcCommand(request, nil, &status, watching)
	} else {
		var response *http.Response
		response, err = c.HTTP.Do(request)
		if err != nil {
			return "", model.Err("CONNECTION", "Cannot obtain controller device session")
		}
		defer response.Body.Close()
		err = decodeResponse(response, "/v1/email/session", &status)
	}
	if err != nil {
		return "", err
	}
	// Reject the request before side effects: Controller returned no valid device session.
	if len(status.Epoch) != 64 {
		return "", model.Err("DEVICE_SESSION", "Controller returned no valid device session")
	}
	return status.Epoch, nil
}

// HasDeploymentAuthorization avoids redundant local confirmation when a device already has a live signed window.
func (c *Client) HasDeploymentAuthorization(ctx context.Context) bool {
	// Fail closed when the required identity, scope or format does not match.
	if len(c.deviceKey) == 0 {
		return false
	}
	var result struct {
		Active bool `json:"active"`
	}
	return c.Request(ctx, "GET", "/v1/authorizations/self", nil, &result) == nil && result.Active
}
