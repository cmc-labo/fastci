import { test, expect } from "vitest";
import { isolated } from "./isolated";

test("isolated", () => {
  expect(isolated()).toBe("isolated");
});
