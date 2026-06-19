package googlechat

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ChatBotIssuer is the Google service account that signs the bearer JWT on
// every incoming webhook event. Validating against the public keys below
// confirms the event genuinely came from Google.
const ChatBotIssuer = "chat@system.gserviceaccount.com"

// googlePublicKeysURL hosts the x509 certs the inbound JWT is signed against.
// Cached by the verifier; refreshed when a kid miss occurs.
const googlePublicKeysURL = "https://www.googleapis.com/service_accounts/v1/metadata/x509/" + ChatBotIssuer

// JWTVerifier validates Bearer JWTs Google attaches to outgoing events. The
// zero-value is ready to use; first verification fetches + caches the public
// keys.
type JWTVerifier struct {
	httpc *http.Client

	mu       sync.Mutex
	keys     map[string]*rsa.PublicKey
	fetched  time.Time
	audience string // required `aud` claim — usually the bot's project number
}

// NewJWTVerifier returns a verifier bound to the given audience (the bot's
// GCP project number). An empty audience is rejected: every call to Verify
// will return an error. The caller must provide a non-empty project number.
func NewJWTVerifier(audience string) *JWTVerifier {
	return &JWTVerifier{
		httpc:    &http.Client{Timeout: 5 * time.Second},
		audience: audience,
	}
}

// Verify parses a Bearer JWT, checks the signature against Google's public
// keys, validates iss + aud + exp, and returns the parsed claims. Returns
// an error on any failure — the caller MUST refuse the request when this
// errors.
func (v *JWTVerifier) Verify(ctx context.Context, token string) (map[string]any, error) {
	if v.audience == "" {
		return nil, fmt.Errorf("googlechat: jwt: verifier has no audience configured — GOOGLECHAT_BOT_PROJECT_NUMBER is required")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("googlechat: jwt: expected 3 parts, got %d", len(parts))
	}
	hdrBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("googlechat: jwt: decode header: %w", err)
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("googlechat: jwt: decode claims: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("googlechat: jwt: decode sig: %w", err)
	}

	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(hdrBytes, &header); err != nil {
		return nil, fmt.Errorf("googlechat: jwt: parse header: %w", err)
	}
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("googlechat: jwt: alg=%q, want RS256", header.Alg)
	}

	key, err := v.getKey(ctx, header.Kid)
	if err != nil {
		return nil, err
	}
	signingInput := parts[0] + "." + parts[1]
	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return nil, fmt.Errorf("googlechat: jwt: bad signature: %w", err)
	}

	var claims map[string]any
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return nil, fmt.Errorf("googlechat: jwt: parse claims: %w", err)
	}

	if iss, _ := claims["iss"].(string); iss != ChatBotIssuer {
		return nil, fmt.Errorf("googlechat: jwt: iss=%q, want %s", iss, ChatBotIssuer)
	}
	if aud, _ := claims["aud"].(string); aud != v.audience {
		return nil, fmt.Errorf("googlechat: jwt: aud=%q, want %q", aud, v.audience)
	}
	if exp, ok := claims["exp"].(float64); ok {
		if time.Now().Unix() > int64(exp) {
			return nil, fmt.Errorf("googlechat: jwt: token expired")
		}
	}
	return claims, nil
}

// getKey returns the cached public key for the given kid, refreshing the key
// set when the kid is unknown or the cache is older than an hour.
func (v *JWTVerifier) getKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	if k, ok := v.keys[kid]; ok && time.Since(v.fetched) < time.Hour {
		v.mu.Unlock()
		return k, nil
	}
	v.mu.Unlock()

	if err := v.refresh(ctx); err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	k, ok := v.keys[kid]
	if !ok {
		return nil, fmt.Errorf("googlechat: jwt: kid %q not in Google's key set", kid)
	}
	return k, nil
}

func (v *JWTVerifier) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googlePublicKeysURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("googlechat: jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("googlechat: jwks: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var raw map[string]string
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("googlechat: jwks: parse: %w", err)
	}
	out := make(map[string]*rsa.PublicKey, len(raw))
	for kid, pemStr := range raw {
		k, err := parseRSAPublicKey(pemStr)
		if err != nil {
			continue
		}
		out[kid] = k
	}
	v.mu.Lock()
	v.keys = out
	v.fetched = time.Now()
	v.mu.Unlock()
	return nil
}

func parseRSAPublicKey(pemStr string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("googlechat: no PEM block in cert")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	k, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("googlechat: expected RSA public key, got %T", cert.PublicKey)
	}
	return k, nil
}
