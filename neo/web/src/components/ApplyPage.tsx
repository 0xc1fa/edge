import { useCallback, useEffect, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Col,
  Descriptions,
  Divider,
  Form,
  Input,
  MessagePlugin,
  Radio,
  Row,
  Steps,
  Tag,
  Textarea,
} from '../td';
import { ApiError, api } from '../api';
import { humanMiB } from '../catalog';
import { Logo } from './ui';
import type { Application, Catalog } from '../types';

interface Props {
  /** 从产品页带过来的选型：`#/apply?flavor=…&gpu=…&cards=1` */
  query: { flavor: string; gpu: string; cards: number };
  onBack: () => void;
}

/**
 * 申请页 —— 独立成页，不再塞在产品页底部。
 *
 * 「选型」和「下单」本来就是两件事：产品页只负责让人看懂有什么，
 * 点「立即申请」之后带着选型跳到这里，表单才能占满整屏、
 * 也才配得上一个明确的提交动作。
 *
 * 表单仍然只有四项：主机名服务端随机生成、镜像取默认、登录用 root + 随机口令。
 * 布局与表单控件全部走 TDesign：栅格、卡片、步骤条、表单标签、描述列表。
 */
export default function ApplyPage({ query, onBack }: Props) {
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [step, setStep] = useState(0); // 0=选择产品 1=申请信息 2=提交申请
  const [flavor, setFlavor] = useState(query.flavor);
  const [gpuId, setGpuId] = useState(query.gpu);
  const [cards, setCards] = useState(query.cards);

  const [applicant, setApplicant] = useState('');
  const [contact, setContact] = useState('');
  const [purpose, setPurpose] = useState('');
  const [days, setDays] = useState(30);

  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [receipt, setReceipt] = useState<Application | null>(null);

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

  const spec = flavors.find((f) => f.name === flavor && f.available);
  const gpu = gpus.find((g) => g.id === gpuId);
  const cardMax = gpu ? Math.max(0, gpu.available_cards) : 0;

  // 直接输入 #/apply 进来（没带选型）时，兜到第一档可用的云主机
  useEffect(() => {
    if (catalog && !spec && !gpu) {
      const first = flavors.find((f) => f.available);
      if (first) setFlavor(first.name);
    }
  }, [catalog, spec, gpu, flavors]);

  const submit = async () => {
    if (!spec) return;
    setBusy(true);
    setError(null);
    try {
      const res = await api.apply({
        flavor: spec.name,
        image: image?.id,
        gpu_model: gpu?.id,
        gpu_count: gpu ? cards : 0,
        applicant,
        contact,
        purpose,
        duration_days: days,
      });
      setReceipt(res.application);
      MessagePlugin.success('申请已受理，请留存回执号');
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : '提交失败，请稍后重试';
      setError(msg);
      MessagePlugin.error(msg);
    } finally {
      setBusy(false);
    }
  };

  /** 提交成功后整页换成回执，不再显示表单——避免同一份申请被重复提交 */
  if (receipt) {
    return (
      <div className="apply-page">
        <header className="topbar">
          <div className="brand">
            <Logo animated />
            <span>日新坊</span>
          </div>
          <div className="topbar-right">
            <Button variant="text" onClick={onBack}>
              返回产品主页
            </Button>
          </div>
        </header>

        <main className="app narrow">
          <Card bordered>
            <div className="receipt-head">
              <Tag theme="warning" variant="light" size="small">
                待处理
              </Tag>
              <h1 className="receipt-title">申请已受理</h1>
            </div>
            <Descriptions column={1} size="small" className="receipt-desc">
              <Descriptions.DescriptionsItem label="回执号">
                <span className="mono receipt-id">{receipt.id}</span>
              </Descriptions.DescriptionsItem>
              <Descriptions.DescriptionsItem label="主机名">
                <span className="mono">{receipt.hostname}</span>
              </Descriptions.DescriptionsItem>
              <Descriptions.DescriptionsItem label="配置">
                {receipt.gpu_count > 0
                  ? `${receipt.gpu_count} 张 ${receipt.gpu_model || '显卡'} + 云主机`
                  : '轻量云主机'}
              </Descriptions.DescriptionsItem>
              <Descriptions.DescriptionsItem label="登录方式">root · 随机口令</Descriptions.DescriptionsItem>
              <Descriptions.DescriptionsItem label="通知到">{receipt.contact}</Descriptions.DescriptionsItem>
            </Descriptions>
            <Divider />
            <p className="muted small">
              管理员核验用途与资源余量后会按上面留的联系方式告知开通信息，
              也可以在产品页底部用回执号自助查进度。
            </p>
            <div className="receipt-actions">
              <Button theme="primary" onClick={onBack}>
                返回产品主页
              </Button>
            </div>
          </Card>
        </main>
      </div>
    );
  }

  /** 右侧配置清单：三步共用，保证每步看到的选型一致。 */
  const summary = (
    <Card bordered title="配置清单">
      <Descriptions column={1} size="small">
        <Descriptions.DescriptionsItem label="类型">
          {gpu ? 'GPU 云服务器' : '轻量云主机'}
        </Descriptions.DescriptionsItem>
        <Descriptions.DescriptionsItem label="云主机">
          {spec ? `${spec.title} · ${spec.vcpu}核 ${humanMiB(spec.memory_mib)} ${spec.disk_gb}G` : '-'}
        </Descriptions.DescriptionsItem>
        <Descriptions.DescriptionsItem label="GPU">
          {gpu ? `${gpu.title} × ${cards}` : '不配显卡'}
        </Descriptions.DescriptionsItem>
        <Descriptions.DescriptionsItem label="镜像">{image?.title ?? '-'}</Descriptions.DescriptionsItem>
        <Descriptions.DescriptionsItem label="主机名">
          <span className="muted">系统自动分配</span>
        </Descriptions.DescriptionsItem>
        <Descriptions.DescriptionsItem label="登录方式">root · 随机口令</Descriptions.DescriptionsItem>
        <Descriptions.DescriptionsItem label="使用时长">{days} 天</Descriptions.DescriptionsItem>
      </Descriptions>
      <Divider />
      <p className="muted small">
        提交后由管理员核验用途与资源余量，通过后按上面留的联系方式告知开通信息。
      </p>
    </Card>
  );

  return (
    <div className="apply-page">
      <header className="topbar">
        <div className="brand">
          <Logo animated />
          <span>日新坊</span>
        </div>
        <div className="topbar-right">
          <Button variant="text" onClick={onBack}>
            返回产品主页
          </Button>
        </div>
      </header>

      <main className="app">
        <Steps current={step} className="apply-steps">
          <Steps.StepItem title="选择产品" />
          <Steps.StepItem title="申请信息" />
          <Steps.StepItem title="提交申请" />
        </Steps>

        {error && <div className="banner error">{error}</div>}

        {/* 第一步：选产品（云主机 + 可选显卡），用可点选卡片，比下拉更直观 */}
        {step === 0 && (
          <Card bordered title="选择产品">
            {gpu && (
              <div className="apply-alert">
                <Alert theme="warning" message="GPU 不能独立交付：申购时会自动连带一台轻量云主机作为算力主体。" />
              </div>
            )}
            <section className="opt-group">
              <h3 className="opt-group-title">云主机</h3>
              <Row gutter={16}>
                {flavors.map((f) => (
                  <Col span={6} key={f.name}>
                    <OptionCard
                      active={flavor === f.name}
                      disabled={!f.available}
                      title={f.title}
                      sub={`${f.vcpu} 核 ${humanMiB(f.memory_mib)} · ${f.disk_gb}G 系统盘`}
                      onClick={() => setFlavor(f.name)}
                    />
                  </Col>
                ))}
              </Row>
            </section>
            <section className="opt-group">
              <h3 className="opt-group-title">显卡（可选）</h3>
              <Row gutter={16}>
                <Col span={6}>
                  <OptionCard active={!gpuId} title="不配显卡" sub="仅云主机" onClick={() => setGpuId('')} />
                </Col>
                {gpus.map((g) => (
                  <Col span={6} key={g.id}>
                    <OptionCard
                      active={gpuId === g.id}
                      disabled={!g.available}
                      title={g.title}
                      sub={`${g.vram_gb}GB 显存${g.available ? ` · 库存 ${g.total}` : ' · 暂不可选'}`}
                      onClick={() => setGpuId(g.id)}
                    />
                  </Col>
                ))}
              </Row>
            </section>
            {gpu && cardMax > 0 && (
              <section className="opt-group">
                <h3 className="opt-group-title">显卡数量（整卡直通）</h3>
                <Radio.Group
                  variant="default-filled"
                  value={cards}
                  onChange={(v) => setCards(Number(v))}
                >
                  {Array.from({ length: Math.max(cardMax, 1) }, (_, i) => i + 1).map((n) => (
                    <Radio.Button key={n} value={n} disabled={n > cardMax}>
                      {n} 张
                    </Radio.Button>
                  ))}
                </Radio.Group>
              </section>
            )}
            <div className="wizard-foot">
              <Button variant="text" onClick={onBack}>
                取消
              </Button>
              <Button theme="primary" disabled={!spec} onClick={() => setStep(1)}>
                下一步
              </Button>
            </div>
          </Card>
        )}

        {/* 第二步：填申请信息 */}
        {step === 1 && (
          <Row gutter={20}>
            <Col span={8}>
              <Card bordered title="申请信息">
                <Form labelAlign="top">
                  <Form.FormItem label="申请人" help="填写姓名">
                    <Input value={applicant} onChange={setApplicant} placeholder="姓名" />
                  </Form.FormItem>
                  <Form.FormItem label="联系方式" help="填写手机号">
                    <Input value={contact} onChange={setContact} placeholder="手机号" />
                  </Form.FormItem>
                  <Form.FormItem label="使用时长" help="以天为单位，默认 30 天">
                    <Input
                      type="number"
                      value={String(days)}
                      onChange={(v) => setDays(Number(v) || 1)}
                    />
                  </Form.FormItem>
                  <Form.FormItem label="用途说明">
                    <Textarea
                      rows={3}
                      value={purpose}
                      placeholder="简单说明要跑什么，比如「跑一个内部的小服务」"
                      onChange={setPurpose}
                    />
                  </Form.FormItem>
                </Form>
                <div className="wizard-foot">
                  <Button variant="outline" onClick={() => setStep(0)}>
                    上一步
                  </Button>
                  <Button
                    theme="primary"
                    disabled={!applicant || !contact || !purpose}
                    onClick={() => setStep(2)}
                  >
                    下一步
                  </Button>
                </div>
              </Card>
            </Col>
            <Col span={4}>{summary}</Col>
          </Row>
        )}

        {/* 第三步：确认并提交 */}
        {step === 2 && (
          <Row gutter={20}>
            <Col span={8}>
              <Card bordered title="确认并提交">
                <Descriptions column={1} size="small">
                  <Descriptions.DescriptionsItem label="申请人">{applicant || '-'}</Descriptions.DescriptionsItem>
                  <Descriptions.DescriptionsItem label="联系方式">{contact || '-'}</Descriptions.DescriptionsItem>
                  <Descriptions.DescriptionsItem label="用途说明">{purpose || '-'}</Descriptions.DescriptionsItem>
                  <Descriptions.DescriptionsItem label="使用时长">{days} 天</Descriptions.DescriptionsItem>
                </Descriptions>
                <div className="wizard-foot">
                  <Button variant="outline" onClick={() => setStep(1)}>
                    上一步
                  </Button>
                  <Button
                    theme="primary"
                    loading={busy}
                    disabled={!applicant || !contact || !purpose || !spec}
                    onClick={() => void submit()}
                  >
                    提交申请
                  </Button>
                </div>
              </Card>
            </Col>
            <Col span={4}>{summary}</Col>
          </Row>
        )}
      </main>
    </div>
  );
}

/** 可点选的选项卡：选择产品时比下拉框更直观，选中态和 TDesign 一致。 */
function OptionCard({
  active,
  disabled,
  title,
  sub,
  onClick,
}: {
  active: boolean;
  disabled?: boolean;
  title: string;
  sub: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className={`opt${active ? ' active' : ''}${disabled ? ' off' : ''}`}
      disabled={disabled || undefined}
      onClick={() => !disabled && onClick()}
    >
      <span className="opt-title">{title}</span>
      <span className="opt-sub">{sub}</span>
    </button>
  );
}
