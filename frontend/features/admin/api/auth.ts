import { authedRequest } from "@/shared/api/authed-client";
import { pathParam } from "@/shared/api/http-client";
import type {
	IdentityProviderDeleteResponse,
	IdentityProviderListResponse,
	IdentityProviderReorderResponse,
	IdentityProviderResponse,
	UpsertIdentityProviderRequest,
} from "@deeix/api-contract";

export type AdminIdentityProviderDTO = IdentityProviderResponse;

export type IdentityProviderPayload = Omit<
  UpsertIdentityProviderRequest,
  "loginEnabled" | "registrationEnabled" | "tlsInsecureSkipVerify"
> & {
  loginEnabled: boolean;
  registrationEnabled: boolean;
  tlsInsecureSkipVerify: boolean;
};

export async function listAdminIdentityProviders(accessToken: string): Promise<{ total: number; results: AdminIdentityProviderDTO[] }> {
  const data = await authedRequest<IdentityProviderListResponse>(
    "/api/v1/admin/auth/providers",
    { accessToken },
    true,
  );
  return data;
}

export async function createAdminIdentityProvider(accessToken: string, payload: IdentityProviderPayload): Promise<AdminIdentityProviderDTO> {
  const data = await authedRequest<IdentityProviderResponse>(
    "/api/v1/admin/auth/providers",
    { method: "POST", accessToken, body: payload },
    true,
  );
  return data;
}

export async function updateAdminIdentityProvider(accessToken: string, providerID: string, payload: IdentityProviderPayload): Promise<AdminIdentityProviderDTO> {
  const data = await authedRequest<IdentityProviderResponse>(
    `/api/v1/admin/auth/providers/${pathParam(providerID)}`,
    { method: "PATCH", accessToken, body: payload },
    true,
  );
  return data;
}

export async function reorderAdminIdentityProviders(accessToken: string, providerIDs: string[]): Promise<IdentityProviderReorderResponse> {
  return authedRequest<IdentityProviderReorderResponse>(
    "/api/v1/admin/auth/provider-order",
    { method: "PATCH", accessToken, body: { providerIDs } },
    true,
  );
}

export async function deleteAdminIdentityProvider(accessToken: string, providerID: string, options: { force?: boolean } = {}): Promise<IdentityProviderDeleteResponse> {
  const query = options.force ? "?force=true" : "";
  return authedRequest<IdentityProviderDeleteResponse>(
    `/api/v1/admin/auth/providers/${pathParam(providerID)}${query}`,
    { method: "DELETE", accessToken },
    true,
  );
}
