package auth

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestProviderWebRedirectsWithoutOriginAllowlist(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		uri  string
		want error
	}{
		{name: "public HTTP IP", uri: "http://68.89.21.9:8080/auth/callback?provider=acme"},
		{name: "unlisted HTTPS domain", uri: "https://web.example.org/auth/callback?provider=acme"},
		{name: "unlisted port", uri: "https://web.example.org:8443/auth/callback?provider=acme"},
		{name: "localhost", uri: "http://localhost:3000/auth/callback?provider=acme"},
		{name: "relative URL", uri: "/auth/callback?provider=acme", want: ErrInvalidRedirectURI},
		{name: "unsupported scheme", uri: "ftp://web.example.org/auth/callback?provider=acme", want: ErrInvalidRedirectURI},
		{name: "missing host", uri: "https:///auth/callback?provider=acme", want: ErrInvalidRedirectURI},
		{name: "wrong path", uri: "https://web.example.org/other?provider=acme", want: ErrInvalidRedirectURI},
		{name: "missing provider", uri: "https://web.example.org/auth/callback", want: ErrInvalidRedirectURI},
		{name: "wrong provider", uri: "https://web.example.org/auth/callback?provider=other", want: ErrInvalidRedirectURI},
		{name: "malformed URL", uri: "http://%/auth/callback?provider=acme", want: ErrInvalidRedirectURI},
	}
	for _, tt := range tests {
		for _, bridge := range []bool{false, true} {
			mode := "direct"
			if bridge {
				mode = "bridge"
			}
			t.Run(mode+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				service, store := newProviderAuthBridgeTestService()
				challenge := providerCodeChallenge(strings.Repeat("c", 43))
				var authorizationURL string
				var err error
				if bridge {
					result, startErr := service.StartProviderAuthBridge(t.Context(), "acme", ProviderAuthBridgeStartInput{
						ClientID: ProviderAuthWebClientID, RedirectURI: tt.uri,
						CodeChallenge: challenge, ClientState: strings.Repeat("s", 43),
					})
					err = startErr
					if result != nil {
						authorizationURL = result.AuthorizationURL
					}
				} else {
					authorizationURL, err = service.BuildProviderAuthURL(t.Context(), "acme", tt.uri, "/chat", challenge, providerIntentLogin)
				}
				if !errors.Is(err, tt.want) {
					t.Fatalf("start auth: got %v, want %v", err, tt.want)
				}
				if tt.want != nil {
					return
				}
				parsed, err := url.Parse(authorizationURL)
				if err != nil {
					t.Fatal(err)
				}
				if !bridge {
					if parsed.Query().Get("redirect_uri") != tt.uri || parsed.Query().Get("code_challenge") != challenge || parsed.Query().Get("code_challenge_method") != "S256" {
						t.Fatal("authorization URL must preserve callback and S256 PKCE")
					}
					return
				}
				state, err := service.verifyProviderAuthBridgeState("acme", parsed.Query().Get("state"))
				if err != nil {
					t.Fatal(err)
				}
				transaction, err := store.ConsumeProviderAuthTransaction(t.Context(), state.TransactionID)
				if err != nil {
					t.Fatal(err)
				}
				if transaction.ClientRedirectURI != tt.uri || transaction.ClientCodeChallenge != challenge {
					t.Fatal("bridge must preserve client callback and PKCE binding")
				}
			})
		}
	}
}
