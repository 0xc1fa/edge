import { useCallback, useEffect, useMemo, useState } from 'react';
import type { ReactNode } from 'react';
import { Button, Card, Col, Descriptions, Input, MessagePlugin, Row, Skeleton, Tag } from '../td';
import type { SkeletonProps } from 'tdesign-react/es/skeleton';
import { ApiError, api } from '../api';
import { humanTime } from '../format';
import { flavorSummary, gpuSummary, mibParts, VRAM_UNIT } from '../catalog';
import { statusTag, Logo, BRAND } from './ui';
import type { Application, Catalog } from '../types';

interface Props {
  onAdmin: () => void;
}

/**
 * 图标：手写 SVG。
 * 只要四个图形，为一个图标再引一个依赖不划算，也不受上游版本影响。
 */
const IconServer = () => (
  <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
    <rect x="3" y="4" width="18" height="7" rx="2" />
    <rect x="3" y="13" width="18" height="7" rx="2" />
    <path d="M7 7.5h.01M7 16.5h.01" />
  </svg>
);

const IconLayers = () => (
  <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
    <path d="M12 3 3 7.5 12 12l9-4.5L12 3Z" />
    <path d="m3 12.5 9 4.5 9-4.5" />
    <path d="m3 17 9 4.5 9-4.5" />
  </svg>
);

const IconChip = () => (
  <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
    <rect x="7" y="7" width="10" height="10" rx="2" />
    <path d="M10 3.5V7M14 3.5V7M10 17v3.5M14 17v3.5M3.5 10H7M3.5 14H7M17 10h3.5M17 14h3.5" />
  </svg>
);

const IconInfo = () => (
  <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
    <circle cx="12" cy="12" r="9" />
    <path d="M12 11v5M12 8h.01" />
  </svg>
);

/** 统计格的一项：上面大数字、下面小标签。 */
interface Stat {
  num: string | number;
  unit?: string;
  label: string;
}

/**
 * 「Web 服务、个人开发机、长期跑的小任务」这类场景串拆成标签。
 * .txt scene 是给表格用的，产品卡要的是可扫的三个词。
 */
function sceneTags(scene: string): string[] {
  return scene
    .split(/[、,，]/)
    .map((s) => s.trim())
    .filter(Boolean)
    .slice(0, 3);
}

/** 滚动到锚点：sticky 导航会遮住标题，偏移量交给 CSS 的 scroll-margin-top */
function jumpTo(selector: string) {
  document.querySelector(selector)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
}

/* 加载骨架：形状照着真实区块拼，避免数据到位时整页跳动 */
const HERO_SKELETON: SkeletonProps['rowCol'] = [
  { width: '88px', height: '22px' },
  { width: '420px', height: '34px' },
  { width: '520px', height: '14px' },
  { width: '248px', height: '32px' },
];

const CARD_SKELETON: SkeletonProps['rowCol'] = [
  [{ width: '36px', height: '36px', type: 'rect' }, { width: '132px', height: '20px', marginLeft: '12px' }],
  { width: '100%', height: '76px', type: 'rect' },
  { width: '100%', height: '14px' },
  { width: '100%', height: '32px' },
];

/**
 * 产品卡 —— 两类产品共用同一张卡型。
 *
 * 对齐云厂商的规格卡：图标 + 名称 + 右上角状态定身份；
 * 中间一格均分的「统计格」是这张卡的卖点，上面大数字、下面小标签，
 * 比「一行大字 + 属性表」更齐整——数字全在同一基线上，不会和下方打架。
 * 统计格下面是适用场景标签（把后端给的场景串摊开），最后一行辅助说明 + 通宽动作。
 */
function ProductCard({
  icon,
  title,
  status,
  stats,
  scene,
  info,
  off,
  action,
}: {
  icon: ReactNode;
  title: string;
  status: ReactNode;
  stats: Stat[];
  scene?: string;
  info: string;
  off?: boolean;
  action: ReactNode;
}) {
  const tags = scene ? sceneTags(scene) : [];

  return (
    <Card bordered hoverShadow={!off} className={off ? 'pc pc-off' : 'pc'}>
      <div className="pc-head">
        <span className="pc-icon">{icon}</span>
        <span className="pc-title">{title}</span>
        {status && <span className="pc-status">{status}</span>}
      </div>

      <div className="pc-stats">
        {stats.map((s) => (
          <div className="pc-stat" key={s.label}>
            <span className="pc-stat-val">
              <span className="pc-stat-num">{s.num}</span>
              {s.unit && <span className="pc-stat-unit">{s.unit}</span>}
            </span>
            <span className="pc-stat-label">{s.label}</span>
          </div>
        ))}
      </div>

      {tags.length > 0 && (
        <div className="pc-scene">
          {tags.map((t) => (
            <Tag key={t} theme="default" variant="light-outline" size="small">
              {t}
            </Tag>
          ))}
        </div>
      )}

      <p className="pc-info">
        <IconInfo />
        {info}
      </p>

      <div className="pc-foot">{action}</div>
    </Card>
  );
}

/** Hero 右侧的实测数字：口径都从目录算出来，不做写死的宣传值。 */
function HeroFact({ num, unit, label }: { num: number; unit: string; label: string }) {
  return (
    <div className="hero-fact">
      <span className="hero-fact-val">
        <span className="hero-fact-num">{num}</span>
        <span className="hero-fact-unit">{unit}</span>
      </span>
      <span className="hero-fact-label">{label}</span>
    </div>
  );
}

/**
 * 公开的产品页 —— 这一页没有任何访问控制，也只回答一个问题：有什么。
 *
 * 结构是「Hero + 两类产品，每类两款」：
 *   轻量云主机（2核4G / 4核8G）与 GPU 云服务器（RTX 4090 / RTX PRO 6000）
 *   各自一个分区，横向并排对比；选型与下单分开——点「立即申请」带着选型跳到 #/apply。
 *
 * 库存口径：页面写的是「库存」，缺货写「待上架」（不是「已被占用」）。
 */
export default function PublicSite({ onAdmin }: Props) {
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [scrolled, setScrolled] = useState(false);

  const [queryId, setQueryId] = useState('');
  const [queryResult, setQueryResult] = useState<Application | null>(null);
  const [queryError, setQueryError] = useState<string | null>(null);

  const loadCatalog = useCallback(() => {
    api
      .catalog()
      .then(setCatalog)
      .catch(() => setError('暂时取不到售卖目录，请稍后重试'));
  }, []);

  useEffect(loadCatalog, [loadCatalog]);

  // 滚动后才给导航加投影：静止时保持一条干净的边线，滚动后才有「浮起来」的层次
  useEffect(() => {
    const onScroll = () => setScrolled(window.scrollY > 4);
    onScroll();
    window.addEventListener('scroll', onScroll, { passive: true });
    return () => window.removeEventListener('scroll', onScroll);
  }, []);

  const flavors = catalog?.flavors ?? [];
  const gpus = catalog?.gpu_models ?? [];
  const images = catalog?.images ?? [];
  const image = images.find((i) => i.default && i.available) ?? images.find((i) => i.available);
  const firstAvailFlavor = flavors.find((f) => f.available)?.name ?? '';

  const availFlavors = flavors.filter((f) => f.available).length;
  const availGpus = gpus.filter((g) => g.available).length;
  const gpuStock = gpus.reduce((sum, g) => sum + (g.available ? g.available_cards : 0), 0);

  // 示例回执号跟着当天日期走：写死一个日期，隔天就成了「昨天的例子」
  const sampleReceiptId = useMemo(() => {
    const d = new Date();
    const p = (n: number) => String(n).padStart(2, '0');
    return `AP-${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-A1B2`;
  }, []);

  // 点「立即申请」＝带着选型换页，选型与下单本来就是两件事
  const gotoApply = (params: Record<string, string>) => {
    const search = new URLSearchParams(params).toString();
    window.location.hash = `#/apply?${search}`;
  };

  const specTextOf = (a: Application): string => {
    const f = flavors.find((x) => x.name === a.flavor);
    return f ? flavorSummary(f) : a.flavor;
  };

  const query = async (e: React.FormEvent) => {
    e.preventDefault();
    setQueryError(null);
    setQueryResult(null);
    try {
      const res = await api.application(queryId.trim());
      setQueryResult(res.application);
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : '查询失败';
      setQueryError(msg);
      MessagePlugin.error(msg);
    }
  };

  return (
    <div className="site">
      <header className={scrolled ? 'tc-nav is-scrolled' : 'tc-nav'}>
        <div className="tc-nav-inner">
          <div className="brand">
            <Logo animated />
            <span>{BRAND}</span>
          </div>
          <nav className="tc-nav-links">
            {/* 不用 <a href="#products">：hash 路由下它会把地址栏改成 #products，
                再点控制台就回不到 #/ 了。这里直接滚动到锚点。 */}
            <Button variant="text" className="t-nav-link" onClick={() => jumpTo('#products')}>
              产品
            </Button>
            <Button variant="outline" size="small" theme="primary" onClick={onAdmin}>
              控制台
            </Button>
          </nav>
        </div>
      </header>

      {error && (
        <div className="site-main">
          <div className="banner error">{error}</div>
        </div>
      )}

      {!catalog && !error && (
        <>
          <section className="hero">
            <div className="hero-inner">
              <Skeleton loading animation="gradient" rowCol={HERO_SKELETON} />
            </div>
          </section>
          <main className="site-main site-loading">
            <Row gutter={[20, 20]}>
              {[0, 1, 2, 3].map((i) => (
                <Col xs={12} sm={6} key={i}>
                  <Card bordered className="pc">
                    <Skeleton loading animation="gradient" rowCol={CARD_SKELETON} />
                  </Card>
                </Col>
              ))}
            </Row>
          </main>
        </>
      )}

      {catalog && (
        <>
          {/* ------------------------------------------------ 首屏 Hero */}
          <section className="hero">
            <div className="hero-inner">
              <div className="hero-copy">
                <Tag theme="primary" variant="light" size="small">
                  内网自助申领
                </Tag>
                <h1 className="hero-title">轻量云主机与 GPU 云服务器</h1>
                <p className="hero-sub">
                  选好机型提交申请，管理员核验用途与资源余量后，按你留的联系方式交付主机名、root
                  账号与随机口令，全程可凭回执号自助查进度。
                </p>
                <div className="hero-actions">
                  <Button theme="primary" onClick={() => jumpTo('#products')}>
                    查看全部产品
                  </Button>
                  <Button variant="outline" onClick={() => jumpTo('#track')}>
                    查询申请进度
                  </Button>
                </div>
              </div>
              <div className="hero-facts">
                <HeroFact num={availFlavors} unit="款" label="可选云主机规格" />
                <HeroFact num={availGpus} unit="款" label="可申领 GPU 型号" />
                <HeroFact num={gpuStock} unit="张" label="GPU 整卡现货" />
              </div>
            </div>
          </section>

          <main className="site-main" id="products">
            {/* -------------------------------------------- 轻量云主机 */}
            <section className="prod-sec">
              <div className="sec-head">
                <span className="sec-bar" />
                <h2 className="sec-title">轻量云主机</h2>
                <p className="sec-note">独享 vCPU 与内存，适合长期常驻的服务与个人开发机</p>
              </div>
              {/* 注意：TDesign 栅格是 12 列制，不是 24——xs=12 才是独占一行，
                  sm=6 是一行两张。写成 24 会让所有卡片通栏堆叠。 */}
              <Row gutter={[20, 20]} align="stretch">
                {flavors.map((f) => {
                  const mem = mibParts(f.memory_mib);
                  return (
                    <Col xs={12} sm={6} key={f.name}>
                      <ProductCard
                        icon={f.vcpu >= 4 ? <IconLayers /> : <IconServer />}
                        title={f.title}
                        status={
                          !f.available ? (
                            <Tag theme="default" variant="light" size="small">
                              {f.reason || '待上架'}
                            </Tag>
                          ) : null
                        }
                        stats={[
                          { num: f.vcpu, label: 'vCPU' },
                          { num: mem.value, unit: mem.unit, label: '内存' },
                          { num: f.disk_gb, unit: 'GiB', label: '系统盘' },
                        ]}
                        scene={f.scene}
                        info={image ? `默认镜像 ${image.title}` : '镜像由系统默认分配'}
                        off={!f.available}
                        action={
                          f.available ? (
                            <Button variant="outline" theme="primary" onClick={() => gotoApply({ flavor: f.name })}>
                              立即申请
                            </Button>
                          ) : (
                            <Button variant="outline" disabled>
                              暂不可申请
                            </Button>
                          )
                        }
                      />
                    </Col>
                  );
                })}
              </Row>
            </section>

            {/* -------------------------------------------- GPU 云服务器 */}
            <section className="prod-sec">
              <div className="sec-head">
                <span className="sec-bar" />
                <h2 className="sec-title">GPU 云服务器</h2>
                <p className="sec-note">整卡直通，需搭配一台轻量云主机一起申购</p>
              </div>
              <Row gutter={[20, 20]} align="stretch">
                {gpus.map((m) => (
                  <Col xs={12} sm={6} key={m.id}>
                    <ProductCard
                      icon={<IconChip />}
                      title={m.title}
                      status={
                        !m.available ? (
                          <Tag theme="default" variant="light" size="small">
                            {m.reason || '待上架'}
                          </Tag>
                        ) : null
                      }
                      // 缺货时第二格留位写「—」而不是整格消失：
                      // 同一行的两张卡统计格数量不一致，格宽就会差一倍，看着像两种东西。
                      stats={[
                        { num: m.vram_gb, unit: VRAM_UNIT, label: '显存' },
                        m.available
                          ? { num: m.total, unit: '张', label: '库存' }
                          : { num: '—', label: '库存' },
                      ]}
                      scene={m.scene}
                      info="需搭配轻量云主机一起申购"
                      off={!m.available}
                      action={
                        m.available ? (
                          <Button
                            variant="outline"
                            theme="primary"
                            onClick={() => gotoApply({ gpu: m.id, flavor: firstAvailFlavor, cards: '1' })}
                          >
                            立即申请
                          </Button>
                        ) : (
                          <Button variant="outline" disabled>
                            暂不可申请
                          </Button>
                        )
                      }
                    />
                  </Col>
                ))}
              </Row>
            </section>
          </main>
        </>
      )}

      {/* ------------------------------------------------ 底部：进度查询 */}
      <section className="track" id="track">
        <div className="track-inner">
          <div className="track-copy">
            <div className="sec-head">
              <span className="sec-bar" />
              <h2 className="sec-title">进度查询</h2>
            </div>
            <p className="track-desc">
              提交申请后会拿到一个回执号，用它可以在这里自助查看这台申请的处理状态，
              不必去问管理员要结果。
            </p>
            <p className="track-desc muted small">
              回执号形如 <span className="mono">{sampleReceiptId}</span>，提交成功那一页会完整显示。
            </p>
          </div>

          <Card bordered className="track-card">
            <form className="query" onSubmit={query}>
              <Input value={queryId} placeholder={`输入回执号，例如 ${sampleReceiptId}`} onChange={setQueryId} />
              <Button theme="primary" type="submit" disabled={queryId.trim() === ''}>
                查询
              </Button>
            </form>

            {queryError && <div className="banner error">{queryError}</div>}

            {queryResult && (
              <div className="track-result">
                <div className="track-result-head">
                  <span className="mono track-result-id">{queryResult.id}</span>
                  {statusTag(queryResult)}
                </div>
                {/* 与控制台走同一个 Descriptions：
                    手搓的那套 kv 网格在最后一行会少两格，看着参差。 */}
                <Descriptions column={2} size="small">
                  <Descriptions.DescriptionsItem label="主机名">
                    <span className="mono">{queryResult.hostname}</span>
                  </Descriptions.DescriptionsItem>
                  <Descriptions.DescriptionsItem label="规格">
                    {specTextOf(queryResult)}
                  </Descriptions.DescriptionsItem>
                  <Descriptions.DescriptionsItem label="GPU">
                    {gpuSummary(queryResult.gpu_model ?? '', queryResult.gpu_count, gpus)}
                  </Descriptions.DescriptionsItem>
                  <Descriptions.DescriptionsItem label="登录账号">
                    <span className="mono">{queryResult.login_user || 'root'}</span>
                  </Descriptions.DescriptionsItem>
                  <Descriptions.DescriptionsItem label="使用时长">
                    {queryResult.duration_days} 天
                  </Descriptions.DescriptionsItem>
                  <Descriptions.DescriptionsItem label="提交时间">
                    {humanTime(queryResult.created_at)}
                  </Descriptions.DescriptionsItem>
                  <Descriptions.DescriptionsItem label="状态更新">
                    {humanTime(queryResult.updated_at)}
                  </Descriptions.DescriptionsItem>
                </Descriptions>
                {queryResult.reason && <p className="track-reason">管理员意见：{queryResult.reason}</p>}
              </div>
            )}
          </Card>
        </div>
      </section>

      <footer className="site-footer">
        <div className="footer-inner">
          <span>{BRAND} · 内网自服务平台</span>
          <span>资源核验与开通由管理员统一处理</span>
        </div>
      </footer>
    </div>
  );
}
