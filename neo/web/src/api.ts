import type {
  Account,
  AccountRole,
  Application,
  ApplyInput,
  Capacity,
  Catalog,
  GPUBooking,
  GPUOverview,
  Health,
  InstanceOwner,
  InstanceView,
  Invite,
  Sample,
  SessionDefaults,
  SessionInfo,
  SessionRow,
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

  /**
   * 用一次性凭证换会话。
   * device 是浏览器指纹折算的设备标签，只为了让管理员分清哪台是哪台。
   */
  /** 管理员用口令登录 —— 控制台只认这一个口令 */
  login: (password: string, device: string) =>
    request<{ user: SessionInfo['user']; session: SessionInfo['session'] }>('/api/auth/login', {
      method: 'POST',
      body: JSON.stringify({ password, device }),
    }),

  /** @deprecated 保留给后台接口；界面上不再摆成员管理 */
  redeem: (token: string, device: string) =>
    request<{ user: SessionInfo['user']; session: SessionInfo['session'] }>('/api/auth/redeem', {
      method: 'POST',
      body: JSON.stringify({ token, device }),
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

  // 成员与访问管理：只有管理员能调，后端会挡普通成员
  accounts: () => request<{ users: Account[]; defaults: SessionDefaults }>('/api/users'),

  createAccount: (payload: {
    name: string;
    role: AccountRole;
    note?: string;
    token_ttl?: string;
    max_idle?: string;
  }) => request<{ user: Account }>('/api/users', { method: 'POST', body: JSON.stringify(payload) }),

  updateAccount: (
    id: string,
    payload: Partial<{
      name: string;
      role: AccountRole;
      note: string;
      token_ttl: string;
      max_idle: string;
      status: 'active' | 'disabled';
      apply_now: boolean;
    }>,
  ) => request<{ user: Account; applied?: number }>(`/api/users/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    body: JSON.stringify(payload),
  }),

  deleteAccount: (id: string) =>
    request<{ status: string }>(`/api/users/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  revokeAccountSessions: (id: string) =>
    request<{ revoked: number }>(`/api/users/${encodeURIComponent(id)}/revoke-sessions`, {
      method: 'POST',
    }),

  invites: () => request<{ invites: Invite[]; valid: number }>('/api/invites'),

  createInvite: (userId: string, ttlSeconds: number) =>
    request<{ invite: Invite }>(`/api/users/${encodeURIComponent(userId)}/invite`, {
      method: 'POST',
      body: JSON.stringify({ ttl_seconds: ttlSeconds }),
    }),

  /**
   * 按名字签发一条访问链接。
   * 控制台不再摆成员管理，所以「让某人进来」就只是填个名字这么一下：
   * 同名复用同一条记录，不同名顺手建一个。
   */
  issueInvite: (name: string, ttlSeconds: number) =>
    request<{ invite: Invite; user: { id: string; name: string; role: string } }>('/api/invites', {
      method: 'POST',
      body: JSON.stringify({ name, ttl_seconds: ttlSeconds }),
    }),

  revokeInvite: (token: string) =>
    request<{ status: string }>(`/api/invites/${encodeURIComponent(token)}/revoke`, {
      method: 'POST',
    }),

  sessions: () =>
    request<{ sessions: SessionRow[]; online: number; total: number; current?: string }>(
      '/api/sessions',
    ),

  revokeSession: (id: string) =>
    request<{ status: string }>(`/api/sessions/${encodeURIComponent(id)}/revoke`, {
      method: 'POST',
    }),

  /** 彻底抹掉台账里的一条会话：不再留「已吊销」的记录，删了就是没了 */
  deleteSession: (id: string) =>
    request<{ status: string }>(`/api/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  // ------------------------------------------------------------ GPU 预约

  /** 排期总览：谁约了哪天、此刻还能批几张 */
  gpuBookings: () => request<GPUOverview>('/api/gpu-bookings'),

  applyGPU: (body: {
    instance: string;
    applicant: string;
    contact: string;
    purpose: string;
    date: string;
    card_count: number;
  }) => request<{ booking: GPUBooking; hint: string }>('/api/gpu-bookings', {
    method: 'POST',
    body: JSON.stringify(body),
  }),

  /** 批准或驳回。批准不等于挂载 —— 挂载是另一个动作 */
  decideGPU: (id: string, action: 'approve' | 'reject', reason: string) =>
    request<{ booking: GPUBooking }>(`/api/gpu-bookings/${encodeURIComponent(id)}/decision`, {
      method: 'POST',
      body: JSON.stringify({ action, reason }),
    }),

  /** 把卡挂到实例上 */
  attachGPU: (id: string) =>
    request<{ booking: GPUBooking; needs_restart: boolean; hint: string }>(
      `/api/gpu-bookings/${encodeURIComponent(id)}/attach`,
      { method: 'POST' },
    ),

  /** 摘卡 */
  detachGPU: (id: string) =>
    request<{ booking: GPUBooking; hint: string }>(
      `/api/gpu-bookings/${encodeURIComponent(id)}/detach`,
      { method: 'POST' },
    ),

  cancelGPU: (id: string) =>
    request<{ booking: GPUBooking }>(`/api/gpu-bookings/${encodeURIComponent(id)}/cancel`, {
      method: 'POST',
    }),

  /**
   * 彻底抹掉台账里的一条记录。
   * 与取消不同：取消留痕，这个连痕一起抹 —— 用于清掉测试与误填的假数据。
   * 卡还挂着时服务端会拒：先摘卡，再删。
   */
  deleteGPU: (id: string) =>
    request<{ booking: GPUBooking; hint: string }>(`/api/gpu-bookings/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),

  /**
   * 重启实例。
   * 这是唯一会打断实例上正在跑的事情的动作，所以它单独成一个按钮。
   */
  restartInstance: (name: string, force = false) =>
    request<{ status: string; instance: string }>(
      `/api/instances/${encodeURIComponent(name)}/restart`,
      { method: 'POST', body: JSON.stringify({ force }) },
    ),

  // ------------------------------------------------------------ 自己实例的操作

  /**
   * 开机 / 关机 / 重启。
   * 与上面的 restartInstance 是两回事：那个只有管理员能按（不带归属判断），
   * 这个登录就能到达，但服务端还会判「这台机器是不是你的」。
   */
  powerInstance: (name: string, action: 'start' | 'stop' | 'restart', force = false) =>
    request<{ status: string; instance: string; action: string; hint: string }>(
      `/api/instances/${encodeURIComponent(name)}/power`,
      { method: 'POST', body: JSON.stringify({ action, force }) },
    ),

  /** 重置实例的 root 登录口令。走容器内 chpasswd，不需要知道旧口令。 */
  resetPassword: (name: string, password: string) =>
    request<{ status: string; hint: string }>(
      `/api/instances/${encodeURIComponent(name)}/password`,
      { method: 'POST', body: JSON.stringify({ password }) },
    ),

  /**
   * 网页终端的 WebSocket 地址。
   *
   * 同源地址，浏览器会自动带上会话 cookie —— 服务端据此判断
   * 「你是谁、这台机器是不是你的」。这里不拼任何令牌参数：
   * 把凭证写进 URL 意味着它会出现在浏览记录、代理日志和 Referer 里。
   */
  shellSocket: (name: string) => {
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    return `${proto}//${window.location.host}/api/instances/${encodeURIComponent(name)}/shell`;
  },

  // ------------------------------------------------------------ 归属分配（管理员）

  /** 把一台实例分给某个成员。分配之后对方刷新即可使用终端、口令与电源。 */
  assignInstance: (name: string, owner: string) =>
    request<{ instance: string; owner: string; status: string; hint: string }>(
      `/api/instances/${encodeURIComponent(name)}/assign`,
      { method: 'POST', body: JSON.stringify({ owner }) },
    ),

  /** 解除归属：这台机器回到「未登记属主」，租户侧操作入口随之关闭。 */
  unassignInstance: (name: string) =>
    request<{ instance: string; status: string; hint: string }>(
      `/api/instances/${encodeURIComponent(name)}/assign`,
      { method: 'DELETE' },
    ),

  /** 全部分配记录，供管理员核对「谁手里有哪些机器」。 */
  instanceOwners: () => request<{ owners: InstanceOwner[] }>('/api/instance-owners'),
};
