package security

import (
	"errors"
	"testing"
)

func TestRedactHeadersJSONMasksSensitiveHeaders(t *testing.T) {
	got := RedactHeadersJSON(`{"Authorization":"Bearer secret","X-API-Key":"key","X-Title":"DEEIX"}`)
	want := `{"Authorization":"********","X-API-Key":"********","X-Title":"DEEIX"}`
	if got != want {
		t.Fatalf("unexpected redacted headers: got %s want %s", got, want)
	}
}

func TestMergeRedactedHeadersJSONPreservesExistingSensitiveValue(t *testing.T) {
	t.Parallel()
	got, err := MergeRedactedHeadersJSON(
		`{"X-API-Key":"real-secret","X-Tenant":"old"}`,
		`{"X-API-Key":"********","X-Tenant":"new"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"X-API-Key":"real-secret","X-Tenant":"new"}` {
		t.Fatalf("got %s", got)
	}
}

func TestMergeRedactedHeadersJSONRejectsUnknownSentinel(t *testing.T) {
	t.Parallel()
	_, err := MergeRedactedHeadersJSON(
		`{"X-Tenant":"old"}`,
		`{"X-API-Key":"********","X-Tenant":"new"}`,
	)
	if !errors.Is(err, ErrInvalidRedactedHeaders) {
		t.Fatalf("got %v", err)
	}
}

func TestMergeRedactedHeadersJSONDeletesOmittedSensitiveKey(t *testing.T) {
	t.Parallel()
	got, err := MergeRedactedHeadersJSON(
		`{"X-API-Key":"real-secret","X-Tenant":"old"}`,
		`{"X-Tenant":"new"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"X-Tenant":"new"}` {
		t.Fatalf("got %s", got)
	}
}

func TestParseHeaderStringMapJSONRejectsInvalidMaps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
	}{
		{name: "duplicate key", raw: `{"X-A":"1","X-A":"2"}`},
		{name: "null value", raw: `{"X-A":null}`},
		{name: "trailing value", raw: `{"X-A":"1"} {"X-B":"2"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseHeaderStringMapJSON(tt.raw); err == nil {
				t.Fatalf("ParseHeaderStringMapJSON(%q) unexpectedly succeeded", tt.raw)
			}
		})
	}
}
