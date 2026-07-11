export type MCPHeaderTemplatePreflightError =
  | "invalidJson"
  | "objectRequired"
  | "stringValuesRequired";

export function validateHeaderTemplateJSON(
  raw: string,
): MCPHeaderTemplatePreflightError | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return "invalidJson";
  }
  if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") {
    return "objectRequired";
  }
  if (Object.values(parsed).some((value) => typeof value !== "string")) {
    return "stringValuesRequired";
  }
  return null;
}
