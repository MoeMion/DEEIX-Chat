import assert from "node:assert/strict";
import test from "node:test";

import type { IdentityProviderDTO, LoginOptionsData } from "@/shared/api/auth.types";
import { DEFAULT_LOGIN_OPTIONS, shouldShowPasswordLogin } from "./login-page";

const loginProvider: IdentityProviderDTO = {
  publicID: "provider_1",
  type: "oidc",
  name: "Acme SSO",
  slug: "acme",
  logoURL: "",
  loginEnabled: true,
  registrationEnabled: true,
  scopes: "openid profile email",
  defaultRole: "user",
  subjectField: "sub",
  emailField: "email",
  emailVerifiedField: "email_verified",
  nameField: "name",
  avatarField: "picture",
  createdAt: "2026-08-06T00:00:00Z",
  updatedAt: "2026-08-06T00:00:00Z",
};

const tests: Array<{
  name: string;
  options: LoginOptionsData;
  requested?: boolean;
  expected: boolean;
}> = [
  {
    name: "shows the password entry by default",
    options: { ...DEFAULT_LOGIN_OPTIONS, providers: [loginProvider] },
    expected: true,
  },
  {
    name: "hides the password entry when a provider is available",
    options: { ...DEFAULT_LOGIN_OPTIONS, passwordLoginEntryVisible: false, providers: [loginProvider] },
    expected: false,
  },
  {
    name: "reveals the hidden password entry on request",
    options: { ...DEFAULT_LOGIN_OPTIONS, passwordLoginEntryVisible: false, providers: [loginProvider] },
    requested: true,
    expected: true,
  },
  {
    name: "restores the password entry without a login provider",
    options: { ...DEFAULT_LOGIN_OPTIONS, passwordLoginEntryVisible: false, providers: [] },
    expected: true,
  },
  {
    name: "ignores the reveal request when password login is disabled",
    options: {
      ...DEFAULT_LOGIN_OPTIONS,
      usernameEnabled: false,
      emailEnabled: false,
      passwordLoginEntryVisible: false,
      providers: [loginProvider],
    },
    requested: true,
    expected: false,
  },
  {
    name: "restores the password entry for a registration-only provider",
    options: {
      ...DEFAULT_LOGIN_OPTIONS,
      passwordLoginEntryVisible: false,
      providers: [{ ...loginProvider, loginEnabled: false, registrationEnabled: true }],
    },
    expected: true,
  },
];

for (const testCase of tests) {
  test(testCase.name, () => {
    assert.equal(
      shouldShowPasswordLogin(testCase.options, testCase.requested),
      testCase.expected,
    );
  });
}
