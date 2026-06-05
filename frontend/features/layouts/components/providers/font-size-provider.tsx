"use client";

import * as React from "react";

import {
  applyFontSizePreference,
  useFontSizePreference,
} from "@/features/settings/utils/font-size";

export function FontSizeProvider({
  children,
}: {
  children: React.ReactNode;
}) {
  const fontSize = useFontSizePreference();

  React.useEffect(() => {
    applyFontSizePreference(fontSize);
  }, [fontSize]);

  return children;
}
