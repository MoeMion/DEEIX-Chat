package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	domainbilling "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/billing"
)

func TestListCurrentSubscriptionSnapshotsExtendsContiguousSamePlan(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	firstEnd := now.Add(30 * day)
	secondEnd := now.Add(60 * day)
	service := NewService(&billingRepositoryStub{
		plans: []domainbilling.Plan{
			{ID: 2, Code: "pro", Name: "Pro", SortOrder: 20, IsActive: true},
		},
		subscriptions: []domainbilling.Subscription{
			{ID: 10, UserID: 1, PlanID: 2, PriceID: 20, Status: "active", CurrentPeriodStartAt: now, CurrentPeriodEndAt: &firstEnd},
			{ID: 11, UserID: 1, PlanID: 2, PriceID: 20, Status: "active", CurrentPeriodStartAt: firstEnd, CurrentPeriodEndAt: &secondEnd},
		},
	})

	snapshots, err := service.ListCurrentSubscriptionSnapshots(context.Background(), []uint{1}, now)
	if err != nil {
		t.Fatalf("ListCurrentSubscriptionSnapshots() error = %v", err)
	}
	snapshot, ok := snapshots[1]
	if !ok {
		t.Fatal("ListCurrentSubscriptionSnapshots() missing user snapshot")
	}
	if snapshot.ExpiresAt == nil || !snapshot.ExpiresAt.Equal(secondEnd) {
		t.Fatalf("snapshot.ExpiresAt = %v, want %v", snapshot.ExpiresAt, secondEnd)
	}
}

func TestSubscribeFreePlanRejectsActivePaidEntitlements(t *testing.T) {
	now := time.Now().Add(30 * 24 * time.Hour)
	repo := &billingRepositoryStub{
		plans: []domainbilling.Plan{
			{ID: 1, Code: "free", Name: "Free", IsActive: true},
			{ID: 2, Code: "pro", Name: "Pro", SortOrder: 20, IsActive: true},
		},
		prices: []domainbilling.Price{
			{ID: 10, PlanID: 1, BillingInterval: domainbilling.IntervalLifetime, AmountCents: 0, IsActive: true},
		},
		subscriptions: []domainbilling.Subscription{
			{ID: 20, UserID: 1, PlanID: 2, PriceID: 20, Status: "active", CurrentPeriodStartAt: time.Now(), CurrentPeriodEndAt: &now},
		},
	}
	service := NewService(repo)

	_, err := service.Subscribe(context.Background(), 1, 10, 1)
	if !errors.Is(err, ErrSubscriptionEntitlementActive) {
		t.Fatalf("Subscribe() error = %v, want ErrSubscriptionEntitlementActive", err)
	}
	if repo.replacedSubscription != nil {
		t.Fatal("Subscribe() replaced subscription despite active paid entitlement")
	}
}

func TestCreatePaymentOrderAllowsLowerTierRenewalAfterActiveEntitlement(t *testing.T) {
	now := time.Now().Add(30 * 24 * time.Hour)
	repo := &billingRepositoryStub{
		mode: "period",
		plans: []domainbilling.Plan{
			{ID: 2, Code: "pro", Name: "Pro", SortOrder: 20, IsActive: true},
			{ID: 4, Code: "ultra", Name: "Ultra", SortOrder: 40, IsActive: true},
		},
		prices: []domainbilling.Price{
			{ID: 20, PlanID: 2, BillingInterval: domainbilling.IntervalMonth, Currency: "USD", AmountCents: 2000, IsActive: true},
		},
		subscriptions: []domainbilling.Subscription{
			{ID: 40, UserID: 1, PlanID: 4, PriceID: 40, Status: "active", CurrentPeriodStartAt: time.Now(), CurrentPeriodEndAt: &now},
		},
	}
	service := NewService(repo)

	_, _, _, err := service.CreatePaymentOrder(context.Background(), PaymentOrderInput{
		UserID:   1,
		PriceID:  20,
		Provider: domainbilling.PaymentProviderStripe,
	})
	if err != nil {
		t.Fatalf("CreatePaymentOrder() error = %v", err)
	}
}

func TestCreatePaymentOrderAllowsUpgradeWithActivePaidEntitlement(t *testing.T) {
	now := time.Now().Add(30 * 24 * time.Hour)
	repo := &billingRepositoryStub{
		mode: "period",
		plans: []domainbilling.Plan{
			{ID: 2, Code: "pro", Name: "Pro", SortOrder: 20, IsActive: true},
			{ID: 4, Code: "ultra", Name: "Ultra", SortOrder: 40, IsActive: true},
		},
		prices: []domainbilling.Price{
			{ID: 40, PlanID: 4, BillingInterval: domainbilling.IntervalMonth, Currency: "USD", AmountCents: 20000, IsActive: true},
		},
		subscriptions: []domainbilling.Subscription{
			{ID: 20, UserID: 1, PlanID: 2, PriceID: 20, Status: "active", CurrentPeriodStartAt: time.Now(), CurrentPeriodEndAt: &now},
		},
	}
	service := NewService(repo)

	order, _, _, err := service.CreatePaymentOrder(context.Background(), PaymentOrderInput{
		UserID:   1,
		PriceID:  40,
		Provider: domainbilling.PaymentProviderStripe,
	})
	if err != nil {
		t.Fatalf("CreatePaymentOrder() error = %v", err)
	}
	if order == nil {
		t.Fatal("CreatePaymentOrder() returned nil order")
	}
}

func TestBuildSubscriptionEntitlementViewsShowsCurrentAndQueuedPlans(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	maxEnd := now.Add(30 * day)
	proEnd := now.Add(60 * day)
	plans := map[uint]domainbilling.Plan{
		2: {ID: 2, Code: "pro", Name: "Pro", SortOrder: 20, IsActive: true},
		3: {ID: 3, Code: "max", Name: "Max", SortOrder: 30, IsActive: true},
	}

	views := buildSubscriptionEntitlementViews([]domainbilling.Subscription{
		{ID: 20, UserID: 1, PlanID: 3, PriceID: 30, Status: "active", CurrentPeriodStartAt: now, CurrentPeriodEndAt: &maxEnd},
		{ID: 21, UserID: 1, PlanID: 2, PriceID: 20, Status: "active", CurrentPeriodStartAt: maxEnd, CurrentPeriodEndAt: &proEnd},
	}, plans, now)

	if len(views) != 2 {
		t.Fatalf("entitlement views len = %d, want 2", len(views))
	}
	if !views[0].IsCurrent || views[0].Plan.Code != "max" {
		t.Fatalf("views[0] = %+v, want current max", views[0])
	}
	if views[1].IsCurrent || views[1].Plan.Code != "pro" {
		t.Fatalf("views[1] = %+v, want queued pro", views[1])
	}
}
