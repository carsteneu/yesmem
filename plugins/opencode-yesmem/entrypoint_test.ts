import { expect, test } from "bun:test";
import plugin from "./index";

test("entrypoint exposes only the plugin factory", async () => {
  const entrypoint = await import("./index");
  const factories = [...new Set(Object.values(entrypoint))];

  expect(factories).toEqual([plugin]);
});
