// Package auth implements single-admin authentication for HTTP services.
package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	secretBytes          = 32
	encodedSecretLength  = 43 // base64.RawURLEncoding.EncodedLen(secretBytes)
	maxSecretFileBytes   = encodedSecretLength + 1
	defaultCookieName    = "gitci_session"
	defaultSessionTTL    = 8 * time.Hour
	sessionCookieVersion = "v1"
	AdminSubject         = "admin"
)

// AuthMethod identifies how a request was authenticated.
type AuthMethod string

const (
	// AuthMethodBearer identifies a request authenticated with an admin bearer token.
	AuthMethodBearer AuthMethod = "bearer"
	// AuthMethodSession identifies a request authenticated with a signed browser session.
	AuthMethodSession AuthMethod = "session"
)

// Principal is the authenticated identity for a request.
//
// git-ci has one administrative identity, identified by AdminSubject.
type Principal struct {
	Subject string
	Method  AuthMethod
}

// ErrorCode classifies an authentication failure without exposing credentials.
type ErrorCode string

const (
	CodeMissingCredentials ErrorCode = "missing_credentials"
	CodeInvalidBearer      ErrorCode = "invalid_bearer"
	CodeInvalidSession     ErrorCode = "invalid_session"
	CodeExpiredSession     ErrorCode = "expired_session"
	CodeCSRF               ErrorCode = "csrf_failed"
)

// AuthError is a typed authentication error returned by Authenticate and
// AuthenticateBearer. Callers can inspect Code or use errors.Is with the
// exported sentinel errors below.
type AuthError struct {
	Code ErrorCode
}

func (e *AuthError) Error() string {
	if e == nil {
		return "authentication failed"
	}

	switch e.Code {
	case CodeMissingCredentials:
		return "authentication required"
	case CodeInvalidBearer:
		return "invalid bearer token"
	case CodeInvalidSession:
		return "invalid session"
	case CodeExpiredSession:
		return "session expired"
	case CodeCSRF:
		return "csrf validation failed"
	default:
		return "authentication failed"
	}
}

// Is allows errors.Is to compare authentication errors by code.
func (e *AuthError) Is(target error) bool {
	targetError, ok := target.(*AuthError)
	return ok && e != nil && e.Code == targetError.Code
}

var (
	ErrMissingCredentials = &AuthError{Code: CodeMissingCredentials}
	ErrInvalidBearer      = &AuthError{Code: CodeInvalidBearer}
	ErrInvalidSession     = &AuthError{Code: CodeInvalidSession}
	ErrExpiredSession     = &AuthError{Code: CodeExpiredSession}
	ErrCSRF               = &AuthError{Code: CodeCSRF}
)

// Session describes a newly issued browser session. CSRFToken is intended to
// be returned to the browser by the caller and supplied in X-CSRF-Token on
// state-changing cookie-authenticated requests.
type Session struct {
	CSRFToken string
	ExpiresAt time.Time
}

// Option configures a Manager.
type Option func(*managerOptions) error

type managerOptions struct {
	cookieName string
	sessionTTL time.Duration
	now        func() time.Time
}

// WithSessionTTL sets the lifetime used for newly issued browser sessions.
func WithSessionTTL(ttl time.Duration) Option {
	return func(options *managerOptions) error {
		if ttl <= 0 {
			return errors.New("auth: session TTL must be positive")
		}
		options.sessionTTL = ttl
		return nil
	}
}

// WithCookieName sets the name of the browser session cookie.
func WithCookieName(name string) Option {
	return func(options *managerOptions) error {
		if !validCookieName(name) {
			return fmt.Errorf("auth: invalid cookie name %q", name)
		}
		options.cookieName = name
		return nil
	}
}

// WithClock supplies the clock used to issue and validate session expiry.
// It is useful when an application already has a controlled time source.
func WithClock(clock func() time.Time) Option {
	return func(options *managerOptions) error {
		if clock == nil {
			return errors.New("auth: clock must not be nil")
		}
		options.now = clock
		return nil
	}
}

// Manager validates the single admin bearer token and issues signed browser
// sessions. It retains only the SHA-256 hash of the admin token; the plaintext
// admin token is not kept after initialization.
type Manager struct {
	adminTokenHash [sha256.Size]byte
	sessionKey     [secretBytes]byte
	cookieName     string
	sessionTTL     time.Duration
	now            func() time.Time
}

// NewManager loads or creates the admin token and session HMAC key at their
// caller-specified paths. Both files are created with, and normalized to, mode
// 0600. The returned bootstrapToken is non-empty only when this call created
// the admin token; callers may display it once for initial setup. An existing
// stored token is never returned.
func NewManager(adminTokenPath, sessionKeyPath string, options ...Option) (*Manager, string, error) {
	if err := validateSecretPaths(adminTokenPath, sessionKeyPath); err != nil {
		return nil, "", err
	}

	config := managerOptions{
		cookieName: defaultCookieName,
		sessionTTL: defaultSessionTTL,
		now:        time.Now,
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(&config); err != nil {
			return nil, "", err
		}
	}

	sessionKeyEncoded, _, err := loadOrCreateSecret(sessionKeyPath)
	if err != nil {
		return nil, "", fmt.Errorf("auth: initialize session key: %w", err)
	}
	sessionKey, err := decodeSecret(sessionKeyEncoded)
	clear(sessionKeyEncoded)
	if err != nil {
		return nil, "", fmt.Errorf("auth: decode session key: %w", err)
	}

	adminTokenEncoded, adminTokenCreated, err := loadOrCreateSecret(adminTokenPath)
	if err != nil {
		clear(sessionKey[:])
		return nil, "", fmt.Errorf("auth: initialize admin token: %w", err)
	}

	adminTokenHash := sha256.Sum256(adminTokenEncoded)
	bootstrapToken := ""
	if adminTokenCreated {
		bootstrapToken = string(adminTokenEncoded)
	}
	clear(adminTokenEncoded)

	return &Manager{
		adminTokenHash: adminTokenHash,
		sessionKey:     sessionKey,
		cookieName:     config.cookieName,
		sessionTTL:     config.sessionTTL,
		now:            config.now,
	}, bootstrapToken, nil
}

// CookieName returns the configured browser session cookie name.
func (m *Manager) CookieName() string {
	return m.cookieName
}

// AuthenticateBearer validates token using a constant-time comparison and
// returns the admin principal on success.
func (m *Manager) AuthenticateBearer(token string) (Principal, error) {
	presentedHash := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(presentedHash[:], m.adminTokenHash[:]) != 1 {
		return Principal{}, newAuthError(CodeInvalidBearer)
	}

	return adminPrincipal(AuthMethodBearer), nil
}

// Authenticate authenticates an HTTP request. A supplied Authorization header
// takes precedence over a session cookie. Cookie-authenticated state-changing
// requests require a matching X-CSRF-Token header; bearer-authenticated
// requests do not.
func (m *Manager) Authenticate(request *http.Request) (Principal, error) {
	if request == nil {
		return Principal{}, newAuthError(CodeMissingCredentials)
	}

	if authorization := request.Header.Values("Authorization"); len(authorization) > 0 {
		token, ok := bearerToken(authorization)
		if !ok {
			return Principal{}, newAuthError(CodeInvalidBearer)
		}
		return m.AuthenticateBearer(token)
	}

	cookieValue, found, err := m.sessionCookie(request)
	if err != nil {
		return Principal{}, err
	}
	if !found {
		return Principal{}, newAuthError(CodeMissingCredentials)
	}

	claims, err := m.verifySession(cookieValue)
	if err != nil {
		return Principal{}, err
	}
	if !safeMethod(request.Method) && !csrfMatches(request.Header.Values("X-CSRF-Token"), claims.CSRFToken) {
		return Principal{}, newAuthError(CodeCSRF)
	}

	return adminPrincipal(AuthMethodSession), nil
}

// IssueSession writes a signed, expiring browser session cookie and returns its
// CSRF token. Call it only after the caller has authenticated an administrator.
// X-Forwarded-Proto is trusted for Secure-cookie detection, so deployments must
// ensure it is stripped or set only by a trusted reverse proxy.
func (m *Manager) IssueSession(writer http.ResponseWriter, request *http.Request) (Session, error) {
	if writer == nil {
		return Session{}, errors.New("auth: response writer must not be nil")
	}

	csrfEncoded, err := newSecret()
	if err != nil {
		return Session{}, fmt.Errorf("auth: generate csrf token: %w", err)
	}
	csrfToken := string(csrfEncoded)
	clear(csrfEncoded)

	expiresAt := m.now().Add(m.sessionTTL)
	claims := sessionClaims{
		ExpiresAt: expiresAt.UnixNano(),
		CSRFToken: csrfToken,
	}
	cookieValue, err := m.signedSessionValue(claims)
	if err != nil {
		return Session{}, fmt.Errorf("auth: sign session: %w", err)
	}

	http.SetCookie(writer, &http.Cookie{
		Name:     m.cookieName,
		Value:    cookieValue,
		Path:     "/",
		Expires:  expiresAt.UTC(),
		MaxAge:   maxAgeSeconds(m.sessionTTL),
		HttpOnly: true,
		Secure:   secureRequest(request),
		SameSite: http.SameSiteStrictMode,
	})

	return Session{CSRFToken: csrfToken, ExpiresAt: expiresAt}, nil
}

// ClearSession expires the configured browser session cookie.
func (m *Manager) ClearSession(writer http.ResponseWriter, request *http.Request) {
	if writer == nil {
		return
	}

	http.SetCookie(writer, &http.Cookie{
		Name:     m.cookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(1, 0).UTC(),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secureRequest(request),
		SameSite: http.SameSiteStrictMode,
	})
}

type sessionClaims struct {
	ExpiresAt int64  `json:"exp"`
	CSRFToken string `json:"csrf"`
}

func (m *Manager) signedSessionValue(claims sessionClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	message := sessionCookieVersion + "." + encodedPayload
	signature := m.sessionSignature(message)
	return message + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (m *Manager) verifySession(value string) (sessionClaims, error) {
	var claims sessionClaims

	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != sessionCookieVersion || parts[1] == "" || parts[2] == "" {
		return claims, newAuthError(CodeInvalidSession)
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != sha256.Size {
		return claims, newAuthError(CodeInvalidSession)
	}
	expectedSignature := m.sessionSignature(parts[0] + "." + parts[1])
	if subtle.ConstantTimeCompare(signature, expectedSignature) != 1 {
		return claims, newAuthError(CodeInvalidSession)
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) == 0 {
		return claims, newAuthError(CodeInvalidSession)
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.ExpiresAt <= 0 || claims.CSRFToken == "" {
		return claims, newAuthError(CodeInvalidSession)
	}

	if !m.now().Before(time.Unix(0, claims.ExpiresAt)) {
		return claims, newAuthError(CodeExpiredSession)
	}

	return claims, nil
}

func (m *Manager) sessionSignature(message string) []byte {
	mac := hmac.New(sha256.New, m.sessionKey[:])
	_, _ = mac.Write([]byte(message))
	return mac.Sum(nil)
}

func (m *Manager) sessionCookie(request *http.Request) (string, bool, error) {
	var value string
	found := false
	for _, cookie := range request.Cookies() {
		if cookie.Name != m.cookieName {
			continue
		}
		if found {
			return "", false, newAuthError(CodeInvalidSession)
		}
		found = true
		value = cookie.Value
	}

	if found && value == "" {
		return "", false, newAuthError(CodeInvalidSession)
	}
	return value, found, nil
}

func newAuthError(code ErrorCode) *AuthError {
	return &AuthError{Code: code}
}

func adminPrincipal(method AuthMethod) Principal {
	return Principal{Subject: AdminSubject, Method: method}
}

func bearerToken(values []string) (string, bool) {
	if len(values) != 1 {
		return "", false
	}

	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	return parts[1], true
}

func csrfMatches(values []string, expected string) bool {
	if len(values) != 1 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(values[0]), []byte(expected)) == 1
}

func safeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

func secureRequest(request *http.Request) bool {
	if request == nil {
		return false
	}
	if request.TLS != nil {
		return true
	}
	for _, headerValue := range request.Header.Values("X-Forwarded-Proto") {
		for _, proto := range strings.Split(headerValue, ",") {
			if strings.EqualFold(strings.TrimSpace(proto), "https") {
				return true
			}
		}
	}
	return false
}

func maxAgeSeconds(ttl time.Duration) int {
	seconds := int64(ttl / time.Second)
	if ttl%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		return 1
	}

	maxInt := int64(^uint(0) >> 1)
	if seconds > maxInt {
		return int(maxInt)
	}
	return int(seconds)
}

func validCookieName(name string) bool {
	if name == "" {
		return false
	}
	return (&http.Cookie{Name: name, Value: "x"}).Valid() == nil
}

func validateSecretPaths(adminTokenPath, sessionKeyPath string) error {
	if adminTokenPath == "" {
		return errors.New("auth: admin token path must not be empty")
	}
	if sessionKeyPath == "" {
		return errors.New("auth: session key path must not be empty")
	}

	adminAbsolutePath, err := filepath.Abs(adminTokenPath)
	if err != nil {
		return fmt.Errorf("auth: resolve admin token path: %w", err)
	}
	sessionAbsolutePath, err := filepath.Abs(sessionKeyPath)
	if err != nil {
		return fmt.Errorf("auth: resolve session key path: %w", err)
	}
	if adminAbsolutePath == sessionAbsolutePath {
		return errors.New("auth: admin token and session key paths must differ")
	}
	return nil
}

func loadOrCreateSecret(path string) ([]byte, bool, error) {
	for {
		secret, err := readSecretFile(path)
		if err == nil {
			return secret, false, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, false, err
		}

		secret, err = newSecret()
		if err != nil {
			return nil, false, err
		}
		if err := writeSecretFile(path, secret); err == nil {
			return secret, true, nil
		} else if errors.Is(err, fs.ErrExist) {
			clear(secret)
			continue
		} else {
			clear(secret)
			return nil, false, err
		}
	}
}

func readSecretFile(path string) ([]byte, error) {
	fileInfo, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fileInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("auth: secret file %q is not a regular file", path)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	fileInfo, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if !fileInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("auth: secret file %q is not a regular file", path)
	}
	if err := file.Chmod(0o600); err != nil {
		return nil, fmt.Errorf("auth: set permissions on %q: %w", path, err)
	}

	secret, err := io.ReadAll(io.LimitReader(file, int64(maxSecretFileBytes+1)))
	if err != nil {
		return nil, err
	}
	if len(secret) > maxSecretFileBytes {
		clear(secret)
		return nil, errors.New("auth: secret file is too large")
	}
	if len(secret) > 0 && secret[len(secret)-1] == '\n' {
		secret = secret[:len(secret)-1]
	}
	if bytes.IndexByte(secret, '\n') >= 0 || bytes.IndexByte(secret, '\r') >= 0 {
		clear(secret)
		return nil, errors.New("auth: secret file contains unexpected whitespace")
	}

	decoded, err := decodeSecret(secret)
	clear(decoded[:])
	if err != nil {
		clear(secret)
		return nil, err
	}
	return secret, nil
}

func writeSecretFile(path string, secret []byte) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("auth: set permissions on %q: %w", path, err)
	}
	if err := writeAll(file, secret); err != nil {
		return err
	}
	if err := writeAll(file, []byte{'\n'}); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return nil
}

func writeAll(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		count, err := writer.Write(value)
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
		value = value[count:]
	}
	return nil
}

func newSecret() ([]byte, error) {
	randomBytes := make([]byte, secretBytes)
	defer clear(randomBytes)
	if _, err := rand.Read(randomBytes); err != nil {
		return nil, err
	}

	encoded := make([]byte, base64.RawURLEncoding.EncodedLen(len(randomBytes)))
	base64.RawURLEncoding.Encode(encoded, randomBytes)
	return encoded, nil
}

func decodeSecret(encoded []byte) ([secretBytes]byte, error) {
	var decoded [secretBytes]byte
	if len(encoded) != encodedSecretLength {
		return decoded, errors.New("auth: secret has invalid length")
	}

	count, err := base64.RawURLEncoding.Decode(decoded[:], encoded)
	if err != nil || count != secretBytes {
		clear(decoded[:])
		return decoded, errors.New("auth: secret has invalid encoding")
	}

	canonical := make([]byte, encodedSecretLength)
	base64.RawURLEncoding.Encode(canonical, decoded[:])
	valid := bytes.Equal(canonical, encoded)
	clear(canonical)
	if !valid {
		clear(decoded[:])
		return decoded, errors.New("auth: secret has non-canonical encoding")
	}

	return decoded, nil
}
