package auth

import "testing"

func TestNormalizeAppearancePreferencesAllowsFontSize(t *testing.T) {
	for _, fontSize := range []string{"small", "standard", "medium", "large"} {
		payload := `{"theme":"system","preset":"default","chatFont":"default","chatFontWeight":"regular","fontSize":"` + fontSize + `"}`

		if _, err := normalizeAppearancePreferences(payload); err != nil {
			t.Fatalf("expected fontSize %q appearance preference to be valid, got %v", fontSize, err)
		}
	}
}

func TestNormalizeAppearancePreferencesDefaultsInvalidFontSize(t *testing.T) {
	payload := `{"fontSize":"huge"}`

	normalized, err := normalizeAppearancePreferences(payload)
	if err != nil {
		t.Fatalf("expected invalid fontSize appearance preference to fall back, got %v", err)
	}
	if normalized != `{"fontSize":"standard"}` {
		t.Fatalf("expected invalid fontSize to fall back to standard, got %s", normalized)
	}
}

func TestNormalizeAppearancePreferencesRejectsUnknownKey(t *testing.T) {
	payload := `{"fontSize":"standard","unknown":"value"}`

	if _, err := normalizeAppearancePreferences(payload); err == nil {
		t.Fatal("expected unknown appearance preference key to be rejected")
	}
}
