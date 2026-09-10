export function normalizeBaseUrl(raw: string): string {
  let u = raw.trim();
  if (!u) throw new Error("Enter a server URL");
  if (!/^https?:\/\//i.test(u)) u = `http://${u}`;
  u = u.replace(/\/+$/, "");
  let parsed: URL;
  try {
    parsed = new URL(u);
  } catch {
    throw new Error("That doesn’t look like a valid URL");
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    throw new Error("URL must start with http:// or https://");
  }
  if (!parsed.hostname) throw new Error("URL is missing a host");
  return `${parsed.protocol}//${parsed.host}`;
}

export function wsUrl(
  base: string,
  token?: string | null,
  viewDeviceId?: string | null,
): string {
  const u = new URL(base);
  u.protocol = u.protocol === "https:" ? "wss:" : "ws:";
  u.pathname = "/ws";
  u.search = "";
  if (token) u.searchParams.set("token", token);
  if (viewDeviceId) u.searchParams.set("view_device_id", viewDeviceId);
  return u.toString();
}
