package artifacts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const defaultTTL = 2 * time.Minute

var (
	ErrSigningKeyRequired = errors.New("signing key is required")
	ErrRunIDRequired      = errors.New("run_id is required")
	ErrSessionIDRequired  = errors.New("session_id is required")
	ErrPathRequired       = errors.New("path is required")
	ErrPathInvalid        = errors.New("path is invalid")
	ErrScopeInvalid       = errors.New("scope is invalid")
	ErrOperationRequired  = errors.New("operation is required")
	ErrOperationInvalid   = errors.New("operation is invalid")
	ErrTokenRequired      = errors.New("presigned token is required")
	ErrTokenInvalid       = errors.New("presigned token is invalid")
	ErrTokenExpired       = errors.New("presigned token is expired")
	ErrScopeRejected      = errors.New("presigned scope rejected")
)

// Operation defines which artifact action a token can authorize.
type Operation string

const (
	OperationUpload   Operation = "upload"
	OperationDownload Operation = "download"
)

// Config controls token minting + validation behaviour.
type Config struct {
	SigningKey []byte
	DefaultTTL time.Duration
	Now        func() time.Time
}

// Claims captures scope + binding for a single presigned artifact URL.
type Claims struct {
	RunID       string    `json:"run_id"`
	SessionID   string    `json:"session_id"`
	ScopePrefix string    `json:"scope_prefix"`
	Path        string    `json:"path"`
	Operation   Operation `json:"operation"`
	IssuedAt    time.Time `json:"issued_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// PresignRequest requests a short-lived scoped token.
type PresignRequest struct {
	RunID       string
	SessionID   string
	ScopePrefix string
	Path        string
	Operation   Operation
	TTL         time.Duration
}

// PresignedToken is returned when token minting succeeds.
type PresignedToken struct {
	Token string
	Claims
}

// ValidateRequest validates a token for a concrete transfer attempt.
type ValidateRequest struct {
	Token     string
	RunID     string
	Path      string
	Operation Operation
}

// Service mints + validates presigned upload/download tokens.
type Service struct {
	key        []byte
	defaultTTL time.Duration
	now        func() time.Time
}

// NewService constructs a presign service with safe defaults.
func NewService(cfg Config) (*Service, error) {
	if len(cfg.SigningKey) == 0 {
		return nil, ErrSigningKeyRequired
	}

	ttl := cfg.DefaultTTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}

	return &Service{
		key:        append([]byte(nil), cfg.SigningKey...),
		defaultTTL: ttl,
		now:        nowFn,
	}, nil
}

// Presign creates a short-lived HMAC-signed token bound to run/session/scope/op.
func (s *Service) Presign(req PresignRequest) (*PresignedToken, error) {
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		return nil, ErrRunIDRequired
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return nil, ErrSessionIDRequired
	}
	op, err := normalizeOperation(req.Operation)
	if err != nil {
		return nil, err
	}

	artifactPath, err := normalizePath(req.Path, false)
	if err != nil {
		return nil, err
	}
	scopePrefix, err := normalizePath(req.ScopePrefix, true)
	if err != nil {
		return nil, err
	}
	if scopePrefix == "" {
		scopePrefix = artifactPath
	}
	if !withinScope(artifactPath, scopePrefix) {
		return nil, fmt.Errorf("%w: path %q is outside scope %q", ErrScopeRejected, artifactPath, scopePrefix)
	}

	ttl := req.TTL
	if ttl <= 0 {
		ttl = s.defaultTTL
	}

	issuedAt := s.now().UTC()
	expiresAt := issuedAt.Add(ttl)

	wire := signedClaims{
		RunID:       runID,
		SessionID:   sessionID,
		ScopePrefix: scopePrefix,
		Path:        artifactPath,
		Operation:   string(op),
		IssuedAt:    issuedAt.Unix(),
		ExpiresAt:   expiresAt.Unix(),
	}

	token, err := s.signToken(wire)
	if err != nil {
		return nil, err
	}

	claims := Claims{
		RunID:       runID,
		SessionID:   sessionID,
		ScopePrefix: scopePrefix,
		Path:        artifactPath,
		Operation:   op,
		IssuedAt:    issuedAt,
		ExpiresAt:   expiresAt,
	}
	return &PresignedToken{Token: token, Claims: claims}, nil
}

// Validate checks token signature + expiration + operation + run + path scope.
func (s *Service) Validate(req ValidateRequest) (*Claims, error) {
	token := strings.TrimSpace(req.Token)
	if token == "" {
		return nil, ErrTokenRequired
	}
	expectedRunID := strings.TrimSpace(req.RunID)
	if expectedRunID == "" {
		return nil, ErrRunIDRequired
	}
	expectedOperation, err := normalizeOperation(req.Operation)
	if err != nil {
		return nil, err
	}
	requestedPath, err := normalizePath(req.Path, false)
	if err != nil {
		return nil, err
	}

	wire, err := s.parseToken(token)
	if err != nil {
		return nil, err
	}

	claims := Claims{
		RunID:       strings.TrimSpace(wire.RunID),
		SessionID:   strings.TrimSpace(wire.SessionID),
		ScopePrefix: strings.TrimSpace(wire.ScopePrefix),
		Path:        strings.TrimSpace(wire.Path),
		Operation:   Operation(strings.TrimSpace(wire.Operation)),
		IssuedAt:    time.Unix(wire.IssuedAt, 0).UTC(),
		ExpiresAt:   time.Unix(wire.ExpiresAt, 0).UTC(),
	}

	if s.now().UTC().After(claims.ExpiresAt) {
		return nil, ErrTokenExpired
	}
	if claims.RunID != expectedRunID {
		return nil, fmt.Errorf("%w: token run_id %q does not match %q", ErrScopeRejected, claims.RunID, expectedRunID)
	}
	if claims.Operation != expectedOperation {
		return nil, fmt.Errorf("%w: token operation %q does not allow %q", ErrScopeRejected, claims.Operation, expectedOperation)
	}
	if !withinScope(requestedPath, claims.ScopePrefix) {
		return nil, fmt.Errorf("%w: requested path %q is outside scope %q", ErrScopeRejected, requestedPath, claims.ScopePrefix)
	}
	if claims.Path != "" && requestedPath != claims.Path {
		return nil, fmt.Errorf("%w: requested path %q does not match token path %q", ErrScopeRejected, requestedPath, claims.Path)
	}
	return &claims, nil
}

type signedClaims struct {
	RunID       string `json:"run_id"`
	SessionID   string `json:"session_id"`
	ScopePrefix string `json:"scope_prefix"`
	Path        string `json:"path"`
	Operation   string `json:"operation"`
	IssuedAt    int64  `json:"issued_at"`
	ExpiresAt   int64  `json:"expires_at"`
}

func (s *Service) signToken(claims signedClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode presign payload: %w", err)
	}

	sig := hmacSignature(s.key, payload)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	encodedSig := base64.RawURLEncoding.EncodeToString(sig)
	return encodedPayload + "." + encodedSig, nil
}

func (s *Service) parseToken(token string) (*signedClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, ErrTokenInvalid
	}
	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrTokenInvalid
	}
	sigRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrTokenInvalid
	}

	expected := hmacSignature(s.key, payloadRaw)
	if !hmac.Equal(sigRaw, expected) {
		return nil, ErrTokenInvalid
	}

	var claims signedClaims
	if err := json.Unmarshal(payloadRaw, &claims); err != nil {
		return nil, ErrTokenInvalid
	}
	if strings.TrimSpace(claims.RunID) == "" || strings.TrimSpace(claims.Path) == "" || strings.TrimSpace(claims.ScopePrefix) == "" {
		return nil, ErrTokenInvalid
	}
	if _, err := normalizeOperation(Operation(claims.Operation)); err != nil {
		return nil, ErrTokenInvalid
	}
	if claims.ExpiresAt <= 0 || claims.IssuedAt <= 0 || claims.ExpiresAt < claims.IssuedAt {
		return nil, ErrTokenInvalid
	}
	return &claims, nil
}

func normalizeOperation(op Operation) (Operation, error) {
	switch Operation(strings.TrimSpace(string(op))) {
	case OperationUpload:
		return OperationUpload, nil
	case OperationDownload:
		return OperationDownload, nil
	case "":
		return "", ErrOperationRequired
	default:
		return "", ErrOperationInvalid
	}
}

func normalizePath(raw string, allowEmpty bool) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if raw == "" {
		if allowEmpty {
			return "", nil
		}
		return "", ErrPathRequired
	}
	parts := strings.Split(raw, "/")
	cleanParts := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if allowEmpty {
				return "", ErrScopeInvalid
			}
			return "", ErrPathInvalid
		}
		if strings.Contains(part, "\x00") {
			if allowEmpty {
				return "", ErrScopeInvalid
			}
			return "", ErrPathInvalid
		}
		cleanParts = append(cleanParts, part)
	}
	if len(cleanParts) == 0 {
		if allowEmpty {
			return "", nil
		}
		return "", ErrPathInvalid
	}
	return strings.Join(cleanParts, "/"), nil
}

func withinScope(artifactPath, scopePrefix string) bool {
	artifactPath = strings.TrimSpace(artifactPath)
	scopePrefix = strings.TrimSpace(scopePrefix)
	if artifactPath == "" || scopePrefix == "" {
		return false
	}
	if artifactPath == scopePrefix {
		return true
	}
	return strings.HasPrefix(artifactPath, scopePrefix+"/")
}

func hmacSignature(key, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}
