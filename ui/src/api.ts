import PocketBase from 'pocketbase';

export const pb = new PocketBase(window.location.origin);
export class FeatureError extends Error {
  constructor(public status: number, public detail: string, public username = '') {
    super(detail);
  }
}
export async function feature<T>(path: string, body?: unknown): Promise<T> {
  const response = await fetch(`/japi${path}`, {
    method: body === undefined ? 'GET' : 'POST',
    headers: {
      Authorization: `Bearer ${pb.authStore.token}`,
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    cache: 'no-store',
  });
  const data = await response.json();
  if (!response.ok) {
    if (response.status === 401) pb.authStore.clear();
    throw new FeatureError(response.status, data.detail || data.message || 'Request failed', data.username);
  }
  return data as T;
}
export type Setting = {
  key: string; label: string; hint: string; min: number; max: number; value: number;
};
