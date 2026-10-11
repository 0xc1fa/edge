import type { Application, CatalogImage, Flavor, GPUModel } from './types';

/** 把 MiB 显示成 GiB：云厂商的规格表都这么写，比原样输出 MiB 更好读数。 */
export function humanMiB(mib: number): string {
  if (mib >= 1024) {
    const gib = mib / 1024;
    return `${Number.isInteger(gib) ? gib : gib.toFixed(1)}GiB`;
  }
  return `${mib}MiB`;
}

// 云主机规格不含 GPU：显卡是独立型号（见 GPUModel），口径不能混在一起。
export function flavorSummary(f: Flavor): string {
  return `${f.vcpu} vCPU · ${humanMiB(f.memory_mib)} · ${f.disk_gb}GB`;
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
