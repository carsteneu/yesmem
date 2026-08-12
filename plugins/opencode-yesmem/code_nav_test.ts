// code_nav_test.ts — verify false-positive BLOCK fix + whitelist coverage
// Run: bun test plugins/opencode-yesmem/code_nav_test.ts

import { test, expect, describe } from "bun:test";
import { isNonCodeFile } from "./code_nav";

describe("isNonCodeFile — non-code whitelist (Bug 3)", () => {
  test("returns true for LICENSE / COPYING", () => {
    expect(isNonCodeFile("LICENSE")).toBe(true);
    expect(isNonCodeFile("LICENSE.txt")).toBe(true);
    expect(isNonCodeFile("COPYING")).toBe(true);
    expect(isNonCodeFile("NOTICE")).toBe(true);
  });

  test("returns true for Dockerfile / Makefile / CHANGELOG", () => {
    expect(isNonCodeFile("Dockerfile")).toBe(true);
    expect(isNonCodeFile("Dockerfile.dev")).toBe(true);
    expect(isNonCodeFile("Makefile")).toBe(true);
    expect(isNonCodeFile("CHANGELOG")).toBe(true);
    expect(isNonCodeFile("CHANGELOG.md")).toBe(true);
  });

  test("returns true for README regardless of extension", () => {
    expect(isNonCodeFile("README")).toBe(true);
    expect(isNonCodeFile("README.md")).toBe(true);
    expect(isNonCodeFile("README.txt")).toBe(true);
  });

  test("returns true for plain-text extensions", () => {
    expect(isNonCodeFile("NOTES.txt")).toBe(true);
    expect(isNonCodeFile("TODO.txt")).toBe(true);
    expect(isNonCodeFile("docs.rst")).toBe(true);
  });

  test("returns true for path-qualified files (parent dir preserved)", () => {
    expect(isNonCodeFile("docs/LICENSE")).toBe(true);
    expect(isNonCodeFile("build/Dockerfile")).toBe(true);
    expect(isNonCodeFile("vendor/README.md")).toBe(true);
  });

  test("returns false for actual source files", () => {
    expect(isNonCodeFile("main.go")).toBe(false);
    expect(isNonCodeFile("index.ts")).toBe(false);
    expect(isNonCodeFile("rpc.ts")).toBe(false);
    expect(isNonCodeFile("app.py")).toBe(false);
    expect(isNonCodeFile("handler.go")).toBe(false);
  });

  test("still honors existing .git* and .md whitelists", () => {
    expect(isNonCodeFile(".gitignore")).toBe(true);
    expect(isNonCodeFile(".gitattributes")).toBe(true);
    expect(isNonCodeFile(".gitmodules")).toBe(true);
    expect(isNonCodeFile("CONTRIBUTING.md")).toBe(true);
  });
});
