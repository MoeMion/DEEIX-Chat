"use client";

import * as React from "react";
import { Download, Share2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuItemIcon,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";

type ConversationShareExportActionsProps = {
  shareLabel: string;
  exportLabel: string;
  onShare?: () => void;
  onExport?: () => void | Promise<void>;
};

type ConversationShareExportMenuItemsProps = ConversationShareExportActionsProps & {
  onCloseMenu?: () => void;
};

export function ConversationShareExportMenuItems({
  shareLabel,
  exportLabel,
  onShare,
  onExport,
  onCloseMenu,
}: ConversationShareExportMenuItemsProps) {
  return (
    <>
      <DropdownMenuItem
        disabled={!onShare}
        onSelect={(event) => {
          event.preventDefault();
          if (!onShare) {
            return;
          }
          onCloseMenu?.();
          onShare();
        }}
      >
        <DropdownMenuItemIcon icon={Share2} />
        {shareLabel}
      </DropdownMenuItem>
      <DropdownMenuItem
        disabled={!onExport}
        onSelect={(event) => {
          event.preventDefault();
          if (!onExport) {
            return;
          }
          onCloseMenu?.();
          void onExport();
        }}
      >
        <DropdownMenuItemIcon icon={Download} />
        {exportLabel}
      </DropdownMenuItem>
    </>
  );
}

type ConversationShareExportIconDropdownProps = {
  label: string;
  active?: boolean;
  className?: string;
} & ConversationShareExportActionsProps;

export function ConversationShareExportIconDropdown({
  label,
  shareLabel,
  exportLabel,
  active = false,
  className,
  onShare,
  onExport,
}: ConversationShareExportIconDropdownProps) {
  const [open, setOpen] = React.useState(false);

  return (
    <DropdownMenu modal={false} open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className={cn(
            "size-8 shrink-0 rounded-lg text-muted-foreground shadow-none hover:bg-muted hover:text-foreground",
            active && "text-foreground",
            className,
          )}
          disabled={!onShare && !onExport}
          aria-label={label}
          title={label}
        >
          <Share2 className="size-4 stroke-[1.8]" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" sideOffset={8} className="min-w-40">
        <ConversationShareExportMenuItems
          shareLabel={shareLabel}
          exportLabel={exportLabel}
          onShare={onShare}
          onExport={onExport}
          onCloseMenu={() => setOpen(false)}
        />
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

export function ConversationShareExportSubmenu({
  label,
  shareLabel,
  exportLabel,
  onShare,
  onExport,
  onCloseMenu,
}: { label: string } & ConversationShareExportMenuItemsProps) {
  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger disabled={!onShare && !onExport}>
        <DropdownMenuItemIcon icon={Share2} />
        {label}
      </DropdownMenuSubTrigger>
      <DropdownMenuSubContent className="min-w-40 p-1.5">
        <ConversationShareExportMenuItems
          shareLabel={shareLabel}
          exportLabel={exportLabel}
          onShare={onShare}
          onExport={onExport}
          onCloseMenu={onCloseMenu}
        />
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  );
}
