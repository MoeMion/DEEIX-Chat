package auth

import (
	"encoding/json"
	"testing"

	appauth "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/application/auth"
)

func providerJSON(t *testing.T, value interface{}) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	var decoded map[string]interface{}
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal JSON: %v", err)
	}
	return decoded
}

func TestLoginOptionsResponseOmitsTLSInsecureSkipVerify(t *testing.T) {
	response := toLoginOptionsResponse(&appauth.LoginOptions{
		Providers: []appauth.IdentityProviderView{{Name: "Public Provider"}},
	})
	raw := providerJSON(t, response)
	providers := raw["providers"].([]interface{})
	provider := providers[0].(map[string]interface{})
	if _, exists := provider["tlsInsecureSkipVerify"]; exists {
		t.Fatal("public login options exposed the TLS policy")
	}
}

func TestIdentityProviderResponseIncludesTLSInsecureSkipVerify(t *testing.T) {
	for _, value := range []bool{false, true} {
		value := value
		response := toIdentityProviderResponse(appauth.IdentityProviderView{
			TLSInsecureSkipVerify: &value,
		})
		raw := providerJSON(t, response)
		if got, exists := raw["tlsInsecureSkipVerify"]; !exists || got != value {
			t.Fatalf("TLS policy = %v, exists=%v, want %v", got, exists, value)
		}
	}
}

func TestToUpsertIdentityProviderInputMapsTLSInsecureSkipVerify(t *testing.T) {
	value := true
	input := toUpsertIdentityProviderInput(UpsertIdentityProviderRequest{
		TLSInsecureSkipVerify: &value,
	}, "admin")
	if input.TLSInsecureSkipVerify == nil || !*input.TLSInsecureSkipVerify {
		t.Fatalf("mapped TLS policy = %v, want true", input.TLSInsecureSkipVerify)
	}
}
