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
import { FileIcon, XIcon } from "lucide-react";
import { type DragEvent, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import {
  PromptInput,
  PromptInputFooter,
  PromptInputTextarea,
  type PromptInputMessage,
} from "@/components/ai-elements/prompt-input";

import {
  capturePlaygroundScreenshot,
  fileToPlaygroundAttachment,
  MAX_ATTACHMENT_COUNT,
  MAX_TOTAL_ATTACHMENT_BYTES,
  getPlaygroundModelProfile,
} from "../../lib";
import type {
  ModelOption,
  GroupOption,
  ParameterEnabled,
  PlaygroundConfig,
  PlaygroundAttachment,
} from "../../types";
import { PlaygroundInputControls } from "./playground-input-controls";
import { PlaygroundInputTools } from "./playground-input-tools";

interface PlaygroundInputProps {
  config: PlaygroundConfig;
  onSubmit: (text: string, attachments?: PlaygroundAttachment[]) => void;
  onStop?: () => void;
  disabled?: boolean;
  isGenerating?: boolean;
  models: ModelOption[];
  modelValue: string;
  onModelChange: (value: string) => void;
  isModelLoading?: boolean;
  groups: GroupOption[];
  groupValue: string;
  onGroupChange: (value: string) => void;
  hasMessages?: boolean;
  onConfigChange: <K extends keyof PlaygroundConfig>(
    key: K,
    value: PlaygroundConfig[K],
  ) => void;
  onClearMessages?: () => void;
  onParameterEnabledChange: (
    key: keyof ParameterEnabled,
    value: boolean,
  ) => void;
  parameterEnabled: ParameterEnabled;
}

export function PlaygroundInput({
  config,
  onSubmit,
  onStop,
  disabled,
  isGenerating,
  models,
  modelValue,
  onModelChange,
  isModelLoading = false,
  groups,
  groupValue,
  onGroupChange,
  hasMessages = false,
  onConfigChange,
  onClearMessages,
  onParameterEnabledChange,
  parameterEnabled,
}: PlaygroundInputProps) {
  const { t } = useTranslation();
  const [text, setText] = useState("");
  const [attachments, setAttachments] = useState<PlaygroundAttachment[]>([]);
  const attachmentsRef = useRef<PlaygroundAttachment[]>([]);
  const attachmentQueueRef = useRef<Promise<void>>(Promise.resolve());
  const [isFileDragActive, setIsFileDragActive] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const photoInputRef = useRef<HTMLInputElement>(null);
  const cameraInputRef = useRef<HTMLInputElement>(null);
  const modelProfile = getPlaygroundModelProfile(modelValue);

  const replaceAttachments = (next: PlaygroundAttachment[]) => {
    attachmentsRef.current = next;
    setAttachments(next);
  };

  const removeAttachment = (id: string) => {
    replaceAttachments(
      attachmentsRef.current.filter((attachment) => attachment.id !== id),
    );
  };

  const appendFiles = (files: File[]): Promise<void> => {
    const task = attachmentQueueRef.current.then(async () => {
      try {
        const current = attachmentsRef.current;
        const available = Math.max(0, MAX_ATTACHMENT_COUNT - current.length);
        const next = await Promise.all(
          files.slice(0, available).map(fileToPlaygroundAttachment),
        );
        const currentBytes = current.reduce(
          (sum, item) => sum + item.size,
          0,
        );
        const nextBytes = next.reduce((sum, item) => sum + item.size, 0);
        if (currentBytes + nextBytes > MAX_TOTAL_ATTACHMENT_BYTES) {
          throw new Error("Total attachment size must be 20 MB or smaller");
        }
        replaceAttachments([...current, ...next]);
      } catch (error) {
        toast.error(
          error instanceof Error ? t(error.message) : t("Upload failed"),
        );
      }
    });
    attachmentQueueRef.current = task;
    return task;
  };

  const handleAttachmentAction = async (action: string) => {
    if (action === "upload-file") fileInputRef.current?.click();
    if (action === "upload-photo") photoInputRef.current?.click();
    if (action === "take-photo") cameraInputRef.current?.click();
    if (action === "take-screenshot") {
      try {
        await appendFiles([await capturePlaygroundScreenshot()]);
      } catch (error) {
        toast.error(
          error instanceof Error ? t(error.message) : t("Capture failed"),
        );
      }
    }
  };

  const handleSubmit = async (message: PromptInputMessage) => {
    await attachmentQueueRef.current;
    const submittableText = message.text?.trim() ?? "";
    const submittedAttachments = attachmentsRef.current;
    if (disabled || (!submittableText && submittedAttachments.length === 0)) {
      return;
    }
    onSubmit(submittableText, submittedAttachments);
    setText("");
    replaceAttachments([]);
  };

  const handleFileDragEnter = (event: DragEvent<HTMLFormElement>) => {
    if (event.dataTransfer.types.includes("Files")) {
      setIsFileDragActive(true);
    }
  };

  const handleFileDragLeave = (event: DragEvent<HTMLFormElement>) => {
    const nextTarget = event.relatedTarget;
    if (
      nextTarget instanceof Node &&
      event.currentTarget.contains(nextTarget)
    ) {
      return;
    }
    setIsFileDragActive(false);
  };

  return (
    <div className="grid shrink-0 gap-4 px-1 md:pb-4">
      <PromptInput
        className="relative"
        groupClassName={`bg-background/95 dark:bg-background/80 border-border/70 shadow-[0_18px_60px_-32px_rgba(0,0,0,0.65)] ring-1 ring-foreground/5 rounded-xl overflow-hidden transition-all duration-200 focus-within:border-primary/45 focus-within:ring-primary/15 focus-within:shadow-[0_22px_70px_-34px_rgba(0,0,0,0.75)] ${isFileDragActive ? "border-primary/70 ring-primary/25 bg-primary/[0.04]" : ""}`}
        onDragEnter={handleFileDragEnter}
        onDragLeave={handleFileDragLeave}
        onDrop={() => setIsFileDragActive(false)}
        onFilesAdded={appendFiles}
        onSubmit={handleSubmit}
      >
        <input
          className="hidden"
          multiple
          onChange={(event) => {
            void appendFiles([...(event.target.files ?? [])]);
            event.target.value = "";
          }}
          ref={fileInputRef}
          type="file"
        />
        <input
          accept="image/*"
          className="hidden"
          multiple
          onChange={(event) => {
            void appendFiles([...(event.target.files ?? [])]);
            event.target.value = "";
          }}
          ref={photoInputRef}
          type="file"
        />
        <input
          accept="image/*"
          capture="environment"
          className="hidden"
          onChange={(event) => {
            void appendFiles([...(event.target.files ?? [])]);
            event.target.value = "";
          }}
          ref={cameraInputRef}
          type="file"
        />

        {attachments.length > 0 && (
          <div className="flex min-h-16 flex-wrap items-start gap-2 px-4 pt-3">
            {attachments.map((attachment) => {
              if (attachment.kind === "image") {
                return (
                <div
                  className="border-border/70 bg-muted/35 group relative size-16 shrink-0 overflow-hidden rounded-md border"
                  key={attachment.id}
                  title={attachment.name}
                >
                  <img
                    alt={attachment.name}
                    className="size-full object-cover"
                    src={attachment.dataUrl}
                  />
                  <button
                    aria-label={t("Remove attachment")}
                    className="absolute top-1 right-1 grid size-5 place-items-center rounded-full bg-black/75 text-white shadow-sm transition-colors hover:bg-black"
                    onClick={() => removeAttachment(attachment.id)}
                    type="button"
                  >
                    <XIcon size={12} />
                  </button>
                </div>
                );
              }

              if (attachment.kind === "video") {
                return (
                <div
                  className="border-border/70 bg-muted/35 group relative size-16 shrink-0 overflow-hidden rounded-md border"
                  key={attachment.id}
                  title={attachment.name}
                >
                  <video
                    aria-label={attachment.name}
                    className="size-full object-cover"
                    muted
                    playsInline
                    preload="metadata"
                    src={attachment.dataUrl}
                  />
                  <button
                    aria-label={t("Remove attachment")}
                    className="absolute top-1 right-1 grid size-5 place-items-center rounded-full bg-black/75 text-white shadow-sm transition-colors hover:bg-black"
                    onClick={() => removeAttachment(attachment.id)}
                    type="button"
                  >
                    <XIcon size={12} />
                  </button>
                </div>
                );
              }

              return (
                <div
                  className="border-border/70 bg-muted/35 relative flex size-16 shrink-0 flex-col items-center justify-center gap-1 overflow-hidden rounded-md border px-1.5 pt-2"
                  key={attachment.id}
                  title={attachment.name}
                >
                  <FileIcon className="text-muted-foreground size-6 shrink-0" />
                  <span className="w-full truncate text-center text-[10px] leading-3">
                    {attachment.name}
                  </span>
                  <button
                    aria-label={t("Remove attachment")}
                    className="absolute top-1 right-1 grid size-5 place-items-center rounded-full bg-black/75 text-white shadow-sm transition-colors hover:bg-black"
                    onClick={() => removeAttachment(attachment.id)}
                    type="button"
                  >
                    <XIcon size={12} />
                  </button>
                </div>
              );
            })}
          </div>
        )}

        <div className="border-border/60 bg-muted/15 grid min-w-0 gap-2 border-b px-4 py-2.5">
          <div className="flex min-w-0 items-center gap-2">
            <span className="border-border/70 bg-background/70 shrink-0 rounded-md border px-2 py-0.5 text-[11px] font-semibold">
              {t(modelProfile.badge)}
            </span>
            <span className="text-muted-foreground min-w-0 truncate text-xs">
              {t(modelProfile.description)}
            </span>
            <code className="text-muted-foreground/80 ml-auto hidden shrink-0 font-mono text-[10px] sm:block">
              POST {modelProfile.endpoint}
            </code>
          </div>

          {modelProfile.requestHints && (
            <div className="flex min-w-0 flex-wrap items-center gap-1.5">
              {modelProfile.requestHints.map((hint) => (
                <code
                  className="border-border/60 bg-background/65 text-muted-foreground rounded border px-1.5 py-0.5 font-mono text-[10px]"
                  key={`${hint.label}-${hint.value}`}
                >
                  {hint.label}: {t(hint.value)}
                </code>
              ))}
            </div>
          )}
        </div>

        <PromptInputTextarea
          autoComplete="off"
          autoCorrect="off"
          autoCapitalize="off"
          spellCheck={false}
          className="min-h-20 px-5 pt-4 pb-3 leading-7 md:min-h-24 md:text-base"
          disabled={disabled}
          onChange={(event) => setText(event.target.value)}
          placeholder={t("Ask anything")}
          value={text}
        />

        <PromptInputFooter className="border-border/60 bg-muted/20 dark:bg-muted/10 border-t px-3 py-2.5 backdrop-blur">
          <PlaygroundInputControls
            disabled={disabled}
            groups={groups}
            groupValue={groupValue}
            isGenerating={isGenerating}
            isModelLoading={isModelLoading}
            models={models}
            modelValue={modelValue}
            onGroupChange={onGroupChange}
            onModelChange={onModelChange}
            onStop={onStop}
            text={text}
            attachmentCount={attachments.length}
            tools={
              <PlaygroundInputTools
                config={config}
                disabled={disabled}
                hasMessages={hasMessages}
                onConfigChange={onConfigChange}
                onClearMessages={onClearMessages}
                onParameterEnabledChange={onParameterEnabledChange}
                parameterEnabled={parameterEnabled}
                onAttachmentAction={handleAttachmentAction}
              />
            }
          />
        </PromptInputFooter>
      </PromptInput>
    </div>
  );
}
