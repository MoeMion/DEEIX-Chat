"use client";

import * as React from "react";
import { motion } from "motion/react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { LogoCarousel, type LogoCarouselLogo } from "@/components/ui/logo-carousel";
import { Onboarding } from "@/components/ui/onboarding";
import { SpinnerLabel } from "@/components/ui/spinner";
import { dispatchUserProfileUpdated } from "@/features/settings/events/user-profile-events";
import { serializeAppearancePreferences } from "@/features/settings/utils/appearance-preferences";
import {
  completeOnboarding,
  isPasswordReuseNotAllowedError,
  patchMe,
  patchUsername,
} from "@/shared/api/auth";
import type { UserDTO } from "@/shared/api/auth.types";
import {
  DISPLAY_NAME_MAX_LENGTH,
  PASSWORD_MIN_LENGTH,
  USERNAME_MAX_LENGTH,
  isDisplayNameLengthValid,
  isPasswordPolicyValid,
  isUsernamePolicyValid,
} from "@/shared/auth/account-policy";
import { useAuthSession } from "@/shared/auth/auth-session-context";
import { clearSessionAndRedirectToLogin } from "@/shared/auth/session";
import { useAppLocale } from "@/i18n/app-i18n-provider";
import type { AppLocale } from "@/i18n/config";
import { useLocalizedErrorMessage } from "@/i18n/use-localized-error";
import { AppLogo } from "@/shared/components/app-logo";
import { useTheme, type Theme, type ThemePreset } from "@/shared/components/theme-provider";

type OnboardingTip = {
  key: string;
};

const adminOnboardingTips: OnboardingTip[] = [
  { key: "adminTips.upstreams" },
  { key: "adminTips.mcp" },
  { key: "adminTips.files" },
  { key: "adminTips.context" },
  { key: "adminTips.trace" },
  { key: "adminTips.billing" },
  { key: "adminTips.admin" },
  { key: "adminTips.ops" },
];

const userOnboardingTips: OnboardingTip[] = [
  { key: "userTips.profile" },
  { key: "userTips.models" },
  { key: "userTips.files" },
  { key: "userTips.conversation" },
];

function buildLogoCarouselItems(): LogoCarouselLogo[] {
  const supportedIconSlugs = [
    "openai",
    "codex",
    "anthropic",
    "claude",
    "google",
    "gemini",
    "gemma",
    "xai",
    "grok",
    "moonshot",
    "kimi",
    "alibaba",
    "alibabacloud",
    "qwen",
    "deepseek",
    "xiaomimimo",
    "zhipu",
    "chatglm",
    "minimax",
    "doubao",
    "mistral",
    "hunyuan",
    "longcat",
    "openrouter",
    "copilot",
    "replicate",
    "fal",
    "stability",
    "runway",
    "luma",
    "ideogram",
    "midjourney",
    "suno",
    "elevenlabs",
  ];
  return supportedIconSlugs.map((slug, index) => {
    return {
      id: `${slug}-${index}`,
      name: titleFromIconSlug(slug),
      src: `/vendor/lobehub-icons/${slug}.svg`,
    };
  });
}

function titleFromIconSlug(slug: string): string {
  return slug
    .replace(/-(brand|brand-color|color|text|text-cn)$/u, "")
    .split("-")
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

const onboardingLogoItems = buildLogoCarouselItems();
const simplifiedOnboardingLocale: AppLocale = "zh-CN";
const simplifiedOnboardingTimezone = "Asia/Shanghai";
const simplifiedOnboardingTheme: Theme = "system";
const simplifiedOnboardingThemePreset: ThemePreset = "azure";

function simplifiedOnboardingAppearancePreferences(): string {
  return serializeAppearancePreferences({
    theme: simplifiedOnboardingTheme,
    preset: simplifiedOnboardingThemePreset,
    chatFont: "default",
    chatFontWeight: "regular",
    fontSize: "standard",
  });
}

function OnboardingFeatureCarousel({
  activeIndex,
  logos,
  tips,
}: {
  activeIndex: number;
  logos: LogoCarouselLogo[];
  tips: OnboardingTip[];
}) {
  const t = useTranslations("guide");
  const activeTip = tips[activeIndex] ?? tips[0];
  const progressDurationSeconds = 4.2;

  return (
    <div className="flex h-full flex-col overflow-hidden">
      <div className="flex min-h-[238px] flex-1 items-center justify-center px-5">
        <LogoCarousel
          logos={logos}
          columnCount={3}
          className="w-full justify-center space-x-4"
          columnClassName="h-24 w-24 md:h-24 md:w-24"
          logoClassName="h-12 w-12 md:h-14 md:w-14"
        />
      </div>
      <div className="space-y-3 p-3">
        <div className="flex w-full gap-1.5 overflow-hidden">
          {tips.map((_, index) => (
            <div className="h-1 min-w-0 flex-1 overflow-hidden rounded-full bg-border/80" key={index}>
              {index === activeIndex ? (
                <motion.span
                  key={`onboarding-progress-${activeIndex}`}
                  className="block h-full origin-left rounded-full bg-foreground/75"
                  initial={{ scaleX: 0 }}
                  animate={{ scaleX: 1 }}
                  transition={{ duration: progressDurationSeconds, ease: "linear" }}
                />
              ) : null}
            </div>
          ))}
        </div>
        <div className="flex h-[3.75rem] items-start overflow-hidden">
          <motion.p
            key={activeTip.key}
            className="text-xs font-medium leading-5 tracking-normal text-foreground"
            initial={{ opacity: 0, y: 4 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -4 }}
            transition={{ duration: 0.22, ease: "easeOut" }}
          >
            {t(activeTip.key)}
          </motion.p>
        </div>
      </div>
    </div>
  );
}

export function InitialSecurityGuard() {
  const t = useTranslations("guide");
  const tCommonErrors = useTranslations("common.errors");
  const resolveErrorMessage = useLocalizedErrorMessage();
  const { locale, setLocale } = useAppLocale();
  const { setPreset, setTheme } = useTheme();
  const { accessToken, user, refreshUser } = useAuthSession();
  const [viewer, setViewer] = React.useState<UserDTO | null>(null);
  const [step, setStep] = React.useState(1);
  const [activeTipIndex, setActiveTipIndex] = React.useState(0);
  const [guideActive, setGuideActive] = React.useState(false);
  const [username, setUsername] = React.useState("");
  const [displayName, setDisplayName] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [savingAccount, setSavingAccount] = React.useState(false);
  const [finishing, setFinishing] = React.useState(false);
  const isAdminGuide = viewer?.role === "admin" || viewer?.role === "superadmin";
  const activeOnboardingTips = isAdminGuide ? adminOnboardingTips : userOnboardingTips;

  React.useEffect(() => {
    setViewer(user);
    setUsername(user?.username ?? "");
    setDisplayName(user?.displayName ?? "");
    if (!user) {
      setGuideActive(false);
      setStep(1);
      return;
    }
    if (user.initialSecurityRequired && locale !== simplifiedOnboardingLocale) {
      void setLocale(simplifiedOnboardingLocale);
    }
    if (user.initialSecurityRequired) {
      setTheme(simplifiedOnboardingTheme);
      setPreset(simplifiedOnboardingThemePreset);
    }
    setGuideActive(Boolean(user.initialSecurityRequired));
  }, [locale, setLocale, setPreset, setTheme, user]);

  React.useEffect(() => {
    if (!guideActive) {
      return;
    }

    const timer = window.setInterval(() => {
      setActiveTipIndex((current) => (current + 1) % activeOnboardingTips.length);
    }, 4200);

    return () => window.clearInterval(timer);
  }, [activeOnboardingTips.length, guideActive]);

  React.useEffect(() => {
    setActiveTipIndex(0);
  }, [isAdminGuide]);

  const isBootstrapAdminSetup = Boolean(viewer?.mustResetPassword);
  const accountTitle = isBootstrapAdminSetup ? t("bootstrapTitle") : isAdminGuide ? t("adminAccountTitle") : t("userAccountTitle");
  const readyDescription = isBootstrapAdminSetup
    ? t("bootstrapReadyDescription")
    : isAdminGuide
      ? t("adminReadyDescription")
      : t("userReadyDescription");

  const submitAccountStep = React.useCallback(async () => {
    if (!viewer?.initialSecurityRequired || savingAccount) return;
    const nextUsername = username.trim().toLowerCase();
    const nextDisplayName = displayName.trim();
    const nextPassword = password.trim();
    if (viewer.initialUsernameRequired && nextUsername === viewer.username.trim().toLowerCase()) {
      toast.error(t("toasts.changeInitialUsername"));
      return;
    }
    if (viewer.initialUsernameRequired && !isUsernamePolicyValid(nextUsername)) {
      toast.error(t("toasts.usernameTooShort"));
      return;
    }
    if (!viewer.mustResetPassword && !isDisplayNameLengthValid(nextDisplayName)) {
      toast.error(t("toasts.displayNameRequired"));
      return;
    }
    if (viewer.mustResetPassword && !isPasswordPolicyValid(nextPassword)) {
      toast.error(t("toasts.passwordTooShort"));
      return;
    }

    setSavingAccount(true);
    try {
      await setLocale(simplifiedOnboardingLocale);
      setTheme(simplifiedOnboardingTheme);
      setPreset(simplifiedOnboardingThemePreset);

      let nextViewer = viewer;
      if (viewer.initialUsernameRequired) {
        nextViewer = await patchUsername(accessToken, { username: nextUsername });
      }
      const profilePayload: Parameters<typeof patchMe>[1] = {
        locale: simplifiedOnboardingLocale,
        timezone: simplifiedOnboardingTimezone,
        appearancePreferences: simplifiedOnboardingAppearancePreferences(),
      };
      if (!viewer.mustResetPassword && nextDisplayName !== viewer.displayName.trim()) {
        profilePayload.displayName = nextDisplayName;
      }
      nextViewer = await patchMe(accessToken, profilePayload);
      setViewer(nextViewer);
      dispatchUserProfileUpdated(nextViewer);
      setStep(2);
    } catch (error) {
      toast.error(t("toasts.saveAccountFailed"), {
        description: resolveErrorMessage(error, tCommonErrors("unknown")),
      });
    } finally {
      setSavingAccount(false);
    }
  }, [accessToken, displayName, password, resolveErrorMessage, savingAccount, setLocale, setPreset, setTheme, t, tCommonErrors, username, viewer]);

  const finishInitialSecurity = React.useCallback(async () => {
    if (!viewer || finishing) return;
    if (viewer.mustResetPassword && !isPasswordPolicyValid(password)) {
      toast.error(t("toasts.passwordTooShort"));
      setStep(1);
      return;
    }
    setFinishing(true);
    try {
      await refreshUser();
      const nextViewer = await completeOnboarding(
        accessToken,
        viewer.mustResetPassword ? { newPassword: password.trim() } : undefined,
      );
      if (nextViewer) {
        setViewer(nextViewer);
        dispatchUserProfileUpdated(nextViewer);
      }
      setGuideActive(false);
      if (viewer.mustResetPassword) {
        toast.success(t("toasts.initializedRelogin"));
        clearSessionAndRedirectToLogin();
        return;
      }
      toast.success(t("toasts.complete"));
    } catch (error) {
      if (isPasswordReuseNotAllowedError(error)) {
        setStep(1);
      }
      toast.error(t("toasts.completeFailed"), {
        description: resolveErrorMessage(error, tCommonErrors("unknown")),
      });
    } finally {
      setFinishing(false);
    }
  }, [accessToken, finishing, password, refreshUser, resolveErrorMessage, t, tCommonErrors, viewer]);

  if (!viewer || !guideActive) {
    return null;
  }

  return (
    <div className="fixed inset-0 z-50 flex min-h-svh items-center justify-center overflow-y-auto bg-background/20 px-3 py-5 backdrop-blur-[2px]">
      <Onboarding
        value={step}
        onValueChange={setStep}
        totalSteps={2}
        role="dialog"
        aria-modal="true"
        aria-label={t("aria.onboarding")}
        className="grid w-full max-w-[820px] animate-in gap-0 overflow-hidden rounded-2xl border border-border/60 bg-background p-0 shadow-xl fade-in-0 zoom-in-95 duration-200 md:h-[430px] md:grid-cols-[0.95fr_1.05fr]"
      >
        <div className="hidden h-full flex-col bg-muted/15 p-4 md:flex">
          <OnboardingFeatureCarousel activeIndex={activeTipIndex} logos={onboardingLogoItems} tips={activeOnboardingTips} />
        </div>

        <div className="flex h-full flex-col p-5">
          <div className="flex items-center justify-between gap-4">
            <AppLogo width={86} height={24} priority className="h-6 w-auto" />
            <Onboarding.StepIndicator variant="dots" dotClassName="bg-muted-foreground/25" />
          </div>

          <div className="flex flex-1">
            <Onboarding.Step step={1} className="flex flex-1 animate-in fade-in-0 slide-in-from-right-2 duration-200">
              <form
                className="flex flex-1 flex-col"
                autoComplete="on"
                onSubmit={(event) => {
                  event.preventDefault();
                  void submitAccountStep();
                }}
              >
                <div className="flex flex-1 items-center">
                  <div className="w-full space-y-6">
                    <Onboarding.Header className="text-left">
                      <div className="space-y-2">
                        <h2 className="text-2xl font-semibold tracking-normal">{accountTitle}</h2>
                      </div>
                    </Onboarding.Header>

                    <div className="space-y-4">
                      <label className="block space-y-1.5" htmlFor="initial-username">
                        <span className="flex items-center text-xs font-medium">
                          {t("labels.username")}
                        </span>
                        <Input
                          id="initial-username"
                          name="username"
                          value={username}
                          onChange={(event) => setUsername(event.target.value.toLowerCase())}
                          disabled={savingAccount}
                          readOnly={!viewer.initialUsernameRequired}
                          maxLength={USERNAME_MAX_LENGTH}
                          autoComplete="username"
                          aria-disabled={!viewer.initialUsernameRequired}
                          placeholder={isBootstrapAdminSetup ? t("placeholders.adminUsername") : t("placeholders.username")}
                        />
                      </label>

                      {isBootstrapAdminSetup ? (
                        <label className="block space-y-1.5" htmlFor="initial-admin-password">
                          <span className="flex items-center text-xs font-medium">
                            {t("labels.password")}
                          </span>
                          <Input
                            id="initial-admin-password"
                            name="password"
                            type="password"
                            value={password}
                            onChange={(event) => setPassword(event.target.value)}
                            disabled={savingAccount || !viewer.mustResetPassword}
                            autoComplete="new-password"
                            minLength={PASSWORD_MIN_LENGTH}
                            placeholder={t("placeholders.adminPassword")}
                          />
                        </label>
                      ) : (
                        <label className="block space-y-1.5" htmlFor="initial-display-name">
                          <span className="flex items-center text-xs font-medium">
                            {t("labels.displayName")}
                          </span>
                          <Input
                            id="initial-display-name"
                            name="name"
                            value={displayName}
                            onChange={(event) => setDisplayName(event.target.value)}
                            disabled={savingAccount}
                            maxLength={DISPLAY_NAME_MAX_LENGTH}
                            autoComplete="name"
                            placeholder={t("placeholders.displayName")}
                          />
                        </label>
                      )}
                    </div>
                  </div>
                </div>

                <Onboarding.Navigation aria-label={t("aria.accountNavigation")} className="mt-auto justify-end pt-6">
                  <Button type="submit" disabled={savingAccount}>
                    {savingAccount ? <SpinnerLabel>{t("saving")}</SpinnerLabel> : t("continue")}
                  </Button>
                </Onboarding.Navigation>
              </form>
            </Onboarding.Step>

            <Onboarding.Step step={2} className="flex flex-1 flex-col animate-in fade-in-0 slide-in-from-right-2 duration-200">
              <div className="flex flex-1 items-center">
                <div className="w-full space-y-5">
                  <Onboarding.Header className="text-left">
                    <div className="space-y-2">
                      <h2 className="text-2xl font-semibold tracking-normal">{t("ready")}</h2>
                      <p className="text-xs text-muted-foreground">
                        {readyDescription}
                      </p>
                    </div>
                  </Onboarding.Header>
                </div>
              </div>

              <Onboarding.Navigation aria-label={t("aria.finishNavigation")} className="mt-auto justify-end pt-6">
                <Button type="button" variant="ghost" className="shadow-none" disabled={finishing} onClick={() => setStep(1)}>
                  {t("back")}
                </Button>
                <Button type="button" disabled={finishing} onClick={() => void finishInitialSecurity()}>
                  {finishing ? <SpinnerLabel>{t("finishing")}</SpinnerLabel> : t("finish")}
                </Button>
              </Onboarding.Navigation>
            </Onboarding.Step>
          </div>
        </div>
      </Onboarding>
    </div>
  );
}
