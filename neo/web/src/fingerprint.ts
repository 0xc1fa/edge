/**
 * 浏览器指纹折算成的设备标签。
 *
 * 它只有一个用途：在会话台账上给设备起个短名字，
 * 让管理员能分清「张三的 MacBook」和「张三的手机」，进而只踢掉其中一台。
 *
 * 为什么它不参与身份判定，三点缺一不可：
 *   1. 会漂移 —— 浏览器升级、装个扩展、外接显示器，标签就变了，人会「被换台」；
 *   2. 会撞车 —— 同型号机器配同版本浏览器，指纹几乎一样，撞上了就分不清人；
 *   3. 可伪造 —— 这串值由前端提交，服务端无从判断真伪。
 *      把它当凭据，等于把钥匙放在门口。
 *
 * 所以这里算出来的东西只会被当作一个待展示的字符串，
 * 服务端存起来给管理员看，绝不用来放行任何请求。
 */

/** canvas 渲染差异：同版本浏览器在同一台机器上通常稳定，是标签里最有区分度的一项。 */
function canvasSignal(): string {
  try {
    const canvas = document.createElement('canvas');
    const ctx = canvas.getContext('2d');
    if (!ctx) return 'no-canvas';
    ctx.textBaseline = 'alphabetic';
    ctx.font = '14px system-ui, sans-serif';
    ctx.fillStyle = '#0052d9';
    ctx.fillText('日新算力 neo 0123456789', 4, 20);
    ctx.globalCompositeOperation = 'multiply';
    ctx.fillStyle = '#e37318';
    ctx.beginPath();
    ctx.arc(40, 30, 22, 0, Math.PI * 2, true);
    ctx.fill();
    const data = canvas.toDataURL();
    return data.slice(-64);
  } catch {
    // 隐私模式或 canvas 被禁用时不能让登录失败：标签少一项照样能生成
    return 'no-canvas';
  }
}

/** FNV-1a：够短、够快、没有依赖。这里不需要抗碰撞强度，只需要「同一台机器稳定」。 */
function fnv1a(input: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < input.length; i++) {
    h ^= input.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

export function deviceTag(): string {
  const parts = [
    navigator.platform,
    navigator.language,
    `${screen.width}x${screen.height}x${screen.colorDepth}`,
    `tz${new Date().getTimezoneOffset()}`,
    `cpu${navigator.hardwareConcurrency ?? 0}`,
    navigator.userAgent.slice(0, 80),
    canvasSignal(),
  ];
  return fnv1a(parts.join('|')).toString(36).padStart(7, '0').slice(0, 8);
}
