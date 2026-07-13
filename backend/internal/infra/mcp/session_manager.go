package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidSessionKey      = errors.New("mcp session key is invalid")
	ErrSessionContextMismatch = errors.New("mcp session context does not match cached session")
	ErrSessionManagerClosed   = errors.New("mcp session manager is closed")
	ErrSessionClosed          = errors.New("mcp session is closed")
)

type SessionKey struct {
	ServerID      uint
	UserPublicID  string
	RunID         string
	AuthIdentity  string
	ConfigVersion string
}

type AcquireInput struct {
	ServerID        uint
	ServerUpdatedAt time.Time
	CallConfig      CallConfig
	RetryCount      int
}

type ConfigurationVersionInput struct {
	ServerUpdatedAt     time.Time
	Endpoint            string
	TimeoutMS           int
	RetryCount          int
	HeadersEnabled      bool
	SignedContextHeader string
	SignedContext       *SignedContextConfig
}

type SessionManager interface {
	Acquire(context.Context, AcquireInput) (Operation, error)
	OpenEphemeral(context.Context, CallConfig, int) (Operation, func(context.Context) error, error)
	CloseRun(context.Context, string, string) error
	CloseAll(context.Context) error
}

type runSessionManager struct {
	transport          Transport
	mu                 sync.Mutex
	entries            map[SessionKey]*managedSession
	ephemeral          map[uint64]*managedSession
	closingEntries     map[*managedSession]struct{}
	nextEphemeral      uint64
	closed             bool
	cleanupWaitTimeout time.Duration
	closeAllOnce       sync.Once
	closeAllDone       chan struct{}
	closeAllErr        error
}

type managedSession struct {
	operation   *operation
	managed     *managedOperation
	config      CallConfig
	retryCount  int
	lifecycleMu sync.Mutex
	closing     bool
	active      sync.WaitGroup
	closeOnce   sync.Once
	closeDone   chan struct{}
	closeErr    error
}

type managedOperation struct {
	session *managedSession
}

var _ SessionManager = (*runSessionManager)(nil)
var _ Operation = (*managedOperation)(nil)

func AuthenticationIdentity(authToken string) string {
	sum := sha256.Sum256([]byte("bearer\x00" + authToken))
	return hex.EncodeToString(sum[:])
}

func ConfigurationVersion(input ConfigurationVersionInput) string {
	digest := sha256.New()
	writeSessionDigestField(digest, input.ServerUpdatedAt.UTC().Format(time.RFC3339Nano))
	writeSessionDigestField(digest, strings.TrimSpace(input.Endpoint))
	writeSessionDigestField(digest, strconv.Itoa(input.TimeoutMS))
	writeSessionDigestField(digest, strconv.Itoa(input.RetryCount))
	writeSessionDigestField(digest, strconv.FormatBool(input.HeadersEnabled))
	writeSessionDigestField(digest, input.SignedContextHeader)
	if input.SignedContext == nil {
		writeSessionDigestField(digest, "none")
	} else {
		writeSessionDigestField(digest, "signed")
		writeSessionDigestField(digest, input.SignedContext.Secret)
		writeSessionDigestField(digest, input.SignedContext.Issuer)
		writeSessionDigestField(digest, input.SignedContext.Audience)
		writeSessionDigestField(digest, input.SignedContext.KeyID)
		writeSessionDigestField(digest, strconv.Itoa(input.SignedContext.ExpiresSeconds))
		writeSessionDigestField(digest, strconv.FormatBool(input.SignedContext.IncludeName))
		writeSessionDigestField(digest, strconv.FormatBool(input.SignedContext.IncludeEmail))
		writeSessionDigestField(digest, strconv.FormatBool(input.SignedContext.IncludeRole))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writeSessionDigestField(digest hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write([]byte(value))
}

func normalizeSessionEndpoint(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil || parsed.Opaque != "" || parsed.Scheme == "" || parsed.Host == "" ||
		parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		parsed.RawFragment != "" || strings.Contains(trimmed, "#") {
		return "", ErrInvalidSessionKey
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", ErrInvalidSessionKey
	}
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String(), nil
}

func BuildSessionKey(input AcquireInput) (SessionKey, error) {
	templateContext := input.CallConfig.Context
	endpoint, err := normalizeSessionEndpoint(input.CallConfig.BaseURL)
	if input.ServerID == 0 || input.ServerUpdatedAt.IsZero() || templateContext.Mode != ContextModeChat ||
		strings.TrimSpace(templateContext.UserPublicID) == "" || strings.TrimSpace(templateContext.RunID) == "" ||
		err != nil || input.CallConfig.TimeoutMS <= 0 || input.RetryCount < 0 {
		return SessionKey{}, ErrInvalidSessionKey
	}
	return SessionKey{
		ServerID:     input.ServerID,
		UserPublicID: strings.TrimSpace(templateContext.UserPublicID),
		RunID:        strings.TrimSpace(templateContext.RunID),
		AuthIdentity: AuthenticationIdentity(input.CallConfig.AuthToken),
		ConfigVersion: ConfigurationVersion(ConfigurationVersionInput{
			ServerUpdatedAt:     input.ServerUpdatedAt,
			Endpoint:            endpoint,
			TimeoutMS:           input.CallConfig.TimeoutMS,
			RetryCount:          input.RetryCount,
			HeadersEnabled:      input.CallConfig.HeadersEnabled,
			SignedContextHeader: input.CallConfig.SignedContextHeader,
			SignedContext:       input.CallConfig.SignedContext,
		}),
	}, nil
}

func NewSessionManager(client *Client) SessionManager {
	if client == nil {
		return newRunSessionManager(nil)
	}
	return newRunSessionManager(client.transport)
}

func newRunSessionManager(transport Transport) *runSessionManager {
	return &runSessionManager{
		transport:          transport,
		entries:            make(map[SessionKey]*managedSession),
		ephemeral:          make(map[uint64]*managedSession),
		closingEntries:     make(map[*managedSession]struct{}),
		cleanupWaitTimeout: cleanupTimeout,
		closeAllDone:       make(chan struct{}),
	}
}

func cloneManagerCallConfig(input CallConfig) (CallConfig, error) {
	return snapshotCallConfig(input)
}

func sameManagerConfig(left CallConfig, right CallConfig, leftRetry int, rightRetry int) bool {
	return left.BaseURL == right.BaseURL && left.AuthToken == right.AuthToken && left.TimeoutMS == right.TimeoutMS &&
		left.HeadersEnabled == right.HeadersEnabled && left.SignedContextHeader == right.SignedContextHeader &&
		leftRetry == rightRetry && reflect.DeepEqual(left.CustomHeaders, right.CustomHeaders) &&
		reflect.DeepEqual(left.Context, right.Context) && reflect.DeepEqual(left.SignedContext, right.SignedContext)
}

func (m *runSessionManager) Acquire(_ context.Context, input AcquireInput) (Operation, error) {
	snapshot, err := cloneManagerCallConfig(input.CallConfig)
	if err != nil {
		return nil, err
	}
	input.CallConfig = snapshot
	key, err := BuildSessionKey(input)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrSessionManagerClosed
	}
	if existing := m.entries[key]; existing != nil {
		if !sameManagerConfig(existing.config, snapshot, existing.retryCount, input.RetryCount) {
			return nil, ErrSessionContextMismatch
		}
		return existing.managed, nil
	}
	op, err := newOperation(m.transport, snapshot, input.RetryCount)
	if err != nil {
		return nil, err
	}
	entry := newManagedSession(op, snapshot, input.RetryCount)
	m.entries[key] = entry
	return entry.managed, nil
}

func (m *runSessionManager) OpenEphemeral(
	_ context.Context,
	cfg CallConfig,
	retryCount int,
) (Operation, func(context.Context) error, error) {
	snapshot, err := cloneManagerCallConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	op, err := newOperation(m.transport, snapshot, retryCount)
	if err != nil {
		return nil, nil, err
	}
	entry := newManagedSession(op, snapshot, retryCount)

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, nil, ErrSessionManagerClosed
	}
	m.nextEphemeral++
	id := m.nextEphemeral
	m.ephemeral[id] = entry
	m.mu.Unlock()

	closeOperation := func(ctx context.Context) error {
		m.mu.Lock()
		tracked := false
		if current, exists := m.ephemeral[id]; exists && current == entry {
			delete(m.ephemeral, id)
			tracked = m.addClosingEntryLocked(entry)
		}
		waitTimeout := m.cleanupWaitTimeout
		m.mu.Unlock()
		if tracked {
			m.removeClosingEntryWhenDone(entry)
		}
		return closeManagedSessions(ctx, []*managedSession{entry}, waitTimeout)
	}
	return entry.managed, closeOperation, nil
}

func newManagedSession(op *operation, config CallConfig, retryCount int) *managedSession {
	entry := &managedSession{
		operation:  op,
		config:     config,
		retryCount: retryCount,
		closeDone:  make(chan struct{}),
	}
	entry.managed = &managedOperation{session: entry}
	return entry
}

func (m *runSessionManager) addClosingEntryLocked(entry *managedSession) bool {
	if _, exists := m.closingEntries[entry]; exists {
		return false
	}
	m.closingEntries[entry] = struct{}{}
	return true
}

func (m *runSessionManager) removeClosingEntryWhenDone(entry *managedSession) {
	go func() {
		<-entry.closeDone
		m.mu.Lock()
		delete(m.closingEntries, entry)
		m.mu.Unlock()
	}()
}

func (o *managedOperation) ListTools(ctx context.Context) ([]Tool, error) {
	if err := o.session.beginUse(); err != nil {
		return nil, err
	}
	defer o.session.endUse()
	return o.session.operation.ListTools(ctx)
}

func (o *managedOperation) CallTool(ctx context.Context, input CallInput) (string, error) {
	if err := o.session.beginUse(); err != nil {
		return "", err
	}
	defer o.session.endUse()
	return o.session.operation.CallTool(ctx, input)
}

func (s *managedSession) beginUse() error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closing {
		return ErrSessionClosed
	}
	s.active.Add(1)
	return nil
}

func (s *managedSession) endUse() {
	s.active.Done()
}

func (s *managedSession) startClose() {
	s.closeOnce.Do(func() {
		s.lifecycleMu.Lock()
		s.closing = true
		s.lifecycleMu.Unlock()

		go func() {
			s.active.Wait()
			cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
			s.closeErr = s.operation.terminate(cleanupCtx)
			cancel()
			close(s.closeDone)
		}()
	})
}

func closeManagedSession(ctx context.Context, entry *managedSession) error {
	entry.startClose()
	select {
	case <-entry.closeDone:
		return entry.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func closeManagedSessions(ctx context.Context, entries []*managedSession, waitTimeout time.Duration) error {
	if len(entries) == 0 {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), waitTimeout)
	defer cancel()

	errs := make(chan error, len(entries))
	var workers sync.WaitGroup
	workers.Add(len(entries))
	for _, entry := range entries {
		go func(entry *managedSession) {
			defer workers.Done()
			if err := closeManagedSession(cleanupCtx, entry); err != nil {
				errs <- err
			}
		}(entry)
	}
	workers.Wait()
	close(errs)

	joined := make([]error, 0, len(errs))
	for err := range errs {
		joined = append(joined, err)
	}
	return errors.Join(joined...)
}

func (m *runSessionManager) CloseRun(ctx context.Context, userPublicID string, runID string) error {
	m.mu.Lock()
	entries := make([]*managedSession, 0)
	tracked := make([]*managedSession, 0)
	for key, entry := range m.entries {
		if key.UserPublicID == userPublicID && key.RunID == runID {
			entries = append(entries, entry)
			delete(m.entries, key)
			if m.addClosingEntryLocked(entry) {
				tracked = append(tracked, entry)
			}
		}
	}
	waitTimeout := m.cleanupWaitTimeout
	m.mu.Unlock()
	for _, entry := range tracked {
		m.removeClosingEntryWhenDone(entry)
	}
	return closeManagedSessions(ctx, entries, waitTimeout)
}

func (m *runSessionManager) CloseAll(ctx context.Context) error {
	m.closeAllOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		entries := make(
			[]*managedSession,
			0,
			len(m.entries)+len(m.ephemeral)+len(m.closingEntries),
		)
		seen := make(map[*managedSession]struct{}, cap(entries))
		appendEntry := func(entry *managedSession) {
			if _, exists := seen[entry]; exists {
				return
			}
			seen[entry] = struct{}{}
			entries = append(entries, entry)
		}
		for _, entry := range m.entries {
			appendEntry(entry)
		}
		for _, entry := range m.ephemeral {
			appendEntry(entry)
		}
		for entry := range m.closingEntries {
			appendEntry(entry)
		}
		m.entries = make(map[SessionKey]*managedSession)
		m.ephemeral = make(map[uint64]*managedSession)
		m.closingEntries = make(map[*managedSession]struct{})
		waitTimeout := m.cleanupWaitTimeout
		m.mu.Unlock()

		cleanupParent := context.WithoutCancel(ctx)
		go func() {
			m.closeAllErr = closeManagedSessions(cleanupParent, entries, waitTimeout)
			close(m.closeAllDone)
		}()
	})
	<-m.closeAllDone
	return m.closeAllErr
}
