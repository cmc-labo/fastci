import { test, expect } from "vitest";
import { run } from "./consumer";

test("run", () => {
  expect(run()).toBe("hello from leaf via mid via consumer");
});
