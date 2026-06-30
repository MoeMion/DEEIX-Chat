import type { CSSProperties } from "react";

const SAFE_HTML_STYLE_PROPERTIES: ReadonlySet<string> = new Set([
  "alignContent",
  "alignItems",
  "alignSelf",
  "background",
  "backgroundColor",
  "border",
  "borderBlock",
  "borderBlockEnd",
  "borderBlockStart",
  "borderBottom",
  "borderColor",
  "borderInline",
  "borderInlineEnd",
  "borderInlineStart",
  "borderLeft",
  "borderBottomWidth",
  "borderRadius",
  "borderRight",
  "borderStyle",
  "borderTop",
  "borderWidth",
  "boxShadow",
  "boxSizing",
  "color",
  "columnGap",
  "display",
  "flex",
  "flexBasis",
  "flexDirection",
  "flexGrow",
  "flexShrink",
  "flexWrap",
  "fontSize",
  "fontStyle",
  "fontWeight",
  "gap",
  "gridAutoColumns",
  "gridAutoFlow",
  "gridAutoRows",
  "gridColumn",
  "gridColumnEnd",
  "gridColumnStart",
  "gridRow",
  "gridRowEnd",
  "gridRowStart",
  "gridTemplateColumns",
  "gridTemplateRows",
  "height",
  "justifyItems",
  "justifyContent",
  "justifySelf",
  "lineHeight",
  "margin",
  "marginBlock",
  "marginBlockEnd",
  "marginBlockStart",
  "marginBottom",
  "marginInline",
  "marginInlineEnd",
  "marginInlineStart",
  "marginLeft",
  "marginRight",
  "marginTop",
  "maxHeight",
  "maxWidth",
  "minHeight",
  "minWidth",
  "opacity",
  "order",
  "overflow",
  "overflowX",
  "overflowY",
  "padding",
  "paddingBlock",
  "paddingBlockEnd",
  "paddingBlockStart",
  "paddingBottom",
  "paddingInline",
  "paddingInlineEnd",
  "paddingInlineStart",
  "paddingLeft",
  "paddingRight",
  "paddingTop",
  "placeContent",
  "placeItems",
  "placeSelf",
  "position",
  "rowGap",
  "textAlign",
  "top",
  "right",
  "bottom",
  "left",
  "transform",
  "verticalAlign",
  "whiteSpace",
  "width",
  "zIndex",
]);

const KATEX_SAFE_HTML_STYLE_PROPERTIES: ReadonlySet<string> = new Set([
  ...SAFE_HTML_STYLE_PROPERTIES,
  "top",
]);
const UNSAFE_STYLE_VALUE_RE = /(?:url\s*\(|expression\s*\(|javascript:|@import|[<>{}])/i;
const COLOR_PROPERTY_NAMES = new Set([
  "border",
  "background",
  "backgroundColor",
  "borderColor",
  "borderBlock",
  "borderBlockEnd",
  "borderBlockStart",
  "borderBottom",
  "borderInline",
  "borderInlineEnd",
  "borderInlineStart",
  "borderLeft",
  "borderRight",
  "borderTop",
  "boxShadow",
  "color",
]);
const PURE_NEUTRAL_HEX_RE = /#(?:000|000000|000000ff|fff|ffffff|ffffffff)\b/gi;
const PURE_NEUTRAL_KEYWORD_RE = /\b(?:black|white)\b/gi;
const RGB_COLOR_FUNCTION_RE = /rgba?\(\s*([^)]+)\)/gi;
const HSL_COLOR_FUNCTION_RE = /hsla?\(\s*([^)]+)\)/gi;

function resolvePureNeutralReplacement(property: string): string {
  if (property === "color") {
    return "var(--foreground)";
  }
  if (property === "background" || property === "backgroundColor") {
    return "var(--background)";
  }
  if (property === "boxShadow") {
    return "color-mix(in oklch, var(--foreground) 16%, transparent)";
  }
  return "var(--border)";
}

function isSafeHTMLStyleValue(value: string | number): boolean {
  if (typeof value === "number") {
    return Number.isFinite(value);
  }
  const normalizedValue = value.trim();
  return Boolean(normalizedValue) && normalizedValue.length <= 120 && !UNSAFE_STYLE_VALUE_RE.test(normalizedValue);
}

function isOpaqueAlpha(value: string | undefined): boolean {
  if (!value) {
    return true;
  }
  return /^(?:1(?:\.0+)?|100%)$/u.test(value.trim());
}

function splitColorComponents(value: string): { channels: string[]; alpha?: string } {
  const [channelPart, slashAlpha] = value.split(/\s+\/\s+/u, 2);
  if (channelPart.includes(",")) {
    const parts = channelPart.split(",").map((item) => item.trim());
    return {
      channels: parts.slice(0, 3),
      alpha: parts[3] ?? slashAlpha,
    };
  }

  return {
    channels: channelPart.trim().split(/\s+/u),
    alpha: slashAlpha,
  };
}

function isPureNeutralRGB(value: string): boolean {
  const { channels, alpha } = splitColorComponents(value);
  if (channels.length !== 3 || !isOpaqueAlpha(alpha)) {
    return false;
  }
  return channels.every((item) => item === "0") || channels.every((item) => item === "255");
}

function isPureNeutralHSL(value: string): boolean {
  const { channels, alpha } = splitColorComponents(value);
  if (channels.length !== 3 || !isOpaqueAlpha(alpha)) {
    return false;
  }
  return channels[2] === "0%" || channels[2] === "100%";
}

function normalizePureNeutralColorStyleValue(property: string, value: string): string {
  const replacement = resolvePureNeutralReplacement(property);

  return value
    .replace(PURE_NEUTRAL_HEX_RE, replacement)
    .replace(PURE_NEUTRAL_KEYWORD_RE, replacement)
    .replace(RGB_COLOR_FUNCTION_RE, (match, content: string) => (isPureNeutralRGB(content) ? replacement : match))
    .replace(HSL_COLOR_FUNCTION_RE, (match, content: string) => (isPureNeutralHSL(content) ? replacement : match));
}

function sanitizeStyle(
  style: CSSProperties | undefined,
  safeProperties: ReadonlySet<string>,
): CSSProperties | undefined {
  if (!style) {
    return undefined;
  }

  const safeStyle: Record<string, string | number> = {};
  for (const [property, value] of Object.entries(style)) {
    if (!safeProperties.has(property)) {
      continue;
    }
    if (typeof value !== "string" && typeof value !== "number") {
      continue;
    }
    if (!isSafeHTMLStyleValue(value)) {
      continue;
    }
    if (COLOR_PROPERTY_NAMES.has(property)) {
      if (typeof value === "number") {
        continue;
      }
      safeStyle[property] = normalizePureNeutralColorStyleValue(property, value);
      continue;
    }
    safeStyle[property] = value;
  }

  return Object.keys(safeStyle).length > 0 ? safeStyle : undefined;
}

export function sanitizeHTMLStyle(style: CSSProperties | undefined): CSSProperties | undefined {
  return sanitizeStyle(style, SAFE_HTML_STYLE_PROPERTIES);
}

export function sanitizeKatexHTMLStyle(style: CSSProperties | undefined): CSSProperties | undefined {
  return sanitizeStyle(style, KATEX_SAFE_HTML_STYLE_PROPERTIES);
}
