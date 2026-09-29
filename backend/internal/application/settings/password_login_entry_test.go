package settings

import (
	"errors"
	"strconv"
	"testing"

	domainsettings "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/settings"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/config"
)

func TestPasswordLoginEntrySeed(t *testing.T) {
	for _, existing := range []string{"", "false"} {
		t.Run("existing="+existing, func(t *testing.T) {
			repo := newSettingsSeedRepo()
			want := "true"
			if existing != "" {
				want = existing
				repo.items["auth:password_login_entry_visible"] = domainsettings.SystemSetting{
					Namespace: "auth", Key: "password_login_entry_visible", Value: existing, ValueType: "bool",
				}
			}
			if err := NewService(repo, "").Seed(t.Context()); err != nil {
				t.Fatal(err)
			}
			item, ok := repo.items["auth:password_login_entry_visible"]
			if !ok || item.Value != want || item.ValueType != "bool" {
				t.Fatalf("seeded setting = %+v, present = %v, want bool %q", item, ok, want)
			}
		})
	}
}

func TestPasswordLoginEntrySaveAndApply(t *testing.T) {
	repo := newSettingsSeedRepo(
		domainsettings.SystemSetting{Namespace: "auth", Key: "username_login_enabled", Value: "true"},
		domainsettings.SystemSetting{Namespace: "auth", Key: "email_login_enabled", Value: "true"},
		domainsettings.SystemSetting{Namespace: "auth", Key: "third_party_login_enabled", Value: "true"},
	)
	service := NewService(repo, "")
	runtime := config.NewRuntime(config.Config{PasswordLoginEntryVisible: true})
	loader := NewRuntimeSettings(repo, nil, "")
	for _, visible := range []bool{false, true} {
		value := strconv.FormatBool(visible)
		grouped, err := service.BatchUpdate(t.Context(), []PatchItem{
			{Namespace: "auth", Key: "password_login_entry_visible", Value: value},
		})
		if err != nil {
			t.Fatalf("save %s: %v", value, err)
		}
		if got := repo.items["auth:password_login_entry_visible"].Value; got != value {
			t.Fatalf("stored value = %q, want %q", got, value)
		}
		found := false
		for _, item := range grouped["auth"] {
			if item.Key == "password_login_entry_visible" && item.Value == value {
				found = true
			}
		}
		if !found {
			t.Fatalf("saved setting missing from response for %s", value)
		}
		if err := loader.ApplyTo(t.Context(), runtime); err != nil {
			t.Fatal(err)
		}
		if got := runtime.Snapshot().PasswordLoginEntryVisible; got != visible {
			t.Fatalf("runtime visibility = %v, want %v", got, visible)
		}
	}
}

func TestPasswordLoginEntryInvalidValueRule(t *testing.T) {
	err := validatePatchItem(PatchItem{Namespace: "auth", Key: "password_login_entry_visible", Value: "hidden"})
	var validationErr *SettingValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("expected validation error, got %v", err)
	}
	if validationErr.Code() != settingCodeInvalidValue || validationErr.Details().Rule != "bool" {
		t.Fatalf("expected boolean value rejection, got %s: %+v", validationErr.Code(), validationErr.Details())
	}
}
