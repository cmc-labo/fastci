export async function useBroken(x: string) {
  const mod = await import(`./nonexistent/${x}`);
  return mod;
}
