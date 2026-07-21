// skill_whitelist_test.ts — verify ghost skill filtering
// Run: bun test plugins/opencode-yesmem/skill_whitelist_test.ts

import { test, expect, beforeEach, describe } from "bun:test";
import { mkdirSync, rmSync, writeFileSync, mkdir } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";

const REAL_HOME = process.env.HOME;
let fakeHome: string;

beforeEach(() => {
  fakeHome = `${tmpdir()}/whitelist_test_${Date.now()}_${Math.random().toString(36).slice(2)}`;
  mkdirSync(join(fakeHome, ".claude", "skills", "yesmem-remember"), { recursive: true });
  writeFileSync(join(fakeHome, ".claude", "skills", "yesmem-remember", "SKILL.md"), "#");
  mkdirSync(join(fakeHome, ".claude", "skills", "brainstorming"), { recursive: true });
  writeFileSync(join(fakeHome, ".claude", "skills", "brainstorming", "SKILL.md"), "#");
  mkdirSync(join(fakeHome, ".agents", "skills", "reddit"), { recursive: true });
  writeFileSync(join(fakeHome, ".agents", "skills", "reddit", "SKILL.md"), "#");
  process.env.HOME = fakeHome;
});

describe("loadInstalledSkills", () => {
  test("returns lowercase skill names from ~/.claude/skills and ~/.agents/skills", async () => {
    const mod = await import("./skill_whitelist.ts?t=" + Date.now());
    mod._resetCacheForTest();
    const names = mod.loadInstalledSkills();
    expect(names.has("yesmem-remember")).toBe(true);
    expect(names.has("brainstorming")).toBe(true);
    expect(names.has("reddit")).toBe(true);
  });

  test("ghost skill yesmem-docs is NOT in whitelist (no dir created)", async () => {
    const mod = await import("./skill_whitelist.ts?t=" + Date.now());
    mod._resetCacheForTest();
    const names = mod.loadInstalledSkills();
    expect(names.has("yesmem-docs")).toBe(false);
    expect(names.has("yesmem-search")).toBe(false);
  });
});

describe("isSkillInstalled", () => {
  test("installed skill returns true (case-insensitive)", async () => {
    const mod = await import("./skill_whitelist.ts?t=" + Date.now());
    mod._resetCacheForTest();
    expect(mod.isSkillInstalled("yesmem-remember")).toBe(true);
    expect(mod.isSkillInstalled("Yesmem-Remember")).toBe(true);
  });

  test("ghost skill returns false", async () => {
    const mod = await import("./skill_whitelist.ts?t=" + Date.now());
    mod._resetCacheForTest();
    expect(mod.isSkillInstalled("yesmem-docs")).toBe(false);
    expect(mod.isSkillInstalled("")).toBe(false);
  });
});

describe("cache invalidation", () => {
  test("adding a skill dir triggers rescan on next call", async () => {
    const mod = await import("./skill_whitelist.ts?t=" + Date.now());
    mod._resetCacheForTest();
    expect(mod.isSkillInstalled("newskill")).toBe(false);
    mkdirSync(join(fakeHome, ".claude", "skills", "newskill"), { recursive: true });
    writeFileSync(join(fakeHome, ".claude", "skills", "newskill", "SKILL.md"), "#");
    expect(mod.isSkillInstalled("newskill")).toBe(true);
  });
});

declare module "bun:test" {
  function describe(name: string, fn: () => void): void;
}
