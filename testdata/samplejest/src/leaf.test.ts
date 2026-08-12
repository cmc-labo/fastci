import { hello } from "./leaf";
import { shout } from "./testutil";

test("hello", () => {
  expect(hello()).toBe("hello from leaf");
  expect(shout("ok")).toBe("OK");
});
