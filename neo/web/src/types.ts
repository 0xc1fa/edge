export interface Nic {
  name: string;
  state: string;
  type: string;
  hwaddr: string;
  ipv4: string;
  ipv6: string;
  rx_bytes: number;
  tx_bytes: number;
}

export interface GPUDevice {
  device: string;
  type: string;
  pci: string;
  product: string;
}

export interface Ingress {
  device: string;
  listen: string;
  port: number;
  connect: string;
  purpose: string;
}

/** 实例视图，字段与后端 View 结构体一一对应。 */
export interface InstanceView {
  name: string;
  type: string;
  type_label: string;
  status: string;
  status_label: string;
  running: boolean;
  architecture: string;
  image_os: string;
  image_release: string;
  image_desc: string;
  created_at: string;
  started_at: string;
  uptime_seconds: number;
  quota_cpu: string;
  quota_memory: string;
  quota_disk: string;
  disk_used: number;
  disk_total: number;
  disk_percent: number;
  pid: number;
  processes: number;
  cpu_seconds: number;
  /** 当前 CPU 占用百分比（由对账层用两次采样差算出，单看累计值算不出来） */
  cpu_percent: number;
  mem_usage: number;
  mem_peak: number;
  mem_total: number;
  mem_percent: number;
  swap_usage: number;
  profiles: string[];
  nics: Nic[];
  gpus: GPUDevice[];
  ingress: Ingress[];
}

export interface Sample {
  ts: number;
  cpu_percent: number;
  mem_usage: number;
}

export interface Health {
  ok: boolean;
  version: string;
  incus_reachable: boolean;
  instance_count: number;
  last_reconcile_age_ms: number;
  error?: string;
}

export interface Capacity {
  instances: number;
  running: number;
  cpu: { total: number; allocated: number };
  memory: { total: number; used: number; allocated: number };
  disk: { allocated: number };
}

/** 会话信息。未登录也返回：前端靠它决定要不要显示登录页。 */
export interface SessionInfo {
  authenticated: boolean;
  role: string;
  /** 会话还有效但已不可用时，服务端给出的原因（如「空闲超时」） */
  reason?: string;
  user?: AccountBrief;
  session?: SessionBrief;
  defaults: SessionDefaults;
}

export interface AccountBrief {
  id: string;
  name: string;
  role: AccountRole;
}

export interface SessionBrief {
  id: string;
  created_at: string;
  last_seen_at: string;
  expires_at: string;
  max_idle: string;
  remain_seconds: number;
  device: string;
}

export interface SessionDefaults {
  /** 已是人读的说法：长期有效时是「长期」，不是「0s」 */
  token_ttl: string;
  max_idle: string;
}

export type AccountRole = 'admin' | 'member';

/** 成员。members 没有任何凭据字段：登录靠一次性凭证。 */
export interface Account {
  id: string;
  name: string;
  role: AccountRole;
  status: 'active' | 'disabled';
  note: string;
  token_ttl: string;
  max_idle: string;
  /** 跟上两个字段的区别：这是把全局默认值算进去之后真正生效的时长 */
  effective_token_ttl: string;
  effective_max_idle: string;
  created_at: string;
  last_login_at: string;
  sessions: number;
  online: number;
  pending_invites: number;
}

/** 一次性登录凭证。失去兑换资格之后服务端只回前 6 位（masked）。 */
export interface Invite {
  token: string;
  masked: boolean;
  user_id: string;
  user_name: string;
  created_by: string;
  created_at: string;
  expires_at: string;
  remain_seconds: number;
  status: 'valid' | 'used' | 'revoked' | 'expired';
}

/** 会话台账里的一行。online 由心跳算出来，不是「cookie 还没过期」。 */
export interface SessionRow {
  id: string;
  user_id: string;
  user_name: string;
  role: string;
  ip: string;
  user_agent: string;
  device: string;
  created_at: string;
  last_seen_at: string;
  expires_at: string;
  max_idle: string;
  online: boolean;
  expired: boolean;
  idle_seconds: number;
  /** 剩余秒数；**-1 表示长期有效**，0 表示此刻已到期 */
  remain_seconds: number;
}

/** 标准实例规格。available 由服务端按当前宿主余量实时算出。 */
export interface Flavor {
  name: string;
  title: string;
  vcpu: number;
  memory_mib: number;
  disk_gb: number;
  scene: string;
  available: boolean;
  reason?: string;
}

/** GPU 型号。产品页只展示型号，申购张数在申请表单里选。 */
export interface GPUModel {
  id: string;
  title: string;
  vram_gb: number;
  scene: string;
  desc: string;
  /** 本机该型号的插槽数；0 表示这台机器没装这个型号 */
  total: number;
  requires_flavor: boolean;
  available: boolean;
  /** 当前最多还能配几张（服务端算好，页面不自己推导） */
  available_cards: number;
  reason?: string;
}

export interface CatalogImage {
  id: string;
  alias: string;
  title: string;
  desc: string;
  available: boolean;
  /** 是否作为申请单的默认镜像；不填镜像即用它 */
  default?: boolean;
}

export interface Catalog {
  flavors: Flavor[];
  gpu_models: GPUModel[];
  images: CatalogImage[];
}

export interface ApplyInput {
  /** 留空则由服务端自动生成（vm-xxxxx） */
  hostname?: string;
  flavor: string;
  /** 留空则用默认镜像 */
  image?: string;
  /** 显卡型号（来自 gpu_models）；不配卡时留空 */
  gpu_model?: string;
  /** 配显卡张数；0 表示不配卡。GPU 只能随云主机一起申购。 */
  gpu_count: number;
  /** 留空则用开通时下发的随机口令登录 root */
  ssh_public_key?: string;
  applicant: string;
  contact: string;
  purpose: string;
  duration_days: number;
}

/** 申请单。管理员视角能看到完整联系方式与公钥，申请人查询时服务端已脱敏。 */
/** GPU 预约：某台机器在某一天的一段固定时段里用卡。 */
export interface GPUBooking {
  id: string;
  instance: string;
  applicant: string;
  contact: string;
  purpose: string;
  /** YYYY-MM-DD */
  date: string;
  card_count: number;
  /** 待处理 → 已批准 → 使用中 → 已结束；驳回与取消是另外两个终点 */
  status: 'pending' | 'approved' | 'rejected' | 'active' | 'ended' | 'cancelled';
  reason: string;
  created_at: string;
  updated_at: string;
  decided_at: string;
  decided_by: string;
  attached_at: string;
  detached_at: string;
  /** 挂载时写进实例的设备名；摘卡按它摘，不能靠重新找一遍 */
  device: string;
  window_start: string;
  window_end: string;
  /** 距离时段结束还有多少秒；未挂载或已结束时为 0 */
  remain_seconds: number;
  ended: boolean;
}

/** GPU 排期总览：台账 + 此刻还能批几张 + 时段口径。 */
export interface GPUOverview {
  bookings: GPUBooking[];
  pending: number;
  available: number;
  total: number;
  window: string;
  today: string;
  can_manage: boolean;
  /** 服务端是否配置了显卡地址；没配置时挂载不可用 */
  cards_visible: boolean;
}

export interface Application {
  id: string;
  hostname: string;
  flavor: string;
  image: string;
  gpu_model?: string;
  gpu_count: number;
  /** 登录账户，第一版恒为 root；留空口令由系统随机生成 */
  login_user: string;
  applicant: string;
  contact: string;
  ssh_public_key: string;
  purpose: string;
  duration_days: number;
  status: 'pending' | 'approved' | 'rejected';
  reason: string;
  created_at: string;
  updated_at: string;
}
