import { greet } from "./mid";

test("greet", () => {
  expect(greet()).toBe("hello from leaf via mid");
});
