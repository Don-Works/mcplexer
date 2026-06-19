package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	keyHawkID        = "HAWK_ID"
	keyHawkKey       = "HAWK_KEY"
	keyHawkAlgorithm = "HAWK_ALGORITHM"
	keyHawkExt       = "HAWK_EXT"
	keyHawkApp       = "HAWK_APP"
	keyHawkDlg       = "HAWK_DLG"

	defaultHawkAlgorithm = "sha256"
)

type hawkCredentials struct {
	ID        string
	Key       string
	Algorithm string
	Ext       string
	App       string
	Dlg       string
}

// ApplyToRequest injects auth for a concrete outbound HTTP request. Most
// scope types still reduce to static headers; Hawk is request-bound and must
// be signed after method, URL, headers, and body are known.
func (inj *Injector) ApplyToRequest(
	ctx context.Context, authScopeID string, req *http.Request, body []byte,
) error {
	if authScopeID == "" {
		return nil
	}
	if req == nil {
		return fmt.Errorf("nil HTTP request")
	}

	if inj.store != nil {
		scope, err := inj.store.GetAuthScope(ctx, authScopeID)
		if err == nil && scope.Type == "hawk" {
			return inj.applyHawk(ctx, authScopeID, req, body)
		}
	}

	headers, err := inj.HeadersForDownstream(ctx, authScopeID)
	if err != nil {
		return err
	}
	for k, vals := range headers {
		for _, v := range vals {
			req.Header.Set(k, v)
		}
	}
	return nil
}

func (inj *Injector) applyHawk(
	ctx context.Context, authScopeID string, req *http.Request, body []byte,
) error {
	if inj.secrets == nil {
		return fmt.Errorf("no secrets manager for hawk scope %s", authScopeID)
	}
	creds, err := inj.hawkCredentials(ctx, authScopeID)
	if err != nil {
		return err
	}
	nonce, err := randomHawkNonce()
	if err != nil {
		return err
	}
	header, err := buildHawkAuthorization(req, body, creds, time.Now().UTC(), nonce)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", header)
	return nil
}

func (inj *Injector) hawkCredentials(ctx context.Context, authScopeID string) (hawkCredentials, error) {
	id, err := inj.requiredSecret(ctx, authScopeID, keyHawkID, "HAWK_KEY_ID", "API_KEY_ID", "api_key_id", "id")
	if err != nil {
		return hawkCredentials{}, err
	}
	key, err := inj.requiredSecret(ctx, authScopeID, keyHawkKey, "HAWK_SECRET", "API_KEY", "api_key", "key")
	if err != nil {
		return hawkCredentials{}, err
	}
	algorithm := inj.optionalSecret(ctx, authScopeID, keyHawkAlgorithm, "ALGORITHM", "algorithm")
	if algorithm == "" {
		algorithm = defaultHawkAlgorithm
	}
	return hawkCredentials{
		ID:        id,
		Key:       key,
		Algorithm: algorithm,
		Ext:       inj.optionalSecret(ctx, authScopeID, keyHawkExt, "ext"),
		App:       inj.optionalSecret(ctx, authScopeID, keyHawkApp, "app"),
		Dlg:       inj.optionalSecret(ctx, authScopeID, keyHawkDlg, "dlg"),
	}, nil
}

func (inj *Injector) requiredSecret(
	ctx context.Context, scopeID string, keys ...string,
) (string, error) {
	for _, key := range keys {
		val, err := inj.secrets.Get(ctx, scopeID, key)
		if err == nil {
			s := strings.TrimSpace(string(val))
			if s != "" {
				return s, nil
			}
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", fmt.Errorf("get %s for scope %s: %w", key, scopeID, err)
		}
	}
	return "", fmt.Errorf("hawk scope %s missing required secret %s", scopeID, keys[0])
}

func (inj *Injector) optionalSecret(ctx context.Context, scopeID string, keys ...string) string {
	for _, key := range keys {
		val, err := inj.secrets.Get(ctx, scopeID, key)
		if err == nil {
			return strings.TrimSpace(string(val))
		}
	}
	return ""
}

func buildHawkAuthorization(
	req *http.Request, body []byte, creds hawkCredentials, now time.Time, nonce string,
) (string, error) {
	if strings.TrimSpace(creds.ID) == "" {
		return "", fmt.Errorf("hawk id is required")
	}
	if creds.Key == "" {
		return "", fmt.Errorf("hawk key is required")
	}
	if nonce == "" {
		return "", fmt.Errorf("hawk nonce is required")
	}
	if err := validateHawkAttrs(creds); err != nil {
		return "", err
	}

	algorithm := strings.TrimSpace(creds.Algorithm)
	if algorithm == "" {
		algorithm = defaultHawkAlgorithm
	}
	hashFn, err := hawkHashFunc(algorithm)
	if err != nil {
		return "", err
	}

	ts := strconv.FormatInt(now.Unix(), 10)
	payloadHash := ""
	if len(body) > 0 {
		payloadHash = hawkPayloadHash(hashFn, req.Header.Get("Content-Type"), body)
	}

	normalized, err := normalizedHawkString(req, ts, nonce, payloadHash, creds.Ext, creds.App, creds.Dlg)
	if err != nil {
		return "", err
	}

	mac := hmac.New(hashFn, []byte(creds.Key))
	_, _ = mac.Write([]byte(normalized))
	macValue := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	attrs := []string{
		hawkAttr("id", creds.ID),
		hawkAttr("ts", ts),
		hawkAttr("nonce", nonce),
	}
	if payloadHash != "" {
		attrs = append(attrs, hawkAttr("hash", payloadHash))
	}
	if creds.Ext != "" {
		attrs = append(attrs, hawkAttr("ext", creds.Ext))
	}
	if creds.App != "" {
		attrs = append(attrs, hawkAttr("app", creds.App))
	}
	if creds.Dlg != "" {
		attrs = append(attrs, hawkAttr("dlg", creds.Dlg))
	}
	attrs = append(attrs, hawkAttr("mac", macValue))
	return "Hawk " + strings.Join(attrs, ", "), nil
}

func normalizedHawkString(
	req *http.Request, ts, nonce, payloadHash, ext, app, dlg string,
) (string, error) {
	host, port := hawkHostPort(req)
	if host == "" {
		return "", fmt.Errorf("hawk request host is required")
	}
	resource := req.URL.RequestURI()
	if resource == "" {
		resource = "/"
	}

	var b strings.Builder
	b.WriteString("hawk.1.header\n")
	b.WriteString(ts)
	b.WriteByte('\n')
	b.WriteString(nonce)
	b.WriteByte('\n')
	b.WriteString(strings.ToUpper(req.Method))
	b.WriteByte('\n')
	b.WriteString(resource)
	b.WriteByte('\n')
	b.WriteString(strings.ToLower(host))
	b.WriteByte('\n')
	b.WriteString(port)
	b.WriteByte('\n')
	b.WriteString(payloadHash)
	b.WriteByte('\n')
	b.WriteString(ext)
	b.WriteByte('\n')
	if app != "" || dlg != "" {
		b.WriteString(app)
		b.WriteByte('\n')
		b.WriteString(dlg)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func hawkHostPort(req *http.Request) (string, string) {
	hostport := req.Host
	if hostport == "" && req.URL != nil {
		hostport = req.URL.Host
	}
	if hostport == "" {
		return "", ""
	}
	if host, port, err := net.SplitHostPort(hostport); err == nil {
		return strings.Trim(host, "[]"), port
	}
	host := hostport
	if strings.HasPrefix(host, "[") && strings.Contains(host, "]") {
		host = strings.Trim(host, "[]")
	} else if i := strings.LastIndex(host, ":"); i > -1 && strings.Count(host, ":") == 1 {
		host = host[:i]
	}
	port := ""
	if req.URL != nil {
		port = req.URL.Port()
		if port == "" {
			switch strings.ToLower(req.URL.Scheme) {
			case "http":
				port = "80"
			case "https":
				port = "443"
			}
		}
	}
	return host, port
}

func hawkPayloadHash(hashFn func() hash.Hash, contentType string, body []byte) string {
	h := hashFn()
	normalizedContentType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	_, _ = h.Write([]byte("hawk.1.payload\n"))
	_, _ = h.Write([]byte(normalizedContentType))
	_, _ = h.Write([]byte("\n"))
	_, _ = h.Write(body)
	_, _ = h.Write([]byte("\n"))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func hawkHashFunc(algorithm string) (func() hash.Hash, error) {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(algorithm), "-", "")) {
	case "sha1":
		return sha1.New, nil
	case "sha256":
		return sha256.New, nil
	default:
		return nil, fmt.Errorf("unsupported hawk algorithm %q", algorithm)
	}
}

func hawkAttr(name, value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return fmt.Sprintf(`%s="%s"`, name, escaped)
}

func validateHawkAttrs(creds hawkCredentials) error {
	values := map[string]string{
		"id":  creds.ID,
		"ext": creds.Ext,
		"app": creds.App,
		"dlg": creds.Dlg,
	}
	for name, value := range values {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("hawk %s contains invalid newline", name)
		}
	}
	return nil
}

func randomHawkNonce() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate hawk nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
