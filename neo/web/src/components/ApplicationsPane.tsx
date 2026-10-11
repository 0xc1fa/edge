import { useCallback, useEffect, useState } from 'react';
import { Button, Input, MessagePlugin, Radio, Table } from '../td';
import type { PrimaryTableCol } from 'tdesign-react';
import { api } from '../api';
import { flavorSummary, gpuSummary, imageTitle } from '../catalog';
import { humanTime } from '../format';
import { statusTag } from './ui';
import type { Application, Catalog, CatalogImage, Flavor } from '../types';

/**
 * 申请审批台。
 *
 * 第一版的边界要说清楚：**批准后不会自动创建实例**，只改台账状态并给出建议命令。
 * 原因：Incus socket 目前是只读挂载，而创建实例涉及端口分配与整卡互斥，
 * 在这两个分配器落地之前自动执行，风险大于收益 —— 宁可由人在终端确认一次。
 */
export default function ApplicationsPane() {
  const [items, setItems] = useState<Application[]>([]);
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [filter, setFilter] = useState<'pending' | ''>('pending');
  const [reason, setReason] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState<string | null>(null);
  const [command, setCommand] = useState<string | null>(null);
  // 首帧不把空数组当成「没有申请」，先显示加载态
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const r = await api.applications(filter);
      setItems(r.applications);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, [filter]);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 15_000);
    return () => window.clearInterval(timer);
  }, [load]);

  // 规格与镜像的名字要显示成人话，所以顺带拉一份目录
  useEffect(() => {
    api
      .catalog()
      .then(setCatalog)
      .catch(() => undefined);
  }, []);

  const decide = async (id: string, action: 'approve' | 'reject') => {
    setBusy(id);
    try {
      const r = await api.decide(id, action, reason[id] ?? '');
      setCommand(r.command);
      MessagePlugin.success(action === 'approve' ? '已批准，请复制下方命令执行' : '已驳回');
      await load();
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      setError(msg);
      MessagePlugin.error(msg);
    } finally {
      setBusy(null);
    }
  };

  const flavors = catalog?.flavors ?? [];
  const images: CatalogImage[] = catalog?.images ?? [];
  const gpuModels = catalog?.gpu_models ?? [];

  const specOf = (a: Application): Flavor | undefined => flavors.find((f) => f.name === a.flavor);

  const columns: PrimaryTableCol<Application>[] = [
    {
      colKey: 'id',
      title: '回执 / 提交时间',
      width: 180,
      cell: ({ row }) => (
        <div className="stack">
          <div className="mono cell-strong">{row.id}</div>
          <div className="muted small">{humanTime(row.created_at)}</div>
        </div>
      ),
    },
    {
      colKey: 'hostname',
      title: '主机配置',
      width: 250,
      cell: ({ row }) => {
        const spec = specOf(row);
        return (
          <div className="stack">
            <div className="cell-strong">{row.hostname}</div>
            <div className="muted small">
              {spec ? flavorSummary(spec) : row.flavor}
              {row.gpu_count > 0 && ` · ${gpuSummary(row.gpu_model ?? '', row.gpu_count, gpuModels)}`} ·{' '}
              {imageTitle(images, row.image)} · {row.duration_days} 天
            </div>
          </div>
        );
      },
    },
    {
      // 申请人一列把「怎么登录」也收进来：凭据单独占一列会让用途列挤到没法看
      colKey: 'applicant',
      title: '申请人 / 登录',
      width: 200,
      cell: ({ row }) => (
        <div className="stack">
          <div className="cell-strong">{row.applicant}</div>
          <div className="muted small mono">{row.contact}</div>
          <div className="muted small">
            {row.login_user || 'root'} · {row.ssh_public_key ? 'SSH 公钥' : '随机口令'}
          </div>
        </div>
      ),
    },
    { colKey: 'purpose', title: '用途', ellipsis: true },
    {
      colKey: 'status',
      title: '状态',
      width: 100,
      cell: ({ row }) => (
        <div className="stack">
          {statusTag(row)}
          {row.reason && <div className="muted small">{row.reason}</div>}
        </div>
      ),
    },
    {
      colKey: 'op',
      title: '处理',
      width: 210,
      cell: ({ row }) =>
        row.status === 'pending' ? (
          <div className="stack">
            <Input
              size="small"
              placeholder="处理意见（可选）"
              value={reason[row.id] ?? ''}
              onChange={(v) => setReason({ ...reason, [row.id]: v })}
            />
            <div className="cell-inline">
              <Button
                size="small"
                theme="primary"
                loading={busy === row.id}
                onClick={() => void decide(row.id, 'approve')}
              >
                批准
              </Button>
              <Button
                size="small"
                theme="danger"
                variant="outline"
                disabled={busy === row.id}
                onClick={() => void decide(row.id, 'reject')}
              >
                驳回
              </Button>
            </div>
          </div>
        ) : (
          <div className="muted small">{humanTime(row.updated_at)}</div>
        ),
    },
  ];

  return (
    <div className="workspace">
      {error && <div className="banner error">{error}</div>}

      {command && (
        <div className="banner cmd">
          <div className="cmd-head">
            <strong>已批准 · 建议执行</strong>
            <Button variant="text" size="small" onClick={() => setCommand(null)}>
              收起
            </Button>
          </div>
          <pre className="cmd-body">{command}</pre>
          <p className="muted small">
            第一版不会自动开通：请确认端口池与 GPU 归属后再在宿主执行。
          </p>
        </div>
      )}

      <section className="panel">
        <div className="panel-head">
          <h2>开通申请</h2>
          <div className="panel-head-right">
            <Radio.Group
              variant="default-filled"
              size="small"
              value={filter === '' ? 'all' : 'pending'}
              onChange={(v) => setFilter(v === 'all' ? '' : 'pending')}
            >
              <Radio.Button value="pending">仅待处理</Radio.Button>
              <Radio.Button value="all">全部</Radio.Button>
            </Radio.Group>
            <Button size="small" variant="outline" onClick={() => void load()}>
              刷新
            </Button>
          </div>
        </div>

        <Table
          rowKey="id"
          size="small"
          loading={loading}
          data={items}
          columns={columns}
          empty={filter === 'pending' ? '没有待处理的申请' : '还没有收到申请'}
        />
      </section>
    </div>
  );
}
