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
const THEME_COLOR_VALUE_RE =
  /^(?:transparent|currentColor|inherit|initial|unset|none|var\(--[a-z0-9-]+\)|color-mix\(in\s+(?:srgb|oklab|oklch),\s*var\(--[a-z0-9-]+\)[^)]+\))$/i;
const NUMERIC_ALPHA_RE = /(?:0?\.\d+|0|1(?:\.0+)?|[1-9]\d?%|100%)/;
const LOW_ALPHA_RGB_COLOR_RE = new RegExp(
  String.raw`^rgba?\(\s*(?:\d{1,3}\s*,\s*){2}\d{1,3}(?:\s*,\s*(${NUMERIC_ALPHA_RE.source}))?\s*\)$`,
  "i",
);
const MODERN_RGB_ALPHA_COLOR_RE = new RegExp(
  String.raw`^rgb\(\s*\d{1,3}(?:\s+\d{1,3}){2}\s*/\s*(${NUMERIC_ALPHA_RE.source})\s*\)$`,
  "i",
);
const LOW_ALPHA_HSL_COLOR_RE = new RegExp(
  String.raw`^hsla?\(\s*[-\d.]+(?:deg|rad|turn)?\s*,\s*[\d.]+%\s*,\s*[\d.]+%(?:\s*,\s*(${NUMERIC_ALPHA_RE.source}))?\s*\)$`,
  "i",
);
const MODERN_HSL_ALPHA_COLOR_RE = new RegExp(
  String.raw`^hsl\(\s*[-\d.]+(?:deg|rad|turn)?\s+[\d.]+%\s+[\d.]+%\s*/\s*(${NUMERIC_ALPHA_RE.source})\s*\)$`,
  "i",
);
const COLOR_FUNCTION_OR_HEX_RE = /#[0-9a-f]{3,8}\b|(?:rgb|rgba|hsl|hsla|oklch|oklab|lab|lch)\(/i;
const HARD_CODED_COLOR_RE = /#[0-9a-f]{3,8}\b|(?:rgba?|hsla?|oklch|oklab|lab|lch)\([^)]*\)/gi;
const THEME_COLOR_TOKEN_RE = /\b(?:transparent|currentColor|inherit|initial|unset|none)\b|var\(--[a-z0-9-]+\)/i;
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
const THEME_ONLY_COLOR_PROPERTY_NAMES = new Set(["color"]);
const LOW_ALPHA_COLOR_PROPERTY_NAMES = new Set([
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
  "borderRight",
  "borderTop",
  "boxShadow",
]);
const MAX_DECORATIVE_ALPHA = 0.18;
const THEME_FALLBACK_BLOCK_BACKGROUND = "color-mix(in oklch, var(--muted) 72%, transparent)";
const THEME_FALLBACK_BLOCK_BORDER = "color-mix(in oklch, var(--border) 72%, transparent)";

function isSafeHTMLStyleValue(value: string | number): boolean {
  if (typeof value === "number") {
    return Number.isFinite(value);
  }
  const normalizedValue = value.trim();
  return Boolean(normalizedValue) && normalizedValue.length <= 120 && !UNSAFE_STYLE_VALUE_RE.test(normalizedValue);
}

function parseAlpha(value: string | undefined): number {
  if (!value) {
    return 1;
  }
  const trimmedValue = value.trim();
  if (trimmedValue.endsWith("%")) {
    return Number.parseFloat(trimmedValue) / 100;
  }
  return Number.parseFloat(trimmedValue);
}

function isLowAlphaColor(value: string): boolean {
  const normalizedValue = value.trim();
  const match =
    LOW_ALPHA_RGB_COLOR_RE.exec(normalizedValue) ??
    MODERN_RGB_ALPHA_COLOR_RE.exec(normalizedValue) ??
    LOW_ALPHA_HSL_COLOR_RE.exec(normalizedValue) ??
    MODERN_HSL_ALPHA_COLOR_RE.exec(normalizedValue);
  if (!match) {
    return false;
  }

  return parseAlpha(match[1]) <= MAX_DECORATIVE_ALPHA;
}

function isSafeColorStyleValue(property: string, value: string | number): boolean {
  if (typeof value === "number") {
    return !THEME_ONLY_COLOR_PROPERTY_NAMES.has(property);
  }

  const normalizedValue = value.trim();
  if (THEME_COLOR_VALUE_RE.test(normalizedValue)) {
    return true;
  }

  if (THEME_ONLY_COLOR_PROPERTY_NAMES.has(property)) {
    return false;
  }

  const hardCodedColors = normalizedValue.match(HARD_CODED_COLOR_RE) ?? [];
  const hasHardCodedColors = hardCodedColors.length > 0;
  const hardCodedColorsAreDecorative = hasHardCodedColors && hardCodedColors.every(isLowAlphaColor);

  if (THEME_COLOR_TOKEN_RE.test(normalizedValue)) {
    return !hasHardCodedColors || hardCodedColorsAreDecorative;
  }

  if (!LOW_ALPHA_COLOR_PROPERTY_NAMES.has(property)) {
    return !COLOR_FUNCTION_OR_HEX_RE.test(normalizedValue);
  }

  return isLowAlphaColor(normalizedValue) || hardCodedColorsAreDecorative;
}

function getThemeFallbackStyleValue(property: string, value: string | number): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  if (!COLOR_FUNCTION_OR_HEX_RE.test(value) || isSafeColorStyleValue(property, value)) {
    return undefined;
  }

  if (property === "background" || property === "backgroundColor") {
    return THEME_FALLBACK_BLOCK_BACKGROUND;
  }

  if (property === "borderColor") {
    return THEME_FALLBACK_BLOCK_BORDER;
  }

  return undefined;
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
    if (COLOR_PROPERTY_NAMES.has(property) && !isSafeColorStyleValue(property, value)) {
      const fallbackValue = getThemeFallbackStyleValue(property, value);
      if (fallbackValue) {
        safeStyle[property] = fallbackValue;
      }
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
