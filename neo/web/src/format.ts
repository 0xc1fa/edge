/** 展示层格式化工具。字节用二进制单位，与 Incus 的配额写法保持一致。 */

export function humanBytes(n: number, digits = 1): string {
  if (!n || n <= 0) return '0';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return `${v.toFixed(i === 0 ? 0 : digits)} ${units[i]}`;
}

/**
 * 字节拆成「数值 + 单位」，阈值与 humanBytes 一致。
 * 用于 TDesign Statistic：它只收数字，单位要另外给 unit 属性。
 */
export function byteParts(n: number, digits = 1): { value: number; unit: string } {
  if (!n || n <= 0) return { value: 0, unit: 'B' };
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return { value: Number(v.toFixed(i === 0 ? 0 : digits)), unit: units[i] };
}

export function humanUptime(sec: number): string {
  if (!sec || sec <= 0) return '-';
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return `${d} 天 ${h} 小时`;
  if (h > 0) return `${h} 小时 ${m} 分`;
  return `${m} 分钟`;
}

export function humanTime(iso: string): string {
  if (!iso) return '-';
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return iso;
  return t.toLocaleString('zh-CN', { hour12: false });
}

export function humanPercent(v: number): string {
  return `${v.toFixed(0)}%`;
}
