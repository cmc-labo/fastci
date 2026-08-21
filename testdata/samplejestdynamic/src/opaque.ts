function pickModule(): string {
  return "./plugins/a";
}

export async function useOpaque() {
  const mod = await import(pickModule());
  return mod;
}
