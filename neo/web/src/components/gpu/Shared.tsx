import type { GPUBooking } from '../../types';

/**
 * 成员视图与管理员视图共用的原子件。
 *
 * 拆开展示之后最怕的不是代码重复，而是**口径漂移**：同一种状态在两边
 * 被写成不同的字、不同的颜色，使用者就会以为那是两种不同的东西。
 * 所以状态字典、时刻格式、倒计时算法只留这一份。
 */

export const STATUS: Record<
  GPUBooking['status'],
  { label: string; theme: 'warning' | 'success' | 'danger' | 'primary' | 'default' }
> = {
  pending: { label: '待批准', theme: 'warning' },
  approved: { label: '已批准', theme: 'primary' },
  rejected: { label: '已驳回', theme: 'danger' },
  active: { label: '使用中', theme: 'success' },
  ended: { label: '已摘卡', theme: 'default' },
  cancelled: { label: '已取消', theme: 'default' },
};

/** 距离时段结束还有多少秒。按服务端给的结束时刻算，不用本地推算几点 */
export const remainOf = (end: string) => {
  const t = new Date(end).getTime();
  if (!t) return 0;
  return Math.max(0, Math.floor((t - Date.now()) / 1000));
};

/** 倒计时：把秒数说成人读的话 */
export const countdown = (sec: number) => {
  if (sec <= 0) return '已到点，即将自动摘卡';
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (h > 0) return `${h} 小时 ${m} 分后自动摘卡`;
  return `${m} 分钟后自动摘卡`;
};

/** 调度时刻。年份在这里是噪音：看这页的人要的是「哪天几点」 */
export const stamp = (iso: string) => {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const p = (n: number) => String(n).padStart(2, '0');
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
};
