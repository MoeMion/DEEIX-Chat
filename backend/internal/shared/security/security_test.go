package security

import (
	"context"
	"errors"
	"net"
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

func TestValidateOutboundHTTPURLRejectsPrivateIPInProduction(t *testing.T) {
	if err := ValidateOutboundHTTPURL("http://127.0.0.1:8080", "prod", true); err == nil {
		t.Fatal("expected private loopback URL to be rejected in production")
	}
	if err := ValidateOutboundHTTPURL("http://127.0.0.1:8080", "dev", true); err != nil {
		t.Fatalf("dev URL should remain available for local testing: %v", err)
	}
}

func TestValidateOutboundHTTPURLRejectsMetadataIP(t *testing.T) {
	if err := ValidateOutboundHTTPURL("http://100.100.100.200/latest/meta-data", "prod", true); err == nil {
		t.Fatal("expected cloud metadata URL to be rejected")
	}
}

func TestValidateOutboundHTTPURLAllowsPrivateIPWhenSSRFProtectionDisabled(t *testing.T) {
	if err := ValidateOutboundHTTPURL("http://127.0.0.1:8080", "prod", false); err != nil {
		t.Fatalf("SSRF protection disabled should allow private URL: %v", err)
	}
	if err := ValidateOutboundHTTPURL("http://100.100.100.200/latest/meta-data", "prod", false); err != nil {
		t.Fatalf("SSRF protection disabled should allow metadata URL: %v", err)
	}
}

func TestOutboundDialerRejectsResolvedPrivateIPInProduction(t *testing.T) {
	dialCalled := false
	dial := newOutboundDialContext(
		"prod",
		true,
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("10.0.0.10")}}, nil
		},
		func(context.Context, string, string) (net.Conn, error) {
			dialCalled = true
			return nil, errors.New("dial should not be called")
		},
	)

	_, err := dial(context.Background(), "tcp", "example.com:443")
	if !errors.Is(err, ErrUnsafeOutboundURL) {
		t.Fatalf("expected unsafe outbound error, got %v", err)
	}
	if dialCalled {
		t.Fatal("unsafe resolved IP must be rejected before dialing")
	}
}

func TestOutboundDialerRejectsMixedResolvedIPsInProduction(t *testing.T) {
	dialCalled := false
	dial := newOutboundDialContext(
		"prod",
		true,
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{
				{IP: net.ParseIP("8.8.8.8")},
				{IP: net.ParseIP("169.254.169.254")},
			}, nil
		},
		func(context.Context, string, string) (net.Conn, error) {
			dialCalled = true
			return nil, errors.New("dial should not be called")
		},
	)

	_, err := dial(context.Background(), "tcp", "example.com:443")
	if !errors.Is(err, ErrUnsafeOutboundURL) {
		t.Fatalf("expected unsafe outbound error, got %v", err)
	}
	if dialCalled {
		t.Fatal("mixed safe and unsafe DNS answers must be rejected before dialing")
	}
}

func TestOutboundDialerDialsResolvedPublicIPInProduction(t *testing.T) {
	var dialAddress string
	dialErr := errors.New("dial sentinel")
	dial := newOutboundDialContext(
		"prod",
		true,
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
		},
		func(_ context.Context, _ string, address string) (net.Conn, error) {
			dialAddress = address
			return nil, dialErr
		},
	)

	_, err := dial(context.Background(), "tcp", "example.com:443")
	if !errors.Is(err, dialErr) {
		t.Fatalf("expected dial sentinel, got %v", err)
	}
	if dialAddress != "8.8.8.8:443" {
		t.Fatalf("expected dial by resolved IP, got %q", dialAddress)
	}
}

func TestOutboundDialerSkipsResolutionOutsideProduction(t *testing.T) {
	resolveCalled := false
	var dialAddress string
	dialErr := errors.New("dial sentinel")
	dial := newOutboundDialContext(
		"dev",
		true,
		func(context.Context, string) ([]net.IPAddr, error) {
			resolveCalled = true
			return nil, errors.New("resolve should not be called")
		},
		func(_ context.Context, _ string, address string) (net.Conn, error) {
			dialAddress = address
			return nil, dialErr
		},
	)

	_, err := dial(context.Background(), "tcp", "localhost:8080")
	if !errors.Is(err, dialErr) {
		t.Fatalf("expected dial sentinel, got %v", err)
	}
	if resolveCalled {
		t.Fatal("development dialer should not resolve or enforce SSRF policy")
	}
	if dialAddress != "localhost:8080" {
		t.Fatalf("expected original address, got %q", dialAddress)
	}
}

func TestOutboundDialerSkipsResolutionWhenSSRFProtectionDisabled(t *testing.T) {
	resolveCalled := false
	var dialAddress string
	dialErr := errors.New("dial sentinel")
	dial := newOutboundDialContext(
		"prod",
		false,
		func(context.Context, string) ([]net.IPAddr, error) {
			resolveCalled = true
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		},
		func(_ context.Context, _ string, address string) (net.Conn, error) {
			dialAddress = address
			return nil, dialErr
		},
	)

	_, err := dial(context.Background(), "tcp", "localhost:8080")
	if !errors.Is(err, dialErr) {
		t.Fatalf("expected dial sentinel, got %v", err)
	}
	if resolveCalled {
		t.Fatal("disabled SSRF protection should not resolve or enforce outbound policy")
	}
	if dialAddress != "localhost:8080" {
		t.Fatalf("expected original address, got %q", dialAddress)
	}
}
