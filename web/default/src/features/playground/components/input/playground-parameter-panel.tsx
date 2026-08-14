/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { SlidersHorizontalIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { PromptInputButton } from "@/components/ai-elements/prompt-input";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";
import { Slider } from "@/components/ui/slider";
import { Switch } from "@/components/ui/switch";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useIsMobile } from "@/hooks/use-mobile";
import { cn } from "@/lib/utils";

import { getPlaygroundModelProfile } from "../../lib/model/model-profiles";
import {
  getParameterControlValueText,
  normalizeParameterNumberValue,
  PLAYGROUND_PARAMETER_CONTROLS,
  PLAYGROUND_PARAMETER_PANEL_SCROLL_CLASS,
  type PlaygroundParameterKey,
} from "../../lib/parameters/playground-parameters";
import type { ParameterEnabled, PlaygroundConfig } from "../../types";

type PlaygroundParameterPanelProps = {
  config: PlaygroundConfig;
  disabled?: boolean;
  onConfigChange: <K extends keyof PlaygroundConfig>(
    key: K,
    value: PlaygroundConfig[K],
  ) => void;
  onParameterEnabledChange: (
    key: PlaygroundParameterKey,
    value: boolean,
  ) => void;
  parameterEnabled: ParameterEnabled;
};

type PlaygroundParameterContentProps = PlaygroundParameterPanelProps & {
  compact?: boolean;
};

function PlaygroundParameterContent({
  compact = false,
  config,
  disabled,
  onConfigChange,
  onParameterEnabledChange,
  parameterEnabled,
}: PlaygroundParameterContentProps) {
  const { t } = useTranslation();
  const modelProfile = getPlaygroundModelProfile(config.model);
  const modelKind = modelProfile.kind;

  const updateParameterConfig = (
    key: PlaygroundParameterKey,
    value: number | null,
  ) => {
    if (key === "seed") {
      onConfigChange("seed", value);
      return;
    }

    onConfigChange(key, value ?? 0);
  };

  if (modelKind !== "chat") {
    return (
      <div
        className={cn(
          "grid w-full min-w-0 gap-3",
          compact ? "px-4 pb-4" : "p-1",
        )}
      >
        {modelKind === "image" && (
          <>
            <label className="grid min-w-0 gap-1.5 text-sm font-medium">
              {t("Image size")}
              <select
                className="border-input bg-background block h-9 w-full min-w-0 max-w-full truncate rounded-md border px-3 text-sm"
                disabled={disabled}
                onChange={(event) =>
                  onConfigChange("imageSize", event.target.value)
                }
                value={config.imageSize}
              >
                {(modelProfile.imageSizes ?? []).map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
            </label>
            <label className="grid min-w-0 gap-1.5 text-sm font-medium">
              {t("Quality")}
              <select
                className="border-input bg-background block h-9 w-full min-w-0 max-w-full truncate rounded-md border px-3 text-sm"
                disabled={disabled}
                onChange={(event) =>
                  onConfigChange("imageQuality", event.target.value)
                }
                value={config.imageQuality}
              >
                <option value="hd">HD</option>
              </select>
            </label>
            {modelProfile.imageResponseFormats && (
              <label className="grid min-w-0 gap-1.5 text-sm font-medium">
                {t("Return format")}
                <select
                  className="border-input bg-background block h-9 w-full min-w-0 max-w-full truncate rounded-md border px-3 text-sm"
                  disabled={disabled}
                  onChange={(event) =>
                    onConfigChange(
                      "imageResponseFormat",
                      event.target.value as PlaygroundConfig["imageResponseFormat"],
                    )
                  }
                  value={config.imageResponseFormat}
                >
                  {modelProfile.imageResponseFormats.map((option) => (
                    <option key={option.value} value={option.value}>
                      {t(option.label)}
                    </option>
                  ))}
                </select>
              </label>
            )}
          </>
        )}

        {modelKind === "video" && (
          <>
            <label className="grid min-w-0 gap-1.5 text-sm font-medium">
              {t("Duration")}
              <Input
                disabled={disabled}
                max={modelProfile.videoDurationMax ?? 30}
                min={modelProfile.videoDurationMin ?? 1}
                onChange={(event) =>
                  onConfigChange(
                    "videoSeconds",
                    Math.max(
                      modelProfile.videoDurationMin ?? 1,
                      Math.min(
                        modelProfile.videoDurationMax ?? 30,
                        Number(event.target.value) ||
                          modelProfile.videoDurationMin ||
                          1,
                      ),
                    ),
                  )
                }
                type="number"
                value={config.videoSeconds}
              />
            </label>
            <label className="grid min-w-0 gap-1.5 text-sm font-medium">
              {t("Resolution")}
              <select
                className="border-input bg-background block h-9 w-full min-w-0 max-w-full truncate rounded-md border px-3 text-sm"
                disabled={disabled}
                onChange={(event) =>
                  onConfigChange("videoSize", event.target.value)
                }
                value={config.videoSize}
              >
                {(modelProfile.videoSizes ?? [
                  { value: "1280x720", label: "1280x720 (16:9)" },
                  { value: "720x1280", label: "720x1280 (9:16)" },
                  { value: "1920x1080", label: "1920x1080 (16:9)" },
                  { value: "1080x1920", label: "1080x1920 (9:16)" },
                ]).map((option) => (
                  <option key={option.value} value={option.value}>
                    {t(option.label)}
                  </option>
                ))}
              </select>
            </label>
          </>
        )}

        {modelKind === "tts" && (
          <>
            <label className="grid min-w-0 gap-1.5 text-sm font-medium">
              {t(modelProfile.ttsVoiceLabel || "Voice")}
              <Input
                disabled={disabled}
                onChange={(event) =>
                  onConfigChange("ttsVoice", event.target.value)
                }
                placeholder={modelProfile.ttsVoicePlaceholder}
                value={config.ttsVoice}
              />
            </label>
            <label className="grid min-w-0 gap-1.5 text-sm font-medium">
              {t("Audio format")}
              <select
                className="border-input bg-background block h-9 w-full min-w-0 max-w-full truncate rounded-md border px-3 text-sm"
                disabled={disabled}
                onChange={(event) =>
                  onConfigChange("ttsFormat", event.target.value)
                }
                value={config.ttsFormat}
              >
                {(modelProfile.ttsFormats ?? []).map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
            </label>
          </>
        )}
      </div>
    );
  }

  return (
    <div
      className={cn(
        "grid gap-3",
        PLAYGROUND_PARAMETER_PANEL_SCROLL_CLASS,
        compact ? "px-4 pb-4" : "p-1",
      )}
    >
      {PLAYGROUND_PARAMETER_CONTROLS.map((control) => {
        const enabled = parameterEnabled[control.key];
        const value = config[control.key];
        const controlId = `playground-${control.key}`;

        return (
          <div
            className={cn(
              "border-border/70 bg-background/60 grid gap-2 rounded-lg border p-3 transition-opacity",
              (!enabled || disabled) && "opacity-55",
            )}
            key={control.key}
          >
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0 space-y-1">
                <div className="flex min-w-0 items-center gap-2">
                  <label
                    className="truncate text-sm leading-5 font-medium"
                    htmlFor={controlId}
                  >
                    {t(control.labelKey)}
                  </label>
                  <Badge
                    className="h-5 max-w-24 shrink-0 px-1.5 font-mono text-[11px]"
                    variant="outline"
                  >
                    {t(getParameterControlValueText(control.key, value))}
                  </Badge>
                </div>
                <p className="text-muted-foreground text-xs leading-4">
                  {t(control.descriptionKey)}
                </p>
              </div>

              <Switch
                aria-label={t("Enable {{parameter}}", {
                  parameter: t(control.labelKey),
                })}
                checked={enabled}
                disabled={disabled}
                onCheckedChange={(checked) =>
                  onParameterEnabledChange(control.key, checked)
                }
                size="sm"
              />
            </div>

            {control.valueType === "slider" ? (
              <Slider
                className="py-1.5"
                disabled={disabled || !enabled}
                id={controlId}
                max={control.max}
                min={control.min}
                onValueChange={(nextValue) => {
                  const firstValue = Array.isArray(nextValue)
                    ? nextValue[0]
                    : nextValue;
                  updateParameterConfig(
                    control.key,
                    normalizeParameterNumberValue(control.key, firstValue),
                  );
                }}
                step={control.step}
                value={[Number(value)]}
              />
            ) : (
              <Input
                disabled={disabled || !enabled}
                id={controlId}
                inputMode="numeric"
                max={control.max}
                min={control.min}
                onChange={(event) => {
                  updateParameterConfig(
                    control.key,
                    normalizeParameterNumberValue(
                      control.key,
                      event.target.value,
                    ),
                  );
                }}
                step={control.step}
                type="number"
                value={value ?? ""}
              />
            )}
          </div>
        );
      })}
    </div>
  );
}

export function PlaygroundParameterPanel(props: PlaygroundParameterPanelProps) {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const modelKind = getPlaygroundModelProfile(props.config.model).kind;
  let activeCount = 2;
  if (modelKind === "chat") {
    activeCount = PLAYGROUND_PARAMETER_CONTROLS.filter(
      (control) => props.parameterEnabled[control.key],
    ).length;
  } else if (modelKind === "image") {
    const profile = getPlaygroundModelProfile(props.config.model);
    activeCount =
      1 +
      (profile.defaultImageQuality === "auto" ? 0 : 1) +
      (profile.imageResponseFormats ? 1 : 0);
  }

  const trigger = (
    <PromptInputButton
      aria-label={t("Parameters")}
      className="text-muted-foreground hover:text-foreground hover:bg-muted/70 relative font-medium"
      disabled={props.disabled}
      variant="ghost"
    >
      <SlidersHorizontalIcon size={16} />
      <span className="bg-primary text-primary-foreground absolute -top-1 -right-1 flex h-3.5 min-w-3.5 items-center justify-center rounded-full px-1 text-[9px] leading-none font-semibold">
        {activeCount}
      </span>
    </PromptInputButton>
  );

  if (isMobile) {
    return (
      <Sheet>
        <Tooltip>
          <TooltipTrigger render={<SheetTrigger render={trigger} />} />
          <TooltipContent>
            <p>{t("Parameters")}</p>
          </TooltipContent>
        </Tooltip>
        <SheetContent
          className="max-h-[85vh] w-full min-w-0 overflow-hidden rounded-t-xl"
          side="bottom"
        >
          <SheetHeader>
            <SheetTitle>{t("Parameter settings")}</SheetTitle>
          </SheetHeader>
          <PlaygroundParameterContent {...props} compact />
        </SheetContent>
      </Sheet>
    );
  }

  return (
    <Popover>
      <Tooltip>
        <TooltipTrigger render={<PopoverTrigger render={trigger} />} />
        <TooltipContent>
          <p>{t("Parameters")}</p>
        </TooltipContent>
      </Tooltip>
      <PopoverContent
        align="start"
        className="w-[22rem] min-w-0 max-w-[calc(100vw-2rem)] overflow-hidden gap-3 p-3"
        collisionPadding={8}
        side="top"
        sideOffset={8}
      >
        <div className="space-y-1 px-1">
          <div className="text-sm font-semibold">{t("Parameter settings")}</div>
          <div className="text-muted-foreground text-xs leading-4">
            {t("Only enabled parameters are sent with the request.")}
          </div>
        </div>
        <PlaygroundParameterContent {...props} />
      </PopoverContent>
    </Popover>
  );
}
