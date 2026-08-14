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
import { useEffect } from "react";

import { PlaygroundChat } from "./components/chat/playground-chat";
import { PlaygroundInput } from "./components/input/playground-input";
import {
  useChatHandler,
  usePlaygroundConversation,
  usePlaygroundOptions,
  usePlaygroundState,
} from "./hooks";
import { getPlaygroundModelProfile } from "./lib";

export function Playground() {
  const {
    config,
    parameterEnabled,
    messages,
    isLoadingMessages,
    models,
    groups,
    updateMessages,
    setModels,
    setGroups,
    updateConfig,
    updateParameterEnabled,
    clearMessages,
  } = usePlaygroundState();

  const { sendChat, stopGeneration, isGenerating } = useChatHandler({
    config,
    parameterEnabled,
    onMessageUpdate: updateMessages,
  });

  const {
    editingMessageKey,
    handleSendMessage,
    handleRegenerateMessage,
    handleEditMessage,
    handleEditOpenChange,
    applyEdit,
    handleDeleteMessage,
  } = usePlaygroundConversation({
    messages,
    updateMessages,
    sendChat,
  });

  const handleClearMessages = () => {
    handleEditOpenChange(false);
    clearMessages();
  };

  const { isLoadingModels } = usePlaygroundOptions({
    currentGroup: config.group,
    currentModel: config.model,
    setGroups,
    setModels,
    updateConfig,
  });

  useEffect(() => {
    const profile = getPlaygroundModelProfile(config.model);

    if (profile.defaultImageSize) {
      updateConfig("imageSize", profile.defaultImageSize);
    }
    if (profile.defaultImageQuality) {
      updateConfig("imageQuality", profile.defaultImageQuality);
    }
    if (profile.defaultImageResponseFormat) {
      updateConfig("imageResponseFormat", profile.defaultImageResponseFormat);
    }
    if (profile.defaultTtsVoice) {
      updateConfig("ttsVoice", profile.defaultTtsVoice);
    }
    if (profile.defaultTtsFormat) {
      updateConfig("ttsFormat", profile.defaultTtsFormat);
    }
    if (profile.defaultVideoSeconds) {
      updateConfig("videoSeconds", profile.defaultVideoSeconds);
    }
    if (profile.defaultVideoSize) {
      updateConfig("videoSize", profile.defaultVideoSize);
    }
  }, [config.model, updateConfig]);

  const handleModelChange = (value: string) => {
    updateConfig("model", value);
  };

  return (
    <div className="relative flex size-full min-h-0 flex-col overflow-hidden">
      {/* Full-width scroll container: scrolling works even over side whitespace */}
      <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
        <PlaygroundChat
          messages={messages}
          isLoadingMessages={isLoadingMessages}
          onRegenerateMessage={handleRegenerateMessage}
          onEditMessage={handleEditMessage}
          onDeleteMessage={handleDeleteMessage}
          onSelectPrompt={handleSendMessage}
          isGenerating={isGenerating}
          editingKey={editingMessageKey}
          onCancelEdit={handleEditOpenChange}
          onSaveEdit={(newContent) => applyEdit(newContent, false)}
          onSaveEditAndSubmit={(newContent) => applyEdit(newContent, true)}
        />
      </div>

      {/* Input area: center content and constrain to the same container width */}
      <div className="mx-auto w-full max-w-4xl">
        <PlaygroundInput
          config={config}
          disabled={isGenerating}
          groups={groups}
          groupValue={config.group}
          isGenerating={isGenerating}
          isModelLoading={isLoadingModels}
          modelValue={config.model}
          models={models}
          onGroupChange={(value) => updateConfig("group", value)}
          onConfigChange={updateConfig}
          onClearMessages={handleClearMessages}
          onModelChange={handleModelChange}
          onParameterEnabledChange={updateParameterEnabled}
          onStop={stopGeneration}
          onSubmit={handleSendMessage}
          parameterEnabled={parameterEnabled}
          hasMessages={messages.length > 0}
        />
      </div>
    </div>
  );
}
