import { authedRequest } from "@/shared/api/authed-client";
import { pathParam } from "@/shared/api/http-client";

export type PermissionGroup = {
  id: number;
  name: string;
  description: string;
  isDefault: boolean;
  rateMultiplierPercent: number;
  modelCount: number;
  manualModelCount: number;
  ruleModelCount: number;
  userCount: number;
  manualUserCount: number;
  subscriptionUserCount: number;
  createdAt: string;
  updatedAt: string;
};

export type PermissionGroupModelRuleType = "all" | "vendor" | "protocol" | "upstream";

export type PermissionGroupModelRule = {
  type: PermissionGroupModelRuleType;
  value: string;
};

export type CreatePermissionGroupRequest = {
  name: string;
  description: string;
  rateMultiplierPercent: number;
};

export type UpdatePermissionGroupRequest = {
  name: string;
  description: string;
  rateMultiplierPercent: number;
};

type PermissionGroupListData = {
  results: PermissionGroup[];
};

type PermissionGroupData = {
  group: PermissionGroup;
};

type GroupModelsData = {
  modelIDs: number[];
  rules?: PermissionGroupModelRule[];
};

type GroupUsersData = {
  userIDs: number[];
};

type ModelPermissionGroupsData = {
  manualGroupIDs: number[];
  matchedGroupIDs: number[];
  effectiveGroupIDs: number[];
  unassigned: boolean;
};

export type DeletePermissionGroupResult = {
  deleted: boolean;
  summary: {
    manualModelCount: number;
    ruleCount: number;
    manualUserCount: number;
    planCount: number;
  };
};

export async function listPermissionGroups(accessToken: string): Promise<PermissionGroup[]> {
  const data = await authedRequest<PermissionGroupListData>(
    "/api/v1/admin/permission-groups",
    { accessToken },
    true,
  );
  return data.results ?? [];
}

export async function createPermissionGroup(
  accessToken: string,
  req: CreatePermissionGroupRequest,
): Promise<PermissionGroup> {
  const data = await authedRequest<PermissionGroupData>(
    "/api/v1/admin/permission-groups",
    { method: "POST", accessToken, body: req },
    true,
  );
  return data.group;
}

export async function updatePermissionGroup(
  accessToken: string,
  id: number,
  req: UpdatePermissionGroupRequest,
): Promise<PermissionGroup> {
  const data = await authedRequest<PermissionGroupData>(
    `/api/v1/admin/permission-groups/${pathParam(id)}`,
    { method: "PATCH", accessToken, body: req },
    true,
  );
  return data.group;
}

export async function deletePermissionGroup(accessToken: string, id: number): Promise<DeletePermissionGroupResult> {
  return authedRequest<DeletePermissionGroupResult>(
    `/api/v1/admin/permission-groups/${pathParam(id)}`,
    { method: "DELETE", accessToken },
    true,
  );
}

export async function listGroupModels(
  accessToken: string,
  groupID: number,
): Promise<{ modelIDs: number[]; rules: PermissionGroupModelRule[] }> {
  const data = await authedRequest<GroupModelsData>(
    `/api/v1/admin/permission-groups/${pathParam(groupID)}/models`,
    { accessToken },
    true,
  );
  return {
    modelIDs: data.modelIDs ?? [],
    rules: data.rules ?? [],
  };
}

export async function setGroupModels(
  accessToken: string,
  groupID: number,
  modelIDs: number[],
  rules: PermissionGroupModelRule[] = [],
): Promise<void> {
  await authedRequest<GroupModelsData>(
    `/api/v1/admin/permission-groups/${pathParam(groupID)}/models`,
    { method: "PUT", accessToken, body: { modelIDs, rules } },
    true,
  );
}

export async function listModelPermissionGroups(
  accessToken: string,
  modelID: number,
): Promise<ModelPermissionGroupsData> {
  const data = await authedRequest<ModelPermissionGroupsData>(
    `/api/v1/admin/models/${pathParam(modelID)}/permission-groups`,
    { accessToken },
    true,
  );
  return {
    manualGroupIDs: data.manualGroupIDs ?? [],
    matchedGroupIDs: data.matchedGroupIDs ?? [],
    effectiveGroupIDs: data.effectiveGroupIDs ?? [],
    unassigned: data.unassigned ?? false,
  };
}

export async function setModelPermissionGroups(
  accessToken: string,
  modelID: number,
  groupIDs: number[],
): Promise<ModelPermissionGroupsData> {
  const data = await authedRequest<ModelPermissionGroupsData>(
    `/api/v1/admin/models/${pathParam(modelID)}/permission-groups`,
    { method: "PUT", accessToken, body: { groupIDs } },
    true,
  );
  return {
    manualGroupIDs: data.manualGroupIDs ?? [],
    matchedGroupIDs: data.matchedGroupIDs ?? [],
    effectiveGroupIDs: data.effectiveGroupIDs ?? [],
    unassigned: data.unassigned ?? false,
  };
}

export async function listGroupUsers(accessToken: string, groupID: number): Promise<number[]> {
  const data = await authedRequest<GroupUsersData>(
    `/api/v1/admin/permission-groups/${pathParam(groupID)}/users`,
    { accessToken },
    true,
  );
  return data.userIDs ?? [];
}

export async function setGroupUsers(
  accessToken: string,
  groupID: number,
  userIDs: number[],
): Promise<void> {
  await authedRequest<GroupUsersData>(
    `/api/v1/admin/permission-groups/${pathParam(groupID)}/users`,
    { method: "PUT", accessToken, body: { userIDs } },
    true,
  );
}
