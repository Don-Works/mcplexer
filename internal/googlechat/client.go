package googlechat

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
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

	"github.com/don-works/mcplexer/internal/store"
)

// ServiceAccountKey is the subset of a Google service account JSON file we
// read. We sign our own JWT (RS256) rather than pulling in google.golang.org
// /api or x/oauth2 — keeps the dep surface trivial and the signing flow
// auditable on a small implementation.
type ServiceAccountKey struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"` // PEM-encoded RSA
	ClientEmail  string `json:"client_email"`
	ClientID     string `json:"client_id"`
	TokenURI     string `json:"token_uri"`
}

// ParseServiceAccountKey unmarshals a service account JSON blob and validates
// the parts we need to sign tokens.
func ParseServiceAccountKey(raw []byte) (*ServiceAccountKey, error) {
	var k ServiceAccountKey
	if err := json.Unmarshal(raw, &k); err != nil {
		return nil, fmt.Errorf("googlechat: parse service account: %w", err)
	}
	if k.ClientEmail == "" || k.PrivateKey == "" || k.TokenURI == "" {
		return nil, fmt.Errorf("googlechat: service account missing client_email/private_key/token_uri")
	}
	return &k, nil
}

// Client is the Google Chat REST transport. Sends messages, exchanges the
// service account assertion for an access token, and caches the token until
// near-expiry.
type Client struct {
	httpc *http.Client
	key   *ServiceAccountKey

	mu          sync.Mutex
	accessToken string
	accessExp   time.Time

	// botName is the bot's user resource name (e.g. "users/12345") used by
	// the parse layer for mention detection. Empty until SetBotName is
	// called (the daemon learns this lazily from the first inbound event).
	botName string
}

// chatScope is the OAuth scope we request when exchanging our signed JWT for
// an access token.
const chatScope = "https://www.googleapis.com/auth/chat.bot"

// NewClient constructs a Client from a service account key.
func NewClient(key *ServiceAccountKey) *Client {
	return &Client{
		httpc: &http.Client{Timeout: 15 * time.Second},
		key:   key,
	}
}

// SetBotName records the bot's resource name (e.g. "users/12345") so the
// parse layer can match annotations against it.
func (c *Client) SetBotName(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.botName = name
}

// BotName returns the recorded bot resource name, or "" if not set.
func (c *Client) BotName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.botName
}

// Send delivers an OutgoingMessage to a space. Returns the native message id
// (last segment of the message resource name) for threading.
func (c *Client) Send(ctx context.Context, space store.GoogleChatSpace, msg OutgoingMessage) (string, error) {
	body := map[string]any{
		"text": RenderText(msg),
	}
	if msg.ThreadName != "" {
		body["thread"] = map[string]string{"name": msg.ThreadName}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("googlechat: encode body: %w", err)
	}

	url := fmt.Sprintf("https://chat.googleapis.com/v1/%s/messages", space.SpaceName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("googlechat: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	tok, err := c.accessTokenFor(ctx)
	if err != nil {
		return "", fmt.Errorf("googlechat: access token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", fmt.Errorf("googlechat: http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("googlechat: send %d: %s", resp.StatusCode, truncate(string(respBody), 256))
	}
	var sent struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(respBody, &sent); err != nil {
		return "", nil // success — but unparseable. Treat as no native id.
	}
	return lastSegment(sent.Name), nil
}

// accessTokenFor fetches (and caches) a Google access token for the bot
// scope. Tokens are valid for an hour; we re-fetch when less than 60s
// remain on the cached one.
func (c *Client) accessTokenFor(ctx context.Context) (string, error) {
	c.mu.Lock()
	tok, exp := c.accessToken, c.accessExp
	c.mu.Unlock()
	if tok != "" && time.Until(exp) > time.Minute {
		return tok, nil
	}

	signed, err := c.signAssertion()
	if err != nil {
		return "", err
	}
	form := strings.NewReader(
		"grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Ajwt-bearer&assertion=" + signed,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.key.TokenURI, form)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("googlechat: token %d: %s", resp.StatusCode, truncate(string(body), 256))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("googlechat: token decode: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("googlechat: empty access_token from %s", c.key.TokenURI)
	}
	c.mu.Lock()
	c.accessToken = out.AccessToken
	if out.ExpiresIn > 0 {
		c.accessExp = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	} else {
		c.accessExp = time.Now().Add(45 * time.Minute)
	}
	c.mu.Unlock()
	return out.AccessToken, nil
}

// signAssertion creates a signed JWT (RS256) suitable for the jwt-bearer
// grant. Claims follow Google's OAuth 2.0 service account spec:
//
//	iss = service account email
//	scope = chat.bot
//	aud = token URI
//	exp = now + 1h, iat = now
func (c *Client) signAssertion() (string, error) {
	now := time.Now().Unix()
	header := map[string]string{
		"alg": "RS256",
		"typ": "JWT",
		"kid": c.key.PrivateKeyID,
	}
	claims := map[string]any{
		"iss":   c.key.ClientEmail,
		"scope": chatScope,
		"aud":   c.key.TokenURI,
		"exp":   now + 3600,
		"iat":   now,
	}
	hdrJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64URL(hdrJSON) + "." + base64URL(claimsJSON)

	priv, err := parseRSAPrivateKey(c.key.PrivateKey)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, h[:])
	if err != nil {
		return "", fmt.Errorf("googlechat: sign: %w", err)
	}
	return signingInput + "." + base64URL(sig), nil
}

func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("googlechat: no PEM block in private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("googlechat: parse private key: %w", err)
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("googlechat: expected RSA private key, got %T", parsed)
	}
	return rsaKey, nil
}

func base64URL(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
