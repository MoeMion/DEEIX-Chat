import assert from "node:assert/strict";
import test from "node:test";

import type { AdminIdentityProviderDTO } from "@/features/admin/api/auth";
import type { IdentityProviderDTO } from "@/shared/api/auth.types";
import type {
  IdentityProviderResponse,
  PublicIdentityProviderResponse,
} from "@deeix/api-contract";

type Equal<Left, Right> =
  (<Value>() => Value extends Left ? 1 : 2) extends
  (<Value>() => Value extends Right ? 1 : 2)
    ? true
    : false;

type Expect<Value extends true> = Value;

const identityProviderContractChecks: [
  Expect<Equal<IdentityProviderDTO, PublicIdentityProviderResponse>>,
  Expect<Equal<AdminIdentityProviderDTO, IdentityProviderResponse>>,
] = [true, true];

test("keeps public and admin identity provider contracts separate", () => {
  assert.deepEqual(identityProviderContractChecks, [true, true]);
});
