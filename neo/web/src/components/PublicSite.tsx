import { useCallback, useEffect, useState } from 'react';
import type { ReactNode } from 'react';
import { Button, Card, Col, Input, MessagePlugin, Row, Tag } from '../td';
import { ApiError, api } from '../api';
import { humanTime } from '../format';
import { statusTag, Logo } from './ui';
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
 * 产品卡 —— 两类产品共用同一张卡型。
 *
 * 对齐云厂商的规格卡：图标 + 名称 + 右上角状态定身份；
 * 中间一格三项的「统计格」是这张卡的卖点，上面大数字、下面小标签，
 * 比「一行大字 + 属性表」更齐整——数字全在同一基线上，不会和下方打架。
 * 底部一行信息说明 + 一个通宽动作，两类产品结构完全一致。
 */
function ProductCard({
  icon,
  title,
  status,
  stats,
  info,
  off,
  action,
}: {
  icon: ReactNode;
  title: string;
  status: ReactNode;
  stats: Stat[];
  info: string;
  off?: boolean;
  action: ReactNode;
}) {
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

      <p className="pc-info">
        <IconInfo />
        {info}
      </p>

      <div className="pc-foot">{action}</div>
    </Card>
  );
}

/**
 * 公开的产品页 —— 这一页没有任何访问控制，也只回答一个问题：有什么。
 *
 * 结构是「两类产品，每类两款」：
 *   轻量云主机（2核4G / 4核8G）与 GPU 云服务器（RTX 4090 / RTX PRO 6000）
 *   各自一个分区，横向并排对比；选型与下单分开——点「立即申请」带着选型跳到 #/apply。
 *
 * 库存口径：页面写的是「库存」，缺货写「待上架」（不是「已被占用」）。
 */
export default function PublicSite({ onAdmin }: Props) {
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [error, setError] = useState<string | null>(null);

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

  const flavors = catalog?.flavors ?? [];
  const gpus = catalog?.gpu_models ?? [];
  const images = catalog?.images ?? [];
  const image = images.find((i) => i.default && i.available) ?? images.find((i) => i.available);
  const firstAvailFlavor = flavors.find((f) => f.available)?.name ?? '';

  // 点「立即申请」＝带着选型换页，选型与下单本来就是两件事
  const gotoApply = (params: Record<string, string>) => {
    const search = new URLSearchParams(params).toString();
    window.location.hash = `#/apply?${search}`;
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
      <header className="tc-nav">
        <div className="tc-nav-inner">
          <div className="brand">
            <Logo animated />
            <span>日新坊</span>
          </div>
          <nav className="tc-nav-links">
            <a href="#products">产品</a>
            <Button variant="text" onClick={onAdmin}>
              控制台
            </Button>
          </nav>
        </div>
      </header>

      <main className="site-main" id="products">
        {error && <div className="banner error">{error}</div>}

        {!catalog && !error && (
          <div className="site-loading">
            <Logo animated />
            <p className="muted">加载产品目录…</p>
          </div>
        )}

        {catalog && (
          <>
            {/* ---------------------------------------------- 轻量云主机 */}
            <section className="prod-sec">
              <h2 className="sec-title">轻量云主机</h2>
              <Row gutter={[20, 20]} align="stretch">
                {flavors.map((f) => (
                  <Col xs={12} md={6} key={f.name}>
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
                        { num: f.memory_mib / 1024, unit: 'GB', label: '内存' },
                        { num: f.disk_gb, unit: 'GB', label: '系统盘' },
                      ]}
                      info={`镜像 ${image?.title ?? 'Ubuntu 22.04 LTS'}`}
                      off={!f.available}
                      action={
                        f.available ? (
                          <Button theme="primary" onClick={() => gotoApply({ flavor: f.name })}>
                            立即申请
                          </Button>
                        ) : (
                          <Button disabled>暂不可申请</Button>
                        )
                      }
                    />
                  </Col>
                ))}
              </Row>
            </section>

            {/* ---------------------------------------------- GPU 云服务器 */}
            <section className="prod-sec">
              <h2 className="sec-title">GPU 云服务器</h2>
              <Row gutter={[20, 20]} align="stretch">
                {gpus.map((m) => (
                  <Col xs={12} md={6} key={m.id}>
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
                      stats={[
                        { num: m.vram_gb, unit: 'GB', label: '显存' },
                        { num: m.total, unit: '张', label: '库存' },
                      ]}
                      info="需搭配轻量云主机一起申购"
                      off={!m.available}
                      action={
                        m.available ? (
                          <Button
                            theme="primary"
                            onClick={() => gotoApply({ gpu: m.id, flavor: firstAvailFlavor, cards: '1' })}
                          >
                            立即申请
                          </Button>
                        ) : (
                          <Button disabled>暂不可申请</Button>
                        )
                      }
                    />
                  </Col>
                ))}
              </Row>
            </section>
          </>
        )}
      </main>

      {/* ------------------------------------------------ 底部：进度查询 */}
      <section className="track" id="track">
        <div className="track-inner">
          <h2 className="sec-title">进度查询</h2>
          <Card bordered className="track-card">
            <form className="query" onSubmit={query}>
              <Input
                value={queryId}
                placeholder="输入回执号，例如 AP-20261010-7XK4"
                onChange={setQueryId}
              />
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
                <div className="track-grid">
                  <div className="track-kv">
                    <span className="track-k">主机名</span>
                    <span className="mono track-v">{queryResult.hostname}</span>
                  </div>
                  <div className="track-kv">
                    <span className="track-k">GPU</span>
                    <span className="track-v">
                      {queryResult.gpu_count > 0
                        ? `${queryResult.gpu_count} 张 ${queryResult.gpu_model || '显卡'}`
                        : '不配显卡'}
                    </span>
                  </div>
                  <div className="track-kv">
                    <span className="track-k">登录账号</span>
                    <span className="mono track-v">{queryResult.login_user || 'root'}</span>
                  </div>
                  <div className="track-kv">
                    <span className="track-k">提交时间</span>
                    <span className="track-v">{humanTime(queryResult.created_at)}</span>
                  </div>
                </div>
                {queryResult.reason && <p className="track-reason">管理员意见：{queryResult.reason}</p>}
              </div>
            )}
          </Card>
        </div>
      </section>

      <footer className="site-footer">
        <div className="footer-inner">
          <span>日新坊 · 内网自服务平台</span>
        </div>
      </footer>
    </div>
  );
}
