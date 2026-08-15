export async function useDyn(): Promise<string> {
  const mod = await import("./dynleaf");
  return mod.dyn();
}
