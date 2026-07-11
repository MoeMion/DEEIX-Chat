package mcp

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	domainmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/mcp"
	inframcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/mcp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/pkg/secretbox"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
)

const (
	contextJWTHeader           = "X-DEEIX-Context"
	contextJWTAlgorithm        = "HS256"
	contextJWTMinExpiresSecond = 60
	contextJWTMaxExpiresSecond = 900
	contextJWTSecretBytes      = 32
	contextJWTMaxKeyIDBytes    = 64
	contextJWTRotationTTL      = 24 * time.Hour
)

var (
	ErrMCPContextJWTInvalidPolicy    = errors.New("invalid mcp context jwt policy")
	ErrMCPContextJWTUnavailable      = errors.New("mcp context jwt unavailable")
	ErrMCPContextJWTPendingExists    = errors.New("mcp context jwt pending rotation exists")
	ErrMCPContextJWTRotationConflict = errors.New("mcp context jwt rotation conflict")
	ErrMCPContextJWTRotationExpired  = errors.New("mcp context jwt rotation expired")
	ErrMCPContextJWTInvalidStorage   = errors.New("mcp context jwt storage invalid")
)

type ContextJWTPolicyInput struct {
	ExpiresSeconds int
	IncludeName    bool
	IncludeEmail   bool
	IncludeRole    bool
}

type ContextJWTStatus struct {
	ServerPublicID   string
	Mode             string
	Configured       bool
	Issuer           string
	Audience         string
	KeyID            string
	ExpiresSeconds   int
	IncludeName      bool
	IncludeEmail     bool
	IncludeRole      bool
	PendingKeyID     string
	PendingExpiresAt *time.Time
}

type PrepareContextJWTRotationResult struct {
	ServerPublicID string
	Header         string
	Algorithm      string
	Secret         string
	Issuer         string
	Audience       string
	KeyID          string
	ExpiresSeconds int
}

type ServerView struct {
	Server     domainmcp.Server
	ContextJWT ContextJWTStatus
}

func (s *Service) DescribeServer(server domainmcp.Server) ServerView {
	now, err := s.contextJWTCurrentTime()
	if err != nil {
		now = time.Time{}
	}
	return ServerView{
		Server:     server,
		ContextJWT: buildContextJWTStatus(server, s.contextJWTStatusIssuer(), now),
	}
}

func (s *Service) UpdateContextJWTPolicy(
	ctx context.Context,
	serverID uint,
	input ContextJWTPolicyInput,
) (ContextJWTStatus, error) {
	if !validContextJWTExpiresSeconds(input.ExpiresSeconds) {
		return ContextJWTStatus{}, ErrMCPContextJWTInvalidPolicy
	}
	now, err := s.contextJWTCurrentTime()
	if err != nil {
		return ContextJWTStatus{}, err
	}
	if s == nil || s.repo == nil {
		return ContextJWTStatus{}, ErrMCPContextJWTUnavailable
	}
	server, err := s.repo.UpdateContextJWTPolicy(ctx, serverID, repository.UpdateMCPContextJWTPolicyInput{
		ExpiresSeconds: input.ExpiresSeconds,
		IncludeName:    input.IncludeName,
		IncludeEmail:   input.IncludeEmail,
		IncludeRole:    input.IncludeRole,
	})
	if err != nil {
		return ContextJWTStatus{}, mapContextJWTRepositoryError(err)
	}
	if server == nil {
		return ContextJWTStatus{}, ErrMCPContextJWTUnavailable
	}
	return buildContextJWTStatus(*server, s.contextJWTStatusIssuer(), now), nil
}

func (s *Service) PrepareContextJWTRotation(
	ctx context.Context,
	serverID uint,
) (PrepareContextJWTRotationResult, error) {
	if s == nil || s.repo == nil {
		return PrepareContextJWTRotationResult{}, ErrMCPContextJWTUnavailable
	}
	server, err := s.repo.GetServer(ctx, serverID)
	if err != nil {
		return PrepareContextJWTRotationResult{}, mapContextJWTRepositoryError(err)
	}
	if server == nil {
		return PrepareContextJWTRotationResult{}, ErrMCPContextJWTUnavailable
	}

	cfg := s.cfg.Snapshot()
	issuer, err := normalizeContextJWTIssuer(cfg.PublicWebBaseURL, cfg.Env)
	if err != nil {
		return PrepareContextJWTRotationResult{}, err
	}
	if strings.TrimSpace(server.ContextJWTAudience) == "" {
		return PrepareContextJWTRotationResult{}, ErrMCPContextJWTInvalidStorage
	}
	if !validContextJWTExpiresSeconds(server.ContextJWTExpiresSeconds) {
		return PrepareContextJWTRotationResult{}, ErrMCPContextJWTInvalidPolicy
	}
	now, err := s.contextJWTCurrentTime()
	if err != nil {
		return PrepareContextJWTRotationResult{}, err
	}
	kid, err := s.contextJWTNextKeyID()
	if err != nil {
		return PrepareContextJWTRotationResult{}, err
	}
	random, err := readContextJWTRandom(s.contextJWTRandom)
	if err != nil {
		return PrepareContextJWTRotationResult{}, err
	}
	secret := base64.RawURLEncoding.EncodeToString(random)
	ciphertext, err := secretbox.EncryptString(cfg.DataEncryptionKey, secret)
	clear(random)
	if err != nil || !strings.HasPrefix(ciphertext, "v1:") {
		return PrepareContextJWTRotationResult{}, ErrMCPContextJWTUnavailable
	}

	server, err = s.repo.PrepareContextJWTRotation(ctx, repository.PrepareMCPContextJWTRotationInput{
		ServerID:         serverID,
		PendingSecretEnc: ciphertext,
		PendingKeyID:     kid,
		CreatedAt:        now,
		ExpiresAt:        now.Add(contextJWTRotationTTL),
	})
	if err != nil {
		return PrepareContextJWTRotationResult{}, mapContextJWTRepositoryError(err)
	}
	if server == nil {
		return PrepareContextJWTRotationResult{}, ErrMCPContextJWTUnavailable
	}
	return PrepareContextJWTRotationResult{
		ServerPublicID: server.PublicID,
		Header:         contextJWTHeader,
		Algorithm:      contextJWTAlgorithm,
		Secret:         secret,
		Issuer:         issuer,
		Audience:       server.ContextJWTAudience,
		KeyID:          kid,
		ExpiresSeconds: server.ContextJWTExpiresSeconds,
	}, nil
}

func (s *Service) ActivateContextJWTRotation(
	ctx context.Context,
	serverID uint,
	kid string,
) (ContextJWTStatus, error) {
	kid = strings.TrimSpace(kid)
	if kid == "" || len(kid) > contextJWTMaxKeyIDBytes {
		return ContextJWTStatus{}, ErrMCPContextJWTInvalidPolicy
	}
	cfg := s.cfg.Snapshot()
	issuer, err := normalizeContextJWTIssuer(cfg.PublicWebBaseURL, cfg.Env)
	if err != nil {
		return ContextJWTStatus{}, err
	}
	if s == nil || s.repo == nil {
		return ContextJWTStatus{}, ErrMCPContextJWTUnavailable
	}
	server, err := s.repo.GetServer(ctx, serverID)
	if err != nil {
		return ContextJWTStatus{}, mapContextJWTRepositoryError(err)
	}
	if server == nil {
		return ContextJWTStatus{}, ErrMCPContextJWTUnavailable
	}
	if server.ContextJWTPendingKeyID != kid {
		return ContextJWTStatus{}, ErrMCPContextJWTRotationConflict
	}
	if strings.TrimSpace(server.ContextJWTPendingSecretEnc) == "" {
		return ContextJWTStatus{}, ErrMCPContextJWTInvalidStorage
	}
	secret, err := secretbox.DecryptString(cfg.DataEncryptionKey, server.ContextJWTPendingSecretEnc)
	if err != nil {
		return ContextJWTStatus{}, ErrMCPContextJWTInvalidStorage
	}
	if err = inframcp.ValidateSignedContextSecret(secret); err != nil {
		return ContextJWTStatus{}, ErrMCPContextJWTInvalidStorage
	}
	if !validContextJWTExpiresSeconds(server.ContextJWTExpiresSeconds) {
		return ContextJWTStatus{}, ErrMCPContextJWTInvalidPolicy
	}
	now, err := s.contextJWTCurrentTime()
	if err != nil {
		return ContextJWTStatus{}, err
	}
	server, err = s.repo.ActivateContextJWTRotation(ctx, serverID, kid, now)
	if err != nil {
		return ContextJWTStatus{}, mapContextJWTRepositoryError(err)
	}
	if server == nil {
		return ContextJWTStatus{}, ErrMCPContextJWTUnavailable
	}
	return buildContextJWTStatus(*server, issuer, now), nil
}

func (s *Service) CancelContextJWTRotation(
	ctx context.Context,
	serverID uint,
	kid string,
) (ContextJWTStatus, error) {
	kid = strings.TrimSpace(kid)
	if kid == "" || len(kid) > contextJWTMaxKeyIDBytes {
		return ContextJWTStatus{}, ErrMCPContextJWTInvalidPolicy
	}
	now, err := s.contextJWTCurrentTime()
	if err != nil {
		return ContextJWTStatus{}, err
	}
	if s == nil || s.repo == nil {
		return ContextJWTStatus{}, ErrMCPContextJWTUnavailable
	}
	server, err := s.repo.CancelContextJWTRotation(ctx, serverID, kid)
	if err != nil {
		return ContextJWTStatus{}, mapContextJWTRepositoryError(err)
	}
	if server == nil {
		return ContextJWTStatus{}, ErrMCPContextJWTUnavailable
	}
	return buildContextJWTStatus(*server, s.contextJWTStatusIssuer(), now), nil
}

func (s *Service) DisableContextJWT(
	ctx context.Context,
	serverID uint,
) (ContextJWTStatus, error) {
	now, err := s.contextJWTCurrentTime()
	if err != nil {
		return ContextJWTStatus{}, err
	}
	if s == nil || s.repo == nil {
		return ContextJWTStatus{}, ErrMCPContextJWTUnavailable
	}
	server, err := s.repo.DisableContextJWT(ctx, serverID)
	if err != nil {
		return ContextJWTStatus{}, mapContextJWTRepositoryError(err)
	}
	if server == nil {
		return ContextJWTStatus{}, ErrMCPContextJWTUnavailable
	}
	return buildContextJWTStatus(*server, s.contextJWTStatusIssuer(), now), nil
}

func buildContextJWTStatus(server domainmcp.Server, issuer string, now time.Time) ContextJWTStatus {
	mode := server.ContextJWTMode
	if mode != "none" && mode != "hs256" {
		mode = "none"
	}
	status := ContextJWTStatus{
		ServerPublicID: server.PublicID,
		Mode:           mode,
		Configured: server.ContextJWTMode == "hs256" &&
			strings.TrimSpace(server.ContextJWTSecretEnc) != "" &&
			strings.TrimSpace(server.ContextJWTKeyID) != "" &&
			strings.TrimSpace(server.ContextJWTAudience) != "" &&
			validContextJWTExpiresSeconds(server.ContextJWTExpiresSeconds),
		Issuer:         issuer,
		Audience:       server.ContextJWTAudience,
		KeyID:          server.ContextJWTKeyID,
		ExpiresSeconds: server.ContextJWTExpiresSeconds,
		IncludeName:    server.ContextJWTIncludeName,
		IncludeEmail:   server.ContextJWTIncludeEmail,
		IncludeRole:    server.ContextJWTIncludeRole,
	}
	if !now.IsZero() && server.ContextJWTPendingExpiresAt != nil && now.Before(*server.ContextJWTPendingExpiresAt) {
		expiresAt := *server.ContextJWTPendingExpiresAt
		status.PendingKeyID = server.ContextJWTPendingKeyID
		status.PendingExpiresAt = &expiresAt
	}
	return status
}

func normalizeContextJWTIssuer(raw string, env string) (string, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(value)
	if err != nil || value == "" || parsed.Scheme == "" || parsed.Host == "" || parsed.Hostname() == "" ||
		(!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) ||
		parsed.User != nil || parsed.ForceQuery || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.RawFragment != "" ||
		strings.Contains(value, "?") || strings.Contains(value, "#") {
		return "", ErrMCPContextJWTUnavailable
	}
	environment := strings.ToLower(strings.TrimSpace(env))
	if (environment == "prod" || environment == "production") && !strings.EqualFold(parsed.Scheme, "https") {
		return "", ErrMCPContextJWTUnavailable
	}
	return value, nil
}

func mapContextJWTRepositoryError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, repository.ErrNotFound):
		return ErrMCPServerNotFound
	case errors.Is(err, repository.ErrMCPContextJWTPendingExists):
		return ErrMCPContextJWTPendingExists
	case errors.Is(err, repository.ErrMCPContextJWTRotationConflict):
		return ErrMCPContextJWTRotationConflict
	case errors.Is(err, repository.ErrMCPContextJWTPendingExpired):
		return ErrMCPContextJWTRotationExpired
	default:
		return err
	}
}

func validContextJWTExpiresSeconds(value int) bool {
	return value >= contextJWTMinExpiresSecond && value <= contextJWTMaxExpiresSecond
}

func (s *Service) contextJWTCurrentTime() (now time.Time, err error) {
	if s == nil || s.contextJWTNow == nil {
		return time.Time{}, ErrMCPContextJWTUnavailable
	}
	defer func() {
		if recover() != nil {
			now = time.Time{}
			err = ErrMCPContextJWTUnavailable
		}
	}()
	now = s.contextJWTNow()
	if now.IsZero() {
		return time.Time{}, ErrMCPContextJWTUnavailable
	}
	return now, nil
}

func (s *Service) contextJWTNextKeyID() (kid string, err error) {
	if s == nil || s.contextJWTNewKeyID == nil {
		return "", ErrMCPContextJWTUnavailable
	}
	defer func() {
		if recover() != nil {
			kid = ""
			err = ErrMCPContextJWTUnavailable
		}
	}()
	kid = strings.TrimSpace(s.contextJWTNewKeyID())
	if kid == "" || len(kid) > contextJWTMaxKeyIDBytes || !utf8.ValidString(kid) {
		return "", ErrMCPContextJWTUnavailable
	}
	return kid, nil
}

func readContextJWTRandom(reader io.Reader) (value []byte, err error) {
	if reader == nil {
		return nil, ErrMCPContextJWTUnavailable
	}
	defer func() {
		if recover() != nil {
			clear(value)
			value = nil
			err = ErrMCPContextJWTUnavailable
		}
	}()
	value = make([]byte, contextJWTSecretBytes)
	if _, err = io.ReadFull(reader, value); err != nil {
		clear(value)
		return nil, ErrMCPContextJWTUnavailable
	}
	return value, nil
}

func (s *Service) contextJWTStatusIssuer() string {
	if s == nil {
		return ""
	}
	cfg := s.cfg.Snapshot()
	issuer, err := normalizeContextJWTIssuer(cfg.PublicWebBaseURL, cfg.Env)
	if err != nil {
		return ""
	}
	return issuer
}
