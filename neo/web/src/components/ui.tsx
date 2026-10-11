import { Tag } from '../td';
import { humanPercent } from '../format';
import { requestStatus } from '../catalog';
import type { Application } from '../types';
import logoUrl from '../../../logo/logo.svg';
import logoAnimatedUrl from '../../../logo/logo-animated.svg';

/** 品牌名只在这里出现一次：改名时改这一行，页面其它地方一律引用它。 */
export const BRAND = '日新坊';

/** 品牌核心标识：白底圆角方块里一个蓝色小方块（源文件见 /root/edge/neo/logo）。
 *  animated 使用 logo-animated.svg，蓝色块内嵌 SMIL 旋转：长静止→快转→慢转→回正，无飞出。 */
export function Logo({ animated = false }: { animated?: boolean }) {
  return <img className="brand-mark" src={animated ? logoAnimatedUrl : logoUrl} alt={BRAND} />;
}

export function StatusBadge({ running, label }: { running: boolean; label: string }) {
  return (
    <Tag theme={running ? 'success' : 'default'} variant="light" size="small">
      {label}
    </Tag>
  );
}

/** 类型是「分类」不是「状态」：压成中性灰，别和状态标签抢注意力。 */
export function TypeBadge({ label }: { label: string }) {
  return (
    <Tag theme="default" variant="light" size="small">
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
      {/* 颜色由 .spark-line 走 --accent，避免在这里硬编码第二套品牌色；
          非等比拉伸会把描边拉变形，所以按屏幕像素归一。 */}
      <polyline
        className="spark-line"
        points={points}
        fill="none"
        strokeWidth="1.6"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  );
}

// 说明：概览统计与详情 kv 分别改用 TDesign 的 Statistic / Descriptions，
// 这里不再自己画卡片和键值对网格。
