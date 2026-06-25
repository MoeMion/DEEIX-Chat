package channel

import (
	"context"
	"testing"

	domainchannel "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/channel"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/cache/memory"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
)

func TestBindingCircuitKeyUsesBindingCode(t *testing.T) {
	if got := bindingCircuitKey("upm_42"); got != "upstream-model-upm_42" {
		t.Fatalf("expected binding-level circuit key, got %q", got)
	}
	if got := bindingCircuitKey("upm:42"); got != "upstream-model-upm-42" {
		t.Fatalf("expected colon-free binding-level circuit key, got %q", got)
	}
	if got := bindingCircuitKey(""); got != "" {
		t.Fatalf("expected empty key for empty binding code, got %q", got)
	}
}

func TestRemoveCandidateUsesUpstreamModelIDInsteadOfPlatformModelName(t *testing.T) {
	items := []routeCandidate{
		{row: repository.ChannelUpstreamRouteRow{UpstreamID: 1, UpstreamModelID: 10, PlatformModelName: "gpt-5.5"}},
		{row: repository.ChannelUpstreamRouteRow{UpstreamID: 1, UpstreamModelID: 11, PlatformModelName: "gpt-5.5"}},
		{row: repository.ChannelUpstreamRouteRow{UpstreamID: 2, UpstreamModelID: 10, PlatformModelName: "gpt-5.5"}},
	}

	got := removeCandidate(items, 1, 10)
	if len(got) != 2 {
		t.Fatalf("expected only one route candidate removed, got %d", len(got))
	}
	for _, item := range got {
		if item.row.UpstreamID == 1 && item.row.UpstreamModelID == 10 {
			t.Fatalf("route candidate was not removed: %#v", got)
		}
	}
}

func TestBuildResolvedRouteSnapshotsModelIdentity(t *testing.T) {
	route := buildResolvedRoute(repository.ChannelUpstreamRouteRow{
		RouteID:           5,
		PlatformModelID:   9,
		PlatformModelName: "gpt-5.5",
		UpstreamModelID:   7,
		UpstreamID:        3,
		UpstreamName:      "OpenAI Official",
		BindingCode:       "upm_abc",
		ModelVendor:       "openai",
		ModelIcon:         "openai",
		UpstreamModelName: "gpt-5.5-20260501",
		Protocol:          "openai_responses",
	}, "sk-test")

	if route.RouteID != 5 || route.PlatformModelID != 9 || route.UpstreamModelID != 7 {
		t.Fatalf("expected route identity snapshot, got %#v", route)
	}
	if route.UpstreamModel != "gpt-5.5-20260501" {
		t.Fatalf("expected upstream model name, got %q", route.UpstreamModel)
	}
	if route.PlatformModelName != "gpt-5.5" || route.BindingCode != "upm_abc" || route.ModelVendor != "openai" || route.ModelIcon != "openai" {
		t.Fatalf("expected platform model snapshot, got %#v", route)
	}
}

func TestRecordCircuitFailureUsesPlatformModelDefaults(t *testing.T) {
	cache := memory.NewChannelCache(memory.New())
	service := &Service{
		repo:  &modelUpdateRepo{},
		cache: cache,
	}
	route := &ResolvedRoute{
		UpstreamID:                      1,
		BindingCode:                     "upm_abc",
		PlatformModelCbFailureThreshold: 1,
		PlatformModelCbDurationMin:      1,
		PlatformModelCbWindowMin:        1,
	}

	service.recordCircuitFailure(context.Background(), route, domainchannel.BreakerDefaults{
		ModelFailureThreshold: 3,
		ModelDurationMin:      1,
		ModelWindowMin:        1,
	})

	open, _ := cache.QueryModelCircuitStatus(context.Background(), 1, "upstream-model-upm_abc")
	if !open {
		t.Fatal("expected platform model default threshold to open circuit")
	}
}

func TestRecordCircuitFailureRouteOverrideBeatsPlatformModelDefaults(t *testing.T) {
	cache := memory.NewChannelCache(memory.New())
	service := &Service{
		repo:  &modelUpdateRepo{},
		cache: cache,
	}
	route := &ResolvedRoute{
		UpstreamID:                      1,
		BindingCode:                     "upm_abc",
		PlatformModelCbFailureThreshold: 1,
		PlatformModelCbDurationMin:      1,
		PlatformModelCbWindowMin:        1,
		ModelCbFailureThreshold:         2,
		ModelCbDurationMin:              1,
		ModelCbWindowMin:                1,
	}

	service.recordCircuitFailure(context.Background(), route, domainchannel.BreakerDefaults{
		ModelFailureThreshold: 3,
		ModelDurationMin:      1,
		ModelWindowMin:        1,
	})

	open, _ := cache.QueryModelCircuitStatus(context.Background(), 1, "upstream-model-upm_abc")
	if open {
		t.Fatal("expected route override threshold to keep circuit closed after one failure")
	}
}

func TestRecordCircuitFailurePlatformModelPolicyEnforcedBeatsRouteOverride(t *testing.T) {
	cache := memory.NewChannelCache(memory.New())
	service := &Service{
		repo:  &modelUpdateRepo{},
		cache: cache,
	}
	route := &ResolvedRoute{
		UpstreamID:                      1,
		BindingCode:                     "upm_abc",
		PlatformModelCbPolicyMode:       "enforced",
		PlatformModelCbFailureThreshold: 1,
		PlatformModelCbDurationMin:      1,
		PlatformModelCbWindowMin:        1,
		ModelCbFailureThreshold:         2,
		ModelCbDurationMin:              1,
		ModelCbWindowMin:                1,
	}

	service.recordCircuitFailure(context.Background(), route, domainchannel.BreakerDefaults{
		ModelFailureThreshold: 3,
		ModelDurationMin:      1,
		ModelWindowMin:        1,
	})

	open, _ := cache.QueryModelCircuitStatus(context.Background(), 1, "upstream-model-upm_abc")
	if !open {
		t.Fatal("expected enforced platform model policy to open circuit after one failure")
	}
}

func TestReleaseGrantedRouteProbesOnlyReleasesGrantedScopes(t *testing.T) {
	cache := &releaseProbeCache{}
	service := &Service{cache: cache}

	service.releaseGrantedRouteProbes(context.Background(), &ResolvedRoute{
		UpstreamID:           1,
		UpstreamModelID:      2,
		BindingCode:          "upm_abc",
		UpstreamProbeGranted: false,
		ModelProbeGranted:    true,
	})

	if len(cache.calls) != 1 {
		t.Fatalf("expected one probe release, got %d", len(cache.calls))
	}
	if cache.calls[0].upstreamID != 1 || cache.calls[0].modelKey != "upstream-model-upm_abc" {
		t.Fatalf("unexpected probe release call: %#v", cache.calls[0])
	}
}

type releaseProbeCall struct {
	upstreamID uint
	modelKey   string
}

type releaseProbeCache struct {
	repository.ChannelCacheRepository
	calls []releaseProbeCall
}

func (c *releaseProbeCache) ReleaseRouteProbes(_ context.Context, upstreamID uint, modelKey string) error {
	c.calls = append(c.calls, releaseProbeCall{upstreamID: upstreamID, modelKey: modelKey})
	return nil
}

func TestRouteScopeAllowsModelAccessDefaultsToUserScope(t *testing.T) {
	for _, scope := range []string{"", "unknown", RouteScopeUser} {
		if routeScopeAllowsModelAccess(scope, ModelAccessScopeInternal) {
			t.Fatalf("scope %q should not access internal model", scope)
		}
		if !routeScopeAllowsModelAccess(scope, ModelAccessScopePublic) {
			t.Fatalf("scope %q should access public model", scope)
		}
	}
}

func TestRouteScopeAllowsInternalModelForInternalScope(t *testing.T) {
	if !routeScopeAllowsModelAccess(RouteScopeInternal, ModelAccessScopeInternal) {
		t.Fatalf("internal scope should access internal model")
	}
}
