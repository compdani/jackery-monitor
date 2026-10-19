/**
 * Dashboard session auth (HMAC cookie via FastAPI).
 * This is the only module that changes when swapping to PocketBase /japi later.
 */
import { api, apiPost, ApiError } from './api';

export type AuthState =
  | { kind: 'loading' }
  | { kind: 'setup' }
  | { kind: 'login' }
  | { kind: 'signedIn'; username: string };

export async function me(): Promise<{ username: string } | null> {
  try {
    return await api<{ username: string }>('/auth/me');
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) return null;
    throw err;
  }
}

/** Probe whether the install needs first-run setup. */
export async function probeAuthGate(): Promise<'setup' | 'login' | 'signedIn'> {
  try {
    await api<{ username: string }>('/auth/me');
    return 'signedIn';
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      if (err.detail === 'setup_required') return 'setup';
      return 'login';
    }
    // Transient network — treat as login so the form is reachable.
    return 'login';
  }
}

export async function login(username: string, password: string) {
  return apiPost<{ ok: boolean; username: string; token?: string }>('/auth/login', {
    username,
    password,
  });
}

export async function setup(username: string, password: string) {
  return apiPost<{ ok: boolean; username: string; token?: string }>('/auth/setup', {
    username,
    password,
  });
}

export async function logout() {
  return apiPost<{ ok: boolean }>('/auth/logout');
}

export async function changePassword(current: string, next: string) {
  return apiPost<{ ok: boolean }>('/auth/change_password', {
    current,
    new: next,
  });
}

/** Jackery cloud credentials (separate from dashboard login). */
export async function cloudAuthStatus(): Promise<{ has_credentials: boolean }> {
  return api('/auth/status');
}

export async function saveCloudCredentials(body: {
  email: string;
  password: string;
  region: string;
}) {
  return apiPost<{ ok: boolean }>('/auth/credentials', body);
}

export async function forgetCloudCredentials() {
  return apiPost<{ ok: boolean }>('/auth/forget');
}
