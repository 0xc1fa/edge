import type { Application, CatalogImage, Flavor, GPUModel } from './types';

/**
 * 把 MiB 拆成「数值 + 单位」。
 * 全站内存/磁盘口径都收敛到这里：要么 GiB、要么 MiB，不再出现一半 GB 一半 GiB。
 * 数值部分用于 TDesign Statistic 与产品卡的统计格，它们只收数字。
 */
export function mibParts(mib: number): { value: number; unit: string } {
  if (mib >= 1024) {
    const gib = mib / 1024;
    return { value: Number.isInteger(gib) ? gib : Number(gib.toFixed(1)), unit: 'GiB' };
  }
  return { value: mib, unit: 'MiB' };
}

/** 把 MiB 显示成一行可读文本：云厂商的规格表都这么写，比原样输出 MiB 更好读数。 */
export function humanMiB(mib: number): string {
  const p = mibParts(mib);
  return `${p.value}${p.unit}`;
}

// 云主机规格不含 GPU：显卡是独立型号（见 GPUModel），口径不能混在一起。
// 单位口径：内存/磁盘走二进制（Linux 系统口径），显存走十进制（显卡厂商口径）。
export function flavorSummary(f: Flavor): string {
  return `${f.vcpu} vCPU · ${humanMiB(f.memory_mib)} · ${f.disk_gb}GiB`;
}

/**
 * 显存按显卡厂商习惯写 GB（如 4090 的 24GB GDDR6X），
 * 与内存/磁盘的 GiB 不同——这两类单位没有换算关系，各按行业习惯更不容易被读错。
 * 产品卡把数字与单位拆成两档字号，所以单位单独导出，避免两处各写一遍。
 */
export const VRAM_UNIT = 'GB';

export function vramText(gb: number): string {
  return `${gb} ${VRAM_UNIT}`;
}

/** 管理员表格里的 GPU 一列：型号 + 张数，型号查不到时退回 ID。 */
export function gpuSummary(id: string, cards: number, models: GPUModel[]): string {
  if (!cards) return '不配显卡';
  const title = models.find((m) => m.id === id)?.title ?? id;
  return `${title} × ${cards}`;
}

export function imageTitle(images: CatalogImage[], id: string): string {
  return images.find((i) => i.id === id)?.title ?? id;
}

export function requestStatus(a: Application): { text: string; cls: string } {
  switch (a.status) {
    case 'pending':
      return { text: '待处理', cls: 'warn' };
    case 'approved':
      return { text: '已批准', cls: 'ok' };
    case 'rejected':
      return { text: '已驳回', cls: 'bad' };
    default:
      return { text: a.status, cls: 'off' };
  }
}
