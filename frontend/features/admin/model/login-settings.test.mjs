import assert from "node:assert/strict";
import test from "node:test";

import * as loginSettings from "./login-settings.ts";

const identityProviderFixture = {
  publicID: "provider_1",
  type: "oauth2",
  name: "Acme OAuth",
  slug: "acme-oauth",
  logoURL: "",
  loginEnabled: true,
  registrationEnabled: true,
  clientID: "client-id",
  issuerURL: "",
  discoveryURL: "",
  authURL: "https://idp.example/authorize",
  tokenURL: "https://idp.example/token",
  userinfoURL: "https://idp.example/userinfo",
  jwksURL: "",
  scopes: "profile email",
  defaultRole: "user",
  subjectField: "id",
  emailField: "email",
  emailVerifiedField: "email_verified",
  nameField: "name",
  avatarField: "picture",
  createdAt: "2026-07-14T00:00:00Z",
  updatedAt: "2026-07-14T00:00:00Z",
};

test("does not build an identity provider payload when the slug cannot be derived", () => {
  const buildIdentityProviderPayload = loginSettings.buildIdentityProviderPayload;

  assert.equal(
    typeof buildIdentityProviderPayload,
    "function",
    "expected the provider form to validate and build its save payload",
  );

  const payload = buildIdentityProviderPayload({
    ...loginSettings.DEFAULT_PROVIDER_FORM,
    name: "企业微信",
    slug: "",
  });

  assert.equal(payload, undefined);
});

test("limits a provider slug derived from the name to the API maximum", () => {
  const payload = loginSettings.buildIdentityProviderPayload({
    ...loginSettings.DEFAULT_PROVIDER_FORM,
    name: "a".repeat(80),
    slug: "",
  });

  assert.equal(payload?.slug.length, 64);
});

test("builds a normalized payload for a non-ASCII name with an explicit slug", () => {
  const form = {
    ...loginSettings.DEFAULT_PROVIDER_FORM,
    name: "企业微信",
    slug: "WeChat Work",
  };
  const payload = loginSettings.buildIdentityProviderPayload(form);

  assert.equal(payload?.slug, "wechat-work");
  assert.equal(payload?.slug, loginSettings.resolveProviderSlug(form));
});

test("derives the provider slug from an ASCII name", () => {
  const payload = loginSettings.buildIdentityProviderPayload({
    ...loginSettings.DEFAULT_PROVIDER_FORM,
    name: "Acme OAuth",
    slug: "",
  });

  assert.equal(payload?.slug, "acme-oauth");
});

test("disables provider registration when provider login is disabled", () => {
  const payload = loginSettings.buildIdentityProviderPayload({
    ...loginSettings.DEFAULT_PROVIDER_FORM,
    name: "Acme OAuth",
    loginEnabled: false,
    registrationEnabled: true,
  });

  assert.equal(payload?.registrationEnabled, false);
});

test("keeps TLS certificate verification enabled by default", () => {
  const payload = loginSettings.buildIdentityProviderPayload({
    ...loginSettings.DEFAULT_PROVIDER_FORM,
    type: "oauth2",
    name: "Acme OAuth",
    slug: "acme-oauth",
  });

  assert.equal(payload?.tlsInsecureSkipVerify, false);
});

test("preserves the TLS certificate verification override when editing", () => {
  const form = loginSettings.providerToForm({
    ...identityProviderFixture,
    tlsInsecureSkipVerify: true,
  });

  assert.equal(form.tlsInsecureSkipVerify, true);
  assert.equal(form.loginEnabled, true);
  assert.equal(form.registrationEnabled, true);
});

test("uses secure TLS verification when an identity provider response omits the override", () => {
  const form = loginSettings.providerToForm(identityProviderFixture);

  assert.equal(form.tlsInsecureSkipVerify, false);
});

test("shows the password login entry by default", () => {
  const settings = loginSettings.applyLoginDefaults({});

  assert.equal(settings["auth.password_login_entry_visible"], "true");
});

test("restores the password login entry when third-party login is disabled", () => {
  const settings = loginSettings.applyLoginDefaults({
    "auth.third_party_login_enabled": "false",
    "auth.password_login_entry_visible": "false",
  });

  assert.equal(settings["auth.password_login_entry_visible"], "true");
});

test("preserves a hidden password login entry while third-party login is enabled", () => {
  const settings = loginSettings.applyLoginDefaults({
    "auth.third_party_login_enabled": "true",
    "auth.password_login_entry_visible": "false",
  });

  assert.equal(settings["auth.password_login_entry_visible"], "false");
});

test("allows password login entry configuration only with third-party and password login", () => {
  assert.equal(loginSettings.canConfigurePasswordLoginEntryVisibility({
    "auth.third_party_login_enabled": "true",
    "auth.username_login_enabled": "true",
    "auth.email_login_enabled": "false",
  }), true);
  assert.equal(loginSettings.canConfigurePasswordLoginEntryVisibility({
    "auth.third_party_login_enabled": "false",
    "auth.username_login_enabled": "true",
    "auth.email_login_enabled": "true",
  }), false);
  assert.equal(loginSettings.canConfigurePasswordLoginEntryVisibility({
    "auth.third_party_login_enabled": "true",
    "auth.username_login_enabled": "false",
    "auth.email_login_enabled": "false",
  }), false);
});
