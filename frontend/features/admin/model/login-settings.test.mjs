import assert from "node:assert/strict";
import test from "node:test";

import * as loginSettings from "./login-settings.ts";

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
