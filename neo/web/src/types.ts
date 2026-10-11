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

export interface SessionInfo {
  authenticated: boolean;
  role: string;
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
