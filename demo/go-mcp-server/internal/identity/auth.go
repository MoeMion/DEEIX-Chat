package identity

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

const sessionPrincipalTag = "DEEIX-MCP-DEMO:session-principal:v1"

type Authenticator struct {
	bearerHash [sha256.Size]byte
	resolver   *Resolver
}

type authenticationError struct {
	code  string
	cause error
}

func (err *authenticationError) Error() string {
	return err.code
}

func (err *authenticationError) Unwrap() error {
	return err.cause
}

func NewAuthenticator(bearer string, resolver *Resolver) (*Authenticator, error) {
	if len(bearer) < 32 {
		return nil, errors.New("bearer must contain at least 32 bytes")
	}
	if resolver == nil || resolver.now == nil {
		return nil, errors.New("resolver is required")
	}
	return &Authenticator{
		bearerHash: sha256.Sum256([]byte(bearer)),
		resolver:   resolver,
	}, nil
}

func (a *Authenticator) Verify(
	_ context.Context,
	token string,
	request *http.Request,
) (*auth.TokenInfo, error) {
	candidateHash := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(candidateHash[:], a.bearerHash[:]) != 1 {
		return nil, &authenticationError{code: "auth.invalid_bearer", cause: auth.ErrInvalidToken}
	}
	if request == nil {
		return nil, &authenticationError{code: "identity.headers_invalid", cause: auth.ErrOAuth}
	}

	snapshot, err := a.resolver.Resolve(request.Header)
	if err != nil {
		return nil, &authenticationError{code: "identity.headers_invalid", cause: auth.ErrOAuth}
	}
	return &auth.TokenInfo{
		Expiration: a.resolver.now().UTC().Add(5 * time.Minute),
		UserID:     sessionPrincipal(snapshot, a.bearerHash),
		Extra: map[string]any{
			SnapshotExtraKey: cloneSnapshot(snapshot),
		},
	}, nil
}

func SnapshotFromTokenInfo(info *auth.TokenInfo) (Snapshot, bool) {
	if info == nil || info.Extra == nil {
		return Snapshot{}, false
	}
	snapshot, ok := info.Extra[SnapshotExtraKey].(Snapshot)
	if !ok {
		return Snapshot{}, false
	}
	return cloneSnapshot(snapshot), true
}

func sessionPrincipal(snapshot Snapshot, bearerHash [sha256.Size]byte) string {
	digest := sha256.New()
	writePrincipalFrame(digest, []byte(sessionPrincipalTag))

	switch {
	case snapshot.Verification.Valid && snapshot.SignedIdentity != nil:
		writePrincipalFrame(digest, []byte("signed"))
		writePrincipalFrame(digest, bearerHash[:])
		writePrincipalFrame(digest, []byte(snapshot.SignedIdentity.Subject))
		writePrincipalFrame(digest, []byte(snapshot.SignedIdentity.Mode))
		writePrincipalFrame(digest, []byte(snapshot.SignedIdentity.ConversationPublicID))
		writePrincipalFrame(digest, []byte(snapshot.SignedIdentity.RunID))
	case snapshot.Identity != (HeaderIdentity{}):
		writePrincipalFrame(digest, []byte("plain"))
		writePrincipalFrame(digest, bearerHash[:])
		plainDigest := snapshot.Identity.CanonicalDigest()
		writePrincipalFrame(digest, plainDigest[:])
	default:
		writePrincipalFrame(digest, []byte("bearer"))
		writePrincipalFrame(digest, bearerHash[:])
	}

	return hex.EncodeToString(digest.Sum(nil))
}

func writePrincipalFrame(digest hash.Hash, value []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(value)
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	cloned := snapshot
	if snapshot.SignedIdentity != nil {
		signed := *snapshot.SignedIdentity
		cloned.SignedIdentity = &signed
	}
	cloned.Mismatches = append([]string{}, snapshot.Mismatches...)
	return cloned
}
