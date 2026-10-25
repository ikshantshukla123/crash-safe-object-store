export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

const API = '/api';
export const TOKEN_KEY = 'objstore.token';
export const DEMO_KEY = 'objstore.demoToken';

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) || '';
}

export function setToken(token: string) {
  if (token) localStorage.setItem(TOKEN_KEY, token);
  else localStorage.removeItem(TOKEN_KEY);
}

export function getDemoToken(): string {
  return localStorage.getItem(DEMO_KEY) || '';
}

export function setDemoToken(token: string) {
  if (token) localStorage.setItem(DEMO_KEY, token);
  else localStorage.removeItem(DEMO_KEY);
}

export async function api<T>(path: string, init: RequestInit = {}, token = getToken()): Promise<T> {
  const headers = new Headers(init.headers);
  if (token) headers.set('Authorization', `Bearer ${token}`);
  if (init.body && !headers.has('Content-Type') && typeof init.body === 'string') {
    headers.set('Content-Type', 'application/json');
  }
  const res = await fetch(`${API}${path}`, { ...init, headers });
  if (!res.ok) {
    let message = res.statusText;
    try {
      const body = await res.json();
      message = body.error || message;
    } catch {
      // response had no JSON body
    }
    throw new ApiError(res.status, message);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export async function demoApi<T>(path: string, demoToken = getDemoToken()): Promise<T> {
  return api<T>(path, {
    method: 'POST',
    headers: { 'X-Demo-Token': demoToken },
  }, '');
}

export async function chaos<T>(path: string, method = 'POST', demoToken = getDemoToken()): Promise<T> {
  const url = path.startsWith('/chaos') ? path : `/chaos${path}`;
  const res = await fetch(`${url}${url.includes('?') ? '&' : '?'}token=${encodeURIComponent(demoToken)}`, {
    method,
    headers: { 'X-Demo-Token': demoToken },
  });
  if (!res.ok) {
    let message = res.statusText;
    try {
      const body = await res.json();
      message = body.error || message;
    } catch {
      // no body
    }
    throw new ApiError(res.status, message);
  }
  return (await res.json()) as T;
}

export function downloadObject(bucket: string, key: string): Promise<Blob> {
  const encodedKey = key.split('/').map(encodeURIComponent).join('/');
  return fetch(`${API}/buckets/${encodeURIComponent(bucket)}/objects/${encodedKey}`, {
    headers: { Authorization: `Bearer ${getToken()}` },
  }).then(async (res) => {
    if (!res.ok) throw new ApiError(res.status, res.statusText);
    return res.blob();
  });
}

export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '0 B';
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let value = n / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value >= 10 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`;
}

export function formatTime(value: string | number): string {
  const date = typeof value === 'number' ? new Date(value * 1000) : new Date(value);
  if (Number.isNaN(date.getTime())) return '—';
  return date.toLocaleString(undefined, {
    month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
  });
}
