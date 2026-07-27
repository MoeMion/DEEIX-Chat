package repository

import "testing"

func TestUpdateIdentityProviderInputIsZeroIncludesTLSInsecureSkipVerify(t *testing.T) {
	if !(UpdateIdentityProviderInput{}).IsZero() {
		t.Fatal("zero input must be zero")
	}
	for _, value := range []bool{false, true} {
		value := value
		if (UpdateIdentityProviderInput{TLSInsecureSkipVerify: &value}).IsZero() {
			t.Fatalf("TLS policy %v was ignored by IsZero", value)
		}
	}
}
