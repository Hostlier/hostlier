package fleet

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"time"
)

const JoinTTL = 10 * time.Minute
const DefaultSocket = "/run/hostlier/controller.sock"

// JoinRequest binds the requested enrollment and polling capability to the member key.
type JoinRequest struct {
	MasterURL   string `json:"masterURL"`
	Name        string `json:"name"`
	Hostname    string `json:"hostname"`
	Certificate string `json:"certificate"`
	TokenHash   string `json:"tokenHash"`
	ExpiresAt   int64  `json:"expiresAt"`
	Signature   string `json:"signature"`
}

// JoinPrepare contains local inputs; it never grants access to the master.
type JoinPrepare struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}

// PreparedJoin is returned only through the protected member administration socket.
type PreparedJoin struct {
	Request     JoinRequest `json:"request"`
	Token       string      `json:"token"`
	Previous    string      `json:"previous"`
	PreviousURL string      `json:"previousURL"`
}

// JoinPoll proves possession of the private capability without disclosing a key.
type JoinPoll struct {
	Code  string `json:"code"`
	Token string `json:"token"`
}

// JoinReview is safe to display on the approving master controller's terminal.
type JoinReview struct {
	Code              string `json:"code"`
	Name              string `json:"name"`
	Hostname          string `json:"hostname"`
	Fingerprint       string `json:"fingerprint"`
	MasterURL         string `json:"masterURL"`
	MasterFingerprint string `json:"masterFingerprint"`
	ExpiresAt         int64  `json:"expiresAt"`
	Approved          bool   `json:"approved"`
	Connected         bool   `json:"connected"`
}

// JoinDecision binds confirmation to the exact request displayed before the prompt.
type JoinDecision struct {
	Fingerprint string `json:"fingerprint"`
}

// JoinTokenHash is a verifier, never a capability accepted for polling.
func JoinTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// joinPayload uses a fixed typed representation and a distinct signature domain.
func joinPayload(request JoinRequest) []byte {
	request.Signature = ""
	data, _ := json.Marshal(request)
	return append([]byte("hostlier-fleet-join-v1\n"), data...)
}

// JoinCode identifies an immutable signed intent, so a code cannot be reassigned.
func JoinCode(request JoinRequest) string {
	sum := sha256.Sum256(joinPayload(request))
	code := strings.ToUpper(hex.EncodeToString(sum[:8]))
	return code[:8] + "-" + code[8:]
}

// SignJoin keeps the member's persisted private identity inside its daemon.
func SignJoin(identity Identity, request JoinRequest) (JoinRequest, error) {
	pair, err := tls.X509KeyPair([]byte(identity.Certificate), []byte(identity.PrivateKey))
	if err != nil {
		return request, err
	}
	key, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return request, errors.New("unsupported fleet signing key")
	}
	request.Certificate = identity.Certificate
	digest := sha256.Sum256(joinPayload(request))
	signature, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	request.Signature = hex.EncodeToString(signature)
	return request, err
}

// VerifyJoin checks identity possession before a public request can enter review.
func VerifyJoin(request JoinRequest, now time.Time) (string, error) {
	if !ValidHostName(request.Name) || request.Name == "local" || !ValidURL(request.MasterURL) || !ValidPin(request.TokenHash) || request.Hostname == "" || len(request.Hostname) > 253 || len(request.Certificate) > 8192 || len(request.Signature) > 256 {
		return "", errors.New("invalid controller join request")
	}
	if request.ExpiresAt <= now.UnixMilli() || request.ExpiresAt > now.Add(JoinTTL+30*time.Second).UnixMilli() {
		return "", errors.New("controller join request expired or outside its allowed lifetime")
	}
	pin, err := Fingerprint(request.Certificate)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode([]byte(request.Certificate))
	if err := verifyPeer([][]byte{block.Bytes}, pin, x509.ExtKeyUsageClientAuth); err != nil {
		return "", err
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	signature, err := hex.DecodeString(request.Signature)
	if err != nil {
		return "", err
	}
	if err := cert.CheckSignature(x509.ECDSAWithSHA256, joinPayload(request), signature); err != nil {
		return "", err
	}
	return pin, nil
}
