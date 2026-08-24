import { test, expect } from "vitest";
import { useDyn } from "./dynconsumer";

test("useDyn", async () => {
  expect(await useDyn()).toBe("dyn");
});
