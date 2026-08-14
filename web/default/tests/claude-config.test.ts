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
import { describe, expect, test } from "bun:test";

import {
  buildClaudeInstallCommand,
  CLAUDE_MODELS,
  detectClaudePlatform,
} from "../src/features/keys/lib/claude-config";

describe("Claude setup command generation", () => {
  test("keeps the exact five-model contract", () => {
    expect([...CLAUDE_MODELS]).toEqual([
      "claude-fable-5",
      "claude-opus-4-8",
      "claude-opus-5",
      "claude-sonnet-5",
      "claude-haiku-4-5",
    ]);
  });

  test("builds token-scoped Windows and macOS commands", () => {
    const key = "sk-test'quoted";
    const windows = buildClaudeInstallCommand({
      apiKey: key,
      platform: "windows",
    });
    const macos = buildClaudeInstallCommand({ apiKey: key, platform: "macos" });

    expect(windows).toContain("$env:MADAPI_KEY='sk-test''quoted'");
    expect(windows).toContain("/mad-claude/install.ps1");
    expect(windows).toContain("finally {");
    expect(windows).not.toContain("MADAPI_CLAUDE_INSTALL_LANGUAGE='1'");
    expect(macos).toContain("MADAPI_KEY='sk-test'\"'\"'quoted'");
    expect(macos).toContain("/mad-claude/install.sh");
    expect(macos).toContain("mktemp");
    expect(macos).not.toContain("MADAPI_CLAUDE_INSTALL_LANGUAGE='1'");
  });

  test("adds the independent language option only when selected", () => {
    const windows = buildClaudeInstallCommand({
      apiKey: "sk-test",
      platform: "windows",
      installLanguage: true,
    });
    const macos = buildClaudeInstallCommand({
      apiKey: "sk-test",
      platform: "macos",
      installLanguage: true,
    });

    expect(windows).toContain("$env:MADAPI_CLAUDE_INSTALL_LANGUAGE='1'");
    expect(windows).toContain("Remove-Item Env:MADAPI_CLAUDE_INSTALL_LANGUAGE");
    expect(macos).toContain("MADAPI_CLAUDE_INSTALL_LANGUAGE='1'");
  });

  test("detects Windows and Apple clients", () => {
    expect(
      detectClaudePlatform("Mozilla/5.0 (Macintosh; Intel Mac OS X)"),
    ).toBe("macos");
    expect(
      detectClaudePlatform("Mozilla/5.0 (Windows NT 10.0; Win64; x64)"),
    ).toBe("windows");
  });
});
