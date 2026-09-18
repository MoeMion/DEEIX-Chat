import assert from "node:assert/strict";
import { createHash, webcrypto } from "node:crypto";
import test from "node:test";

import type { IdentityProviderDTO, LoginOptionsData } from "@/shared/api/auth.types";
import { createProviderClientState, createProviderPKCE, DEFAULT_LOGIN_OPTIONS, shouldShowPasswordLogin } from "./login-page";

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

for (const withSubtle of [true, false]) {
  test(`generates S256 PKCE ${withSubtle ? "with Web Crypto" : "on HTTP without SubtleCrypto"}`, async (t) => {
    const originalWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
    t.after(() => {
      if (originalWindow) Object.defineProperty(globalThis, "window", originalWindow);
      else Reflect.deleteProperty(globalThis, "window");
    });
    let randomCalls = 0;
    let digestCalls = 0;
    Object.defineProperty(globalThis, "window", {
      configurable: true,
      value: {
        crypto: {
          getRandomValues(bytes: Uint8Array<ArrayBuffer>) {
            randomCalls++;
            return webcrypto.getRandomValues(bytes);
          },
          ...(withSubtle ? {
            subtle: {
              async digest(algorithm: string, input: Uint8Array<ArrayBuffer>) {
                digestCalls++;
                assert.equal(algorithm, "SHA-256");
                return webcrypto.subtle.digest(algorithm, input);
              },
            },
          } : {}),
        },
      },
    });
    const first = await createProviderPKCE();
    const second = await createProviderPKCE();
    for (const result of [first, second]) {
      assert.match(result.verifier, /^[A-Za-z0-9_-]{64}$/);
      assert.match(result.challenge, /^[A-Za-z0-9_-]{43}$/);
      assert.equal(result.challenge, createHash("sha256").update(result.verifier).digest("base64url"));
    }
    assert.notEqual(first.verifier, second.verifier);
    assert.equal(randomCalls, 2);
    assert.equal(digestCalls, withSubtle ? 2 : 0);
    assert.match(createProviderClientState(), /^[A-Za-z0-9_-]{43}$/);
    assert.equal(randomCalls, 3);
  });
}

test("refuses PKCE when secure randomness is unavailable", async (t) => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  t.after(() => {
    if (originalWindow) Object.defineProperty(globalThis, "window", originalWindow);
    else Reflect.deleteProperty(globalThis, "window");
  });
  Object.defineProperty(globalThis, "window", { configurable: true, value: { crypto: {} } });
  await assert.rejects(createProviderPKCE(), TypeError);
});
