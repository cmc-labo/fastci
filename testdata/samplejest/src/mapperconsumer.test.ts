import { useLib } from "./mapperconsumer";

test("useLib", () => {
  expect(useLib()).toBe("lib thing");
});
