package auth

import (
	"encoding/json"
	"reflect"
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
	providerType := reflect.TypeOf(LoginOptionsResponse{}.Providers).Elem()
	if _, exists := providerType.FieldByName("TLSInsecureSkipVerify"); exists {
		t.Fatal("public login options provider contract contains the TLS policy")
	}

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
	tests := []struct {
		name    string
		payload string
		wantNil bool
		want    bool
	}{
		{name: "omitted", payload: `{}`, wantNil: true},
		{name: "explicit false", payload: `{"tlsInsecureSkipVerify":false}`},
		{name: "explicit true", payload: `{"tlsInsecureSkipVerify":true}`, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req UpsertIdentityProviderRequest
			if err := json.Unmarshal([]byte(tt.payload), &req); err != nil {
				t.Fatalf("unmarshal request: %v", err)
			}

			input := toUpsertIdentityProviderInput(req, "admin")
			if tt.wantNil {
				if input.TLSInsecureSkipVerify != nil {
					t.Fatalf("mapped TLS policy = %v, want nil", *input.TLSInsecureSkipVerify)
				}
				return
			}
			if input.TLSInsecureSkipVerify == nil || *input.TLSInsecureSkipVerify != tt.want {
				t.Fatalf("mapped TLS policy = %v, want %v", input.TLSInsecureSkipVerify, tt.want)
			}
		})
	}
}
