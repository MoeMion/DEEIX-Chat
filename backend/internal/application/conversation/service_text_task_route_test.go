package conversation

import (
	"context"
	"errors"
	"testing"

	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/application/channel"
)

type textTaskRouteResolverStub struct {
	routes       map[string]*channel.ResolvedRoute
	defaultRoute *channel.ResolvedRoute
	fail         map[string]error
}

func (r *textTaskRouteResolverStub) ResolveRoute(_ context.Context, input channel.ResolveRouteInput) (*channel.ResolvedRoute, error) {
	if err := r.fail[input.PlatformModelName]; err != nil {
		return nil, err
	}
	route := r.routes[input.PlatformModelName]
	if route == nil {
		return nil, errors.New("route not found")
	}
	return route, nil
}

func (r *textTaskRouteResolverStub) ResolveDefaultRoute(context.Context, channel.ResolveRouteInput) (*channel.ResolvedRoute, error) {
	if r.defaultRoute == nil {
		return nil, errors.New("default route not found")
	}
	return r.defaultRoute, nil
}

func (r *textTaskRouteResolverStub) MarkRouteFailure(context.Context, *channel.ResolvedRoute, error) {
}

func (r *textTaskRouteResolverStub) MarkRouteSuccess(context.Context, *channel.ResolvedRoute) {}

func TestResolveTextTaskRouteCandidatesFollowUsesCurrentThenDefault(t *testing.T) {
	service := &Service{routeResolver: &textTaskRouteResolverStub{
		routes: map[string]*channel.ResolvedRoute{
			"grok-4.3": {PlatformModelName: "grok-4.3", BindingCode: "current", Protocol: "xai_responses", UpstreamModel: "grok-4.3"},
		},
		defaultRoute: &channel.ResolvedRoute{PlatformModelName: "gpt-5-mini", BindingCode: "default", Protocol: "openai_responses", UpstreamModel: "gpt-5-mini"},
	}}

	routes, err := service.resolveTextTaskRouteCandidates(context.Background(), textTaskFollowModel, "grok-4.3", 1, 2, "")
	if err != nil {
		t.Fatalf("resolve candidates: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("expected current and default routes, got %#v", routes)
	}
	if routes[0].BindingCode != "current" || routes[1].BindingCode != "default" {
		t.Fatalf("unexpected route order: %#v", routes)
	}
}

func TestResolveTextTaskRouteCandidatesSpecifiedModelDoesNotAddDefault(t *testing.T) {
	service := &Service{routeResolver: &textTaskRouteResolverStub{
		routes: map[string]*channel.ResolvedRoute{
			"gpt-5-mini": {PlatformModelName: "gpt-5-mini", BindingCode: "specified", Protocol: "openai_responses", UpstreamModel: "gpt-5-mini"},
		},
		defaultRoute: &channel.ResolvedRoute{PlatformModelName: "fallback", BindingCode: "default", Protocol: "openai_responses", UpstreamModel: "fallback"},
	}}

	routes, err := service.resolveTextTaskRouteCandidates(context.Background(), "gpt-5-mini", "grok-4.3", 1, 2, "")
	if err != nil {
		t.Fatalf("resolve candidates: %v", err)
	}
	if len(routes) != 1 || routes[0].BindingCode != "specified" {
		t.Fatalf("expected only specified route, got %#v", routes)
	}
}

func TestResolveTextTaskRouteCandidatesFollowFallsBackWhenCurrentRouteFails(t *testing.T) {
	service := &Service{routeResolver: &textTaskRouteResolverStub{
		routes: map[string]*channel.ResolvedRoute{},
		fail: map[string]error{
			"grok-4.3": errors.New("current route unavailable"),
		},
		defaultRoute: &channel.ResolvedRoute{PlatformModelName: "gpt-5-mini", BindingCode: "default", Protocol: "openai_responses", UpstreamModel: "gpt-5-mini"},
	}}

	routes, err := service.resolveTextTaskRouteCandidates(context.Background(), textTaskFollowModel, "grok-4.3", 1, 2, "")
	if err != nil {
		t.Fatalf("resolve candidates: %v", err)
	}
	if len(routes) != 1 || routes[0].BindingCode != "default" {
		t.Fatalf("expected default route after current route failure, got %#v", routes)
	}
}
