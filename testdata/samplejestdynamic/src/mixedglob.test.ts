import { loadPlugin } from "./mixedglob";

test("loadPlugin", async () => {
  await loadPlugin("a");
});
