"use client";

import * as React from "react";

import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

type ChatEmptyStateProps = {
  greetingTitle: string;
  badgeLabel?: string;
  badgeTooltip?: string;
  children?: React.ReactNode;
};

export function ChatEmptyState({ greetingTitle, badgeLabel, badgeTooltip, children }: ChatEmptyStateProps) {
  const badge = badgeLabel ? (
    <Badge
      variant="outline"
      className="absolute left-full top-0 ml-1.5 cursor-default border-border/70 bg-background/60 px-1.5 py-0 text-[9px] font-medium text-muted-foreground"
    >
      {badgeLabel}
    </Badge>
  ) : null;

  return (
    <div className="flex h-full min-h-0 flex-col items-center justify-center px-3 py-12 text-center md:px-6 md:py-20">
      <div className="relative inline-flex max-w-[calc(100%-4.5rem)] justify-center">
        <h1 className="min-w-0 text-balance text-[22px] font-medium leading-[1.12] tracking-[-0.005em] text-foreground [font-family:var(--font-economist)] md:text-[32px]">
          {greetingTitle}
        </h1>
        {badge && badgeTooltip ? (
          <Tooltip>
            <TooltipTrigger asChild>{badge}</TooltipTrigger>
            <TooltipContent side="top" className="max-w-72 text-left leading-5">
              {badgeTooltip}
            </TooltipContent>
          </Tooltip>
        ) : badge}
      </div>
      {children ? <div className="mt-7 w-full max-w-[800px] md:mt-8">{children}</div> : null}
    </div>
  );
}
