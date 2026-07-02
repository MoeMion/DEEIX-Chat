package billing

import "testing"

func TestApplyRateMultiplierWithGroupDiscount(t *testing.T) {
	discounted := billingRateMultiplier{Numerator: 80, Denominator: 100}
	if got := applyRateMultiplier(1000, discounted); got != 800 {
		t.Fatalf("expected 80%% multiplier to yield 800, got %d", got)
	}

	composed := billingRateMultiplier{Numerator: 6 * 80, Denominator: 1 * 100}
	if got := applyRateMultiplier(1000, composed); got != 4800 {
		t.Fatalf("expected composed 4.8x multiplier to yield 4800, got %d", got)
	}
}

func TestApplyGroupRateMultiplierNoResolverKeepsBase(t *testing.T) {
	s := &Service{}
	base := billingRateMultiplier{Numerator: 2, Denominator: 1}
	got := s.applyGroupRateMultiplier(nil, 1, nil, base)
	if got != base {
		t.Fatalf("expected base multiplier unchanged without resolver, got %+v", got)
	}
}
