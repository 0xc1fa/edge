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
  // 整天时不带「0 小时」：列表里一排「19 天 0 小时」只多不清晰
  if (d > 0) return h > 0 ? `${d} 天 ${h} 小时` : `${d} 天`;
  if (h > 0) return `${h} 小时 ${m} 分`;
  return `${m} 分钟`;
}

const pad2 = (n: number) => String(n).padStart(2, '0');

/**
 * 时间统一输出 `YYYY-MM-DD HH:mm:ss`。
 * 不用 toLocaleString：它随运行环境的 locale 变格式，
 * 而控制台里同一天的时间必须长得一样。
 */
export function humanTime(iso: string): string {
  if (!iso) return '-';
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return '-';
  return (
    `${t.getFullYear()}-${pad2(t.getMonth() + 1)}-${pad2(t.getDate())} ` +
    `${pad2(t.getHours())}:${pad2(t.getMinutes())}:${pad2(t.getSeconds())}`
  );
}

export function humanPercent(v: number): string {
  return `${v.toFixed(0)}%`;
}

/**
 * 把秒数说成「还剩多久」。会话台账与凭证列表都要显示剩余寿命，
 * 统一在这里编人类的说法，避免一处写成「3 天 5 小时」另一处写成「77:05:00」。
 */
export function humanDuration(sec: number): string {
  if (!Number.isFinite(sec) || sec <= 0) return '已到期';
  const min = Math.floor(sec / 60);
  const hour = Math.floor(min / 60);
  const day = Math.floor(hour / 24);
  if (day > 0) return hour % 24 > 0 ? `${day} 天 ${hour % 24} 小时` : `${day} 天`;
  if (hour > 0) return min % 60 > 0 ? `${hour} 小时 ${min % 60} 分` : `${hour} 小时`;
  return `${Math.max(1, min)} 分钟`;
}

/** 相对现在的「多久之前」，用于最后活跃时间这种需要一眼判断新旧的场合。 */
export function humanAgo(iso: string): string {
  if (!iso) return '-';
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return '-';
  return `${humanDuration((Date.now() - t) / 1000)}前`;
}
