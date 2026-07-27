package identity

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"
)

const testBearerToken = "0123456789abcdef0123456789abcdef"

func TestAuthenticatorConstructorValidatesDependencies(t *testing.T) {
	t.Parallel()

	resolver := mustTestResolver(t, validSignedConfig())
	for _, bearer := range []string{"", strings.Repeat("b", 31)} {
		bearer := bearer
		t.Run("weak bearer", func(t *testing.T) {
			t.Parallel()
			if _, err := NewAuthenticator(bearer, resolver); err == nil {
				t.Fatal("NewAuthenticator() accepted a weak bearer")
			} else if bearer != "" && strings.Contains(err.Error(), bearer) {
				t.Fatalf("NewAuthenticator() error leaked bearer: %v", err)
			}
		})
	}
	if _, err := NewAuthenticator(testBearerToken, nil); err == nil {
		t.Fatal("NewAuthenticator() accepted a nil resolver")
	}
	if authenticator, err := NewAuthenticator(testBearerToken, resolver); err != nil || authenticator == nil {
		t.Fatalf("NewAuthenticator(valid) = %#v, %v", authenticator, err)
	}
}

func TestAuthenticatorRejectsWrongBearerBeforeParsingIdentity(t *testing.T) {
	t.Parallel()

	authenticator := mustTestAuthenticator(t, mustTestResolver(t, validSignedConfig()))
	secret := "plain-header-secret"
	request := requestWithHeaders(http.Header{
		"X-MCP-CLIENT-USER-EMAIL": {secret + "\x00"},
	})
	info, err := authenticator.Verify(t.Context(), "wrong-bearer", request)
	if info != nil {
		t.Fatalf("TokenInfo = %#v, want nil", info)
	}
	if err == nil || err.Error() != "auth.invalid_bearer" {
		t.Fatalf("error = %v, want auth.invalid_bearer", err)
	}
	if !errors.Is(err, auth.ErrInvalidToken) || errors.Is(err, auth.ErrOAuth) {
		t.Fatalf("error chain = %v, want only auth.ErrInvalidToken", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "wrong-bearer") {
		t.Fatalf("error leaked request data: %v", err)
	}
}

func TestAuthenticatorMapsPlainHeaderFailuresToOAuth(t *testing.T) {
	t.Parallel()

	authenticator := mustTestAuthenticator(t, mustTestResolver(t, validSignedConfig()))
	secret := "plain-header-secret"
	request := requestWithHeaders(http.Header{
		"X-MCP-CLIENT-USER-EMAIL": {secret + "\x00"},
	})
	info, err := authenticator.Verify(t.Context(), testBearerToken, request)
	if info != nil {
		t.Fatalf("TokenInfo = %#v, want nil", info)
	}
	if err == nil || err.Error() != "identity.headers_invalid" {
		t.Fatalf("error = %v, want identity.headers_invalid", err)
	}
	if !errors.Is(err, auth.ErrOAuth) || errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("error chain = %v, want only auth.ErrOAuth", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked identity value: %v", err)
	}

	if _, err := authenticator.Verify(t.Context(), testBearerToken, nil); err == nil ||
		err.Error() != "identity.headers_invalid" || !errors.Is(err, auth.ErrOAuth) {
		t.Fatalf("nil request error = %v, want safe OAuth classification", err)
	}
}

func TestAuthenticatorKeepsSignedFailuresDiagnostic(t *testing.T) {
	t.Parallel()

	resolver := mustTestResolver(t, validSignedConfig())
	authenticator := mustTestAuthenticator(t, resolver)
	validClaims := validTestContextClaims("chat")
	validRaw := signTestContext(t, jwt.SigningMethodHS256, validClaims, "JWT", testKeyID, []byte(testSecret))
	tests := []struct {
		name       string
		headers    http.Header
		wantReason string
	}{
		{name: "missing", headers: matchingPlainHeaders(), wantReason: "signed_context_missing"},
		{name: "invalid", headers: withSignedHeader(matchingPlainHeaders(), "invalid"), wantReason: "signed_context_invalid"},
		{name: "mismatch", headers: withMismatchingDisplayName(withSignedHeader(matchingPlainHeaders(), validRaw)), wantReason: "signed_context_mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info, err := authenticator.Verify(t.Context(), testBearerToken, requestWithHeaders(tt.headers))
			if err != nil {
				t.Fatalf("Verify(): %v", err)
			}
			snapshot, ok := SnapshotFromTokenInfo(info)
			if !ok || snapshot.Verification.Reason != tt.wantReason || snapshot.Verification.Verified {
				t.Fatalf("Snapshot = %#v, %v, want diagnostic %s", snapshot, ok, tt.wantReason)
			}
		})
	}
}

func TestAuthenticatorDerivesGoldenSessionPrincipals(t *testing.T) {
	t.Parallel()

	resolver := mustTestResolver(t, validSignedConfig())
	authenticator := mustTestAuthenticator(t, resolver)
	validRaw := signTestContext(t, jwt.SigningMethodHS256, validTestContextClaims("chat"), "JWT", testKeyID, []byte(testSecret))
	tests := []struct {
		name       string
		headers    http.Header
		wantUserID string
		wantReason string
	}{
		{
			name:       "signed",
			headers:    withSignedHeader(matchingPlainHeaders(), validRaw),
			wantUserID: "9fe183eb54c68a06ee71834a7d91b0d9da694acfb1a7a4f0fe383e5cb301afc2",
			wantReason: "signed_context_verified",
		},
		{
			name:       "valid signed mismatch still uses signed principal",
			headers:    withMismatchingDisplayName(withSignedHeader(matchingPlainHeaders(), validRaw)),
			wantUserID: "9fe183eb54c68a06ee71834a7d91b0d9da694acfb1a7a4f0fe383e5cb301afc2",
			wantReason: "signed_context_mismatch",
		},
		{
			name:       "plain",
			headers:    matchingPlainHeaders(),
			wantUserID: "dff14d002bcf91e63823177c396eb636455e34909b12add5dfbb846e41f187e1",
			wantReason: "signed_context_missing",
		},
		{
			name:       "invalid signed falls back to plain",
			headers:    withSignedHeader(matchingPlainHeaders(), "invalid"),
			wantUserID: "dff14d002bcf91e63823177c396eb636455e34909b12add5dfbb846e41f187e1",
			wantReason: "signed_context_invalid",
		},
		{
			name:       "bearer only",
			headers:    http.Header{},
			wantUserID: "a0d42c7ec34dd3b4235aa13ec373d05784afcc11341e1e0c5bf8019568dcf558",
			wantReason: "signed_context_missing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info, err := authenticator.Verify(t.Context(), testBearerToken, requestWithHeaders(tt.headers))
			if err != nil {
				t.Fatalf("Verify(): %v", err)
			}
			if info.UserID != tt.wantUserID {
				t.Fatalf("UserID = %q, want %q", info.UserID, tt.wantUserID)
			}
			snapshot, ok := SnapshotFromTokenInfo(info)
			if !ok || snapshot.Verification.Reason != tt.wantReason {
				t.Fatalf("Snapshot = %#v, %v, want reason %s", snapshot, ok, tt.wantReason)
			}
		})
	}
}

func TestAuthenticatorSignedPrincipalExcludesEphemeralClaims(t *testing.T) {
	t.Parallel()

	resolver := mustTestResolver(t, validSignedConfig())
	authenticator := mustTestAuthenticator(t, resolver)
	firstClaims := validTestContextClaims("chat")
	firstRaw := signTestContext(t, jwt.SigningMethodHS256, firstClaims, "JWT", testKeyID, []byte(testSecret))
	firstInfo := verifyTestAuthenticator(t, authenticator, withSignedHeader(nil, firstRaw))

	secondClaims := validTestContextClaims("chat")
	issuedAt := testNow.Add(-2 * time.Minute)
	secondClaims.IssuedAt = jwt.NewNumericDate(issuedAt)
	secondClaims.NotBefore = jwt.NewNumericDate(issuedAt)
	secondClaims.ExpiresAt = jwt.NewNumericDate(issuedAt.Add(5 * time.Minute))
	secondClaims.ID = "different-jti"
	secondClaims.Name = "Different Name"
	secondClaims.Email = "different@example.test"
	secondClaims.Role = "viewer"
	secondRaw := signTestContext(t, jwt.SigningMethodHS256, secondClaims, "JWT", testKeyID, []byte(testSecret))
	secondInfo := verifyTestAuthenticator(t, authenticator, withSignedHeader(nil, secondRaw))
	if firstInfo.UserID != secondInfo.UserID {
		t.Fatalf("ephemeral/optional claims changed UserID: %q != %q", firstInfo.UserID, secondInfo.UserID)
	}

	secondClaims.ConversationPublicID = "different-conversation"
	changedRaw := signTestContext(t, jwt.SigningMethodHS256, secondClaims, "JWT", testKeyID, []byte(testSecret))
	changedInfo := verifyTestAuthenticator(t, authenticator, withSignedHeader(nil, changedRaw))
	if firstInfo.UserID == changedInfo.UserID {
		t.Fatal("stable conversation identity did not change UserID")
	}
}

func TestAuthenticatorStoresOnlyDefensiveSnapshotAndFiveMinuteUTCExpiration(t *testing.T) {
	t.Parallel()

	resolver := mustTestResolver(t, validSignedConfig())
	authenticator := mustTestAuthenticator(t, resolver)
	raw := signTestContext(t, jwt.SigningMethodHS256, validTestContextClaims("chat"), "JWT", testKeyID, []byte(testSecret))
	headers := withMismatchingDisplayName(withSignedHeader(matchingPlainHeaders(), raw))
	info := verifyTestAuthenticator(t, authenticator, headers)

	wantExpiration := testNow.UTC().Add(5 * time.Minute)
	if !info.Expiration.Equal(wantExpiration) || info.Expiration.Location() != time.UTC {
		t.Fatalf("Expiration = %v (%v), want %v UTC", info.Expiration, info.Expiration.Location(), wantExpiration)
	}
	if len(info.Extra) != 1 {
		t.Fatalf("Extra = %#v, want exactly one key", info.Extra)
	}
	stored, ok := info.Extra[SnapshotExtraKey]
	if !ok {
		t.Fatalf("Extra missing %q", SnapshotExtraKey)
	}
	if _, ok := stored.(Snapshot); !ok {
		t.Fatalf("Extra[%q] = %T, want Snapshot value", SnapshotExtraKey, stored)
	}

	first, ok := SnapshotFromTokenInfo(info)
	if !ok || first.SignedIdentity == nil || len(first.Mismatches) == 0 {
		t.Fatalf("SnapshotFromTokenInfo() = %#v, %v", first, ok)
	}
	first.SignedIdentity.Name = "mutated"
	first.Mismatches[0] = "mutated"
	second, ok := SnapshotFromTokenInfo(info)
	if !ok || second.SignedIdentity == nil || second.SignedIdentity.Name == "mutated" || second.Mismatches[0] == "mutated" {
		t.Fatalf("SnapshotFromTokenInfo() exposed shared storage: %#v", second)
	}

	if _, ok := SnapshotFromTokenInfo(nil); ok {
		t.Fatal("SnapshotFromTokenInfo(nil) succeeded")
	}
	if _, ok := SnapshotFromTokenInfo(&auth.TokenInfo{}); ok {
		t.Fatal("SnapshotFromTokenInfo(empty) succeeded")
	}
	if _, ok := SnapshotFromTokenInfo(&auth.TokenInfo{Extra: map[string]any{SnapshotExtraKey: "wrong-type"}}); ok {
		t.Fatal("SnapshotFromTokenInfo(wrong type) succeeded")
	}
}

func TestAuthenticatorPreservesEmptyMismatchSliceThroughTokenInfo(t *testing.T) {
	t.Parallel()

	authenticator := mustTestAuthenticator(t, mustTestResolver(t, validSignedConfig()))
	verifiedRaw := signTestContext(
		t,
		jwt.SigningMethodHS256,
		validTestContextClaims("chat"),
		"JWT",
		testKeyID,
		[]byte(testSecret),
	)
	tests := []struct {
		name    string
		headers http.Header
	}{
		{name: "verified", headers: withSignedHeader(matchingPlainHeaders(), verifiedRaw)},
		{name: "bearer only", headers: http.Header{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info := verifyTestAuthenticator(t, authenticator, tt.headers)
			snapshot, ok := SnapshotFromTokenInfo(info)
			if !ok {
				t.Fatal("SnapshotFromTokenInfo() did not find the snapshot")
			}
			if snapshot.Mismatches == nil || len(snapshot.Mismatches) != 0 {
				t.Fatalf("Mismatches = %#v, want non-nil empty slice", snapshot.Mismatches)
			}
		})
	}
}

func TestAuthenticatorVerifyMatchesSDKTokenVerifier(t *testing.T) {
	t.Parallel()

	authenticator := mustTestAuthenticator(t, mustTestResolver(t, validSignedConfig()))
	var verifier auth.TokenVerifier = authenticator.Verify
	info, err := verifier(t.Context(), testBearerToken, requestWithHeaders(nil))
	if err != nil || info == nil {
		t.Fatalf("TokenVerifier() = %#v, %v", info, err)
	}
}

func TestSnapshotFromTokenInfoReturnsExactSnapshotCopy(t *testing.T) {
	t.Parallel()

	want := Snapshot{
		Identity:       HeaderIdentity{UserPublicID: "user"},
		SignedIdentity: &SignedIdentity{Subject: "subject"},
		Verification:   Verification{Present: true, Configured: true, Valid: true, Verified: true, Reason: "signed_context_verified"},
		Mismatches:     []string{"userPublicID"},
	}
	info := &auth.TokenInfo{Extra: map[string]any{SnapshotExtraKey: want}}
	got, ok := SnapshotFromTokenInfo(info)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("SnapshotFromTokenInfo() = %#v, %v, want %#v", got, ok, want)
	}
	got.SignedIdentity.Subject = "mutated"
	got.Mismatches[0] = "mutated"
	again, ok := SnapshotFromTokenInfo(info)
	if !ok || !reflect.DeepEqual(again, want) {
		t.Fatalf("SnapshotFromTokenInfo() was not defensive: %#v, %v", again, ok)
	}
}

func mustTestAuthenticator(t *testing.T, resolver *Resolver) *Authenticator {
	t.Helper()
	authenticator, err := NewAuthenticator(testBearerToken, resolver)
	if err != nil {
		t.Fatalf("NewAuthenticator(): %v", err)
	}
	return authenticator
}

func verifyTestAuthenticator(t *testing.T, authenticator *Authenticator, headers http.Header) *auth.TokenInfo {
	t.Helper()
	info, err := authenticator.Verify(t.Context(), testBearerToken, requestWithHeaders(headers))
	if err != nil {
		t.Fatalf("Verify(): %v", err)
	}
	if info == nil {
		t.Fatal("Verify() returned nil TokenInfo")
	}
	return info
}

func requestWithHeaders(headers http.Header) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", nil)
	request.Header = headers.Clone()
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	return request
}

func withSignedHeader(headers http.Header, raw string) http.Header {
	cloned := headers.Clone()
	if cloned == nil {
		cloned = make(http.Header)
	}
	cloned.Set(testSignedHeader, raw)
	return cloned
}

func withMismatchingDisplayName(headers http.Header) http.Header {
	cloned := headers.Clone()
	for name := range cloned {
		if strings.EqualFold(name, "X-MCP-CLIENT-USER-DISPLAY-NAME") {
			cloned[name] = []string{"different"}
			return cloned
		}
	}
	cloned.Set("X-MCP-CLIENT-USER-DISPLAY-NAME", "different")
	return cloned
}
