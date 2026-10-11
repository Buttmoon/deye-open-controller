package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
)

const (
	kvSyncAPIAuthScheme   = "sync_api_auth_scheme"
	kvSyncAPIAuthToken    = "sync_api_auth_token"
	kvSyncAPIAuthUsername = "sync_api_auth_username"
	kvSyncAPIAuthPassword = "sync_api_auth_password"
)

type syncAuthConfig struct {
	Scheme   string
	Token    string
	Username string
	Password string
}

func (a *App) syncAuthConfig() syncAuthConfig {
	return syncAuthConfig{
		Scheme:   strings.ToLower(strings.TrimSpace(a.kvString(kvSyncAPIAuthScheme))),
		Token:    a.kvString(kvSyncAPIAuthToken),
		Username: a.kvString(kvSyncAPIAuthUsername),
		Password: a.kvString(kvSyncAPIAuthPassword),
	}
}

func (c syncAuthConfig) configured() bool {
	switch c.Scheme {
	case "bearer":
		return strings.TrimSpace(c.Token) != ""
	case "basic":
		return strings.TrimSpace(c.Username) != "" || strings.TrimSpace(c.Password) != ""
	case "bearer_or_basic", "both", "any":
		return strings.TrimSpace(c.Token) != "" || strings.TrimSpace(c.Username) != "" || strings.TrimSpace(c.Password) != ""
	default:
		// Auto-detect: any credential present means Sync auth is configured.
		return strings.TrimSpace(c.Token) != "" || strings.TrimSpace(c.Username) != "" || strings.TrimSpace(c.Password) != ""
	}
}

func constantTimeEqualString(a, b string) bool {
	sumA := sha256.Sum256([]byte(a))
	sumB := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(sumA[:], sumB[:]) == 1
}

func (c syncAuthConfig) checkAuthorization(header string) bool {
	header = strings.TrimSpace(header)
	if header == "" || !c.configured() {
		return false
	}
	lower := strings.ToLower(header)
	bearerOK := strings.TrimSpace(c.Token) != ""
	basicOK := strings.TrimSpace(c.Username) != "" || strings.TrimSpace(c.Password) != ""

	allowBearer := bearerOK
	allowBasic := basicOK
	switch c.Scheme {
	case "bearer":
		allowBasic = false
	case "basic":
		allowBearer = false
	}

	if allowBearer && strings.HasPrefix(lower, "bearer ") {
		token := strings.TrimSpace(header[len("Bearer "):])
		if token == "" {
			token = strings.TrimSpace(header[len("bearer "):])
		}
		return constantTimeEqualString(token, c.Token)
	}
	if allowBasic && strings.HasPrefix(lower, "basic ") {
		payload := strings.TrimSpace(header[len("Basic "):])
		if payload == "" {
			payload = strings.TrimSpace(header[len("basic "):])
		}
		raw, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return false
		}
		parts := strings.SplitN(string(raw), ":", 2)
		if len(parts) != 2 {
			return false
		}
		return constantTimeEqualString(parts[0], c.Username) && constantTimeEqualString(parts[1], c.Password)
	}
	return false
}

func writeSyncError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"ok": false,
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

// requireSyncAuth enforces fail-closed Sync credentials.
// Returns false after writing 503/401 when the request must not proceed.
func (a *App) requireSyncAuth(w http.ResponseWriter, r *http.Request) bool {
	cfg := a.syncAuthConfig()
	if !cfg.configured() {
		writeSyncError(w, http.StatusServiceUnavailable, "auth_not_configured", "Sync API credentials are not configured")
		return false
	}
	if !cfg.checkAuthorization(r.Header.Get("Authorization")) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="sync", Basic realm="sync"`)
		writeSyncError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid Authorization")
		return false
	}
	return true
}

func (a *App) saveSyncAuthSettings(scheme, token, username, password string) error {
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	switch scheme {
	case "", "bearer", "basic", "bearer_or_basic", "both", "any":
	default:
		return errInvalidSyncAuthScheme
	}
	if err := a.kvSet(kvSyncAPIAuthScheme, scheme); err != nil {
		return err
	}
	if err := a.kvSet(kvSyncAPIAuthToken, token); err != nil {
		return err
	}
	if err := a.kvSet(kvSyncAPIAuthUsername, username); err != nil {
		return err
	}
	return a.kvSet(kvSyncAPIAuthPassword, password)
}

var errInvalidSyncAuthScheme = errString("invalid sync auth scheme")

type errString string

func (e errString) Error() string { return string(e) }
