import { Tag } from '../td';
import { humanPercent } from '../format';
import { requestStatus } from '../catalog';
import type { Application } from '../types';
import logoUrl from '../../../logo/logo.svg';
import logoAnimatedUrl from '../../../logo/logo-animated.svg';

/** 品牌核心标识：白底圆角方块里一个蓝色小方块（源文件见 /root/edge/neo/logo）。
 *  animated 使用 logo-animated.svg，蓝色块内嵌 SMIL 旋转：长静止→快转→慢转→回正，无飞出。 */
export function Logo({ animated = false }: { animated?: boolean }) {
  return <img className="brand-mark" src={animated ? logoAnimatedUrl : logoUrl} alt="日新坊" />;
}

export function StatusBadge({ running, label }: { running: boolean; label: string }) {
  return (
    <Tag theme={running ? 'success' : 'default'} variant="light" size="small">
      {label}
    </Tag>
  );
}

export function TypeBadge({ label }: { label: string }) {
  return (
    <Tag theme={label === '虚拟机' ? 'primary' : 'warning'} variant="light-outline" size="small">
      {label}
    </Tag>
  );
}

/** 台账状态 → 语义色。颜色只在这里映射一次，避免各处自己挑。 */
export function statusTag(a: Application) {
  const st = requestStatus(a);
  const theme =
    st.cls === 'ok' ? 'success' : st.cls === 'warn' ? 'warning' : st.cls === 'bad' ? 'danger' : 'default';
  return (
    <Tag theme={theme} variant="light" size="small">
      {st.text}
    </Tag>
  );
}

/** UsageBar 展示已用/总量占比，超过 85% 转成告警色，避免等到满了才发现。 */
export function UsageBar({ percent }: { percent: number }) {
  const level = percent >= 85 ? 'high' : percent >= 60 ? 'mid' : 'low';
  return (
    <div className="bar" title={humanPercent(percent)}>
      <div className={`bar-fill ${level}`} style={{ width: `${Math.min(100, Math.max(0, percent))}%` }} />
    </div>
  );
}

/**
 * Sparkline 用 SVG 画迷你走势。
 * 不引图表库：这里只需要一条折线，换一个依赖就把产物体积翻倍。
 */
export function Sparkline({ values, height = 48 }: { values: number[]; height?: number }) {
  if (values.length < 2) {
    return <div className="spark empty">采样中…</div>;
  }
  const width = 240;
  const max = Math.max(...values, 1);
  const step = width / (values.length - 1);
  const points = values
    .map((v, i) => `${(i * step).toFixed(1)},${(height - (v / max) * (height - 4) - 2).toFixed(1)}`)
    .join(' ');

  return (
    <svg className="spark" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none">
      <polyline points={points} fill="none" stroke="#5ac8fa" strokeWidth="1.6" />
    </svg>
  );
}

// 说明：概览统计与详情 kv 分别改用 TDesign 的 Statistic / Descriptions，
// 这里不再自己画卡片和键值对网格。
