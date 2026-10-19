/** Single seam for FastAPI feature routes. Swap API_BASE later for /japi. */

export const API_BASE = '/api';

export class ApiError extends Error {
  constructor(
    public status: number,
    public detail: string,
  ) {
    super(detail);
  }
}

export function apiDetail(j: unknown, fallback = 'Request failed'): string {
  const obj = j as Record<string, unknown> | null;
  const d = obj?.detail;
  if (typeof d === 'string' && d) return d;
  if (Array.isArray(d) && d.length) {
    return d
      .map((x) => {
        const item = x as Record<string, unknown> | null;
        return (item && (item.msg || item.message)) || JSON.stringify(x);
      })
      .join('; ');
  }
  if (d && typeof d === 'object') {
    const o = d as Record<string, unknown>;
    return String(o.msg || o.message || JSON.stringify(d));
  }
  if (obj && (obj.error || obj.message)) return String(obj.error || obj.message);
  return fallback;
}

type AuthClearHandler = () => void;
let onUnauthorized: AuthClearHandler | null = null;

/** Register a callback when any API call returns 401 (except auth endpoints). */
export function setUnauthorizedHandler(handler: AuthClearHandler | null) {
  onUnauthorized = handler;
}

export async function api<T = unknown>(
  path: string,
  init: RequestInit = {},
): Promise<T> {
  const url = path.startsWith('http')
    ? path
    : `${API_BASE}${path.startsWith('/') ? path : `/${path}`}`;
  const headers = new Headers(init.headers);
  if (init.body != null && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json');
  }
  const response = await fetch(url, {
    credentials: 'same-origin',
    cache: 'no-store',
    ...init,
    headers,
  });
  let data: unknown = null;
  const text = await response.text();
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = { detail: text };
    }
  }
  if (!response.ok) {
    if (response.status === 401 && !path.includes('/auth/')) {
      onUnauthorized?.();
    }
    throw new ApiError(response.status, apiDetail(data, `HTTP ${response.status}`));
  }
  return data as T;
}

export async function apiPost<T = unknown>(path: string, body?: unknown): Promise<T> {
  return api<T>(path, {
    method: 'POST',
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}
