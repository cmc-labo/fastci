export async function loadPlugin(name: string) {
  const mod = await import(`./plugins/${name}`);
  return mod;
}

export async function loadFromMissingDir(name: string) {
  const mod = await import(`./missingdir/${name}`);
  return mod;
}
