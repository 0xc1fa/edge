import type {
  Application,
  ApplyInput,
  Capacity,
  Catalog,
  Health,
  InstanceView,
  Sample,
  SessionInfo,
} from './types';

/**
 * 薄薄一层 fetch 封装。这里刻意不引入数据请求库：
 * 页面只有几个端点，多一点糖原等于多一点升级负担。
 */

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
    this.name = 'ApiError';
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    credentials: 'same-origin',
    headers: {
      'Content-Type': 'application/json',
      ...(init?.headers ?? {}),
    },
  });

  if (!res.ok) {
    let msg = `请求失败 (${res.status})`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body?.error) msg = body.error;
    } catch {
      // 响应不是 JSON 时保留默认提示
    }
    throw new ApiError(res.status, msg);
  }
  return (await res.json()) as T;
}

export const api = {
  health: () => request<Health>('/api/health'),

  session: () => request<SessionInfo>('/api/session'),

  login: (password: string) =>
    request<{ role: string; ttl: number }>('/api/auth/login', {
      method: 'POST',
      body: JSON.stringify({ password }),
    }),

  logout: () => request<{ status: string }>('/api/auth/logout', { method: 'POST' }),

  instances: () => request<{ instances: InstanceView[] }>('/api/instances'),

  instance: (name: string) =>
    request<{ instance: InstanceView; series: Sample[] }>(`/api/instances/${encodeURIComponent(name)}`),

  capacity: () => request<Capacity>('/api/capacity'),

  // 公开区：产品页未登录也能读目录、提交申请
  catalog: () => request<Catalog>('/api/catalog'),

  apply: (payload: ApplyInput) =>
    request<{ application: Application; hint: string }>('/api/applications', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),

  application: (id: string) =>
    request<{ application: Application }>(`/api/applications/${encodeURIComponent(id)}`),

  // 管理员区
  applications: (status = '') =>
    request<{ applications: Application[]; pending: number }>(
      `/api/applications${status ? `?status=${status}` : ''}`,
    ),

  decide: (id: string, action: 'approve' | 'reject', reason: string) =>
    request<{ application: Application; command: string }>(
      `/api/applications/${encodeURIComponent(id)}/decision`,
      { method: 'POST', body: JSON.stringify({ action, reason }) },
    ),
};
