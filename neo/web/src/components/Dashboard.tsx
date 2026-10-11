import { useCallback, useEffect, useState } from 'react';
import { Button, Card, Col, Loading, Row, Statistic, Table, Tag } from '../td';
import type { PrimaryTableCol } from 'tdesign-react';
import { ApiError, api } from '../api';
import { byteParts, humanBytes, humanTime, humanUptime } from '../format';
import InstanceDrawer from './InstanceDrawer';
import { StatusBadge, TypeBadge, UsageBar } from './ui';
import type { Capacity, Health, InstanceView, Sample } from '../types';

// 轮询周期与后端采集间隔一致：更快没意义，更慢就不像实时监控了。
const POLL_MS = 10_000;

/**
 * 实例工作区。
 *
 * 事实来源是 Incus 本身：这里不持有任何可写的实例状态，
 * 所以「页面显示与 incus list 不一致」这类经典故障从根上不存在。
 */
export default function Dashboard() {
  const [instances, setInstances] = useState<InstanceView[]>([]);
  const [capacity, setCapacity] = useState<Capacity | null>(null);
  const [health, setHealth] = useState<Health | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [series, setSeries] = useState<Sample[]>([]);
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null);
  // 首帧不给假数据：没拿到之前宁可显示加载态，也不要先摆一排 0 再跳变
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const [inst, cap] = await Promise.all([api.instances(), api.capacity()]);
      setInstances(inst.instances);
      setCapacity(cap);
      setUpdatedAt(new Date());
      setError(null);

      // 实例被别人删掉时，选中态要自动回到未选中，否则抽屉会卡在幽灵实例上
      setSelected((current) => {
        if (current && !inst.instances.some((i) => i.name === current)) return null;
        return current;
      });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), POLL_MS);
    return () => window.clearInterval(timer);
  }, [load]);

  // 健康位由这里轮询：会话过期会先表现为 health 401，
  // 此时刷新页面，外壳会把人送回登录卡。
  useEffect(() => {
    const tick = async () => {
      try {
        setHealth(await api.health());
      } catch (e) {
        if (e instanceof ApiError && (e.status === 401 || e.status === 403)) {
          window.location.reload();
        }
      }
    };
    void tick();
    const timer = window.setInterval(tick, POLL_MS);
    return () => window.clearInterval(timer);
  }, []);

  // 选中项变化时单独拉它的采样序列
  useEffect(() => {
    if (!selected) {
      setSeries([]);
      return;
    }
    let cancelled = false;
    const pull = () => {
      api
        .instance(selected)
        .then((d) => {
          if (!cancelled) setSeries(d.series);
        })
        .catch(() => {
          if (!cancelled) setSeries([]);
        });
    };
    pull();
    const timer = window.setInterval(pull, POLL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [selected]);

  const selectedInstance = instances.find((i) => i.name === selected) ?? null;
  const running = instances.filter((i) => i.running).length;

  const columns: PrimaryTableCol<InstanceView>[] = [
    {
      colKey: 'name',
      title: '实例',
      width: 170,
      cell: ({ row }) => (
        <div className="stack">
          <div className="cell-strong">{row.name}</div>
          <div className="muted small">
            {row.image_os} {row.image_release}
          </div>
        </div>
      ),
    },
    {
      colKey: 'status',
      title: '状态',
      width: 150,
      cell: ({ row }) => (
        <div className="cell-inline">
          <StatusBadge running={row.running} label={row.status_label} />
          <TypeBadge label={row.type_label} />
        </div>
      ),
    },
    {
      colKey: 'quota',
      title: '规格',
      width: 190,
      cell: ({ row }) => (
        // 「2 vCPU · 4GiB · 20GiB」太长会把这列撑到换行，规格列按惯例写「2 核」
        <span className="mono">
          {row.quota_cpu} 核 · {row.quota_memory} · {row.quota_disk}
        </span>
      ),
    },
    {
      colKey: 'ipv4',
      title: '内网地址',
      width: 130,
      cell: ({ row }) => <span className="mono">{row.nics[0]?.ipv4 || '-'}</span>,
    },
    {
      colKey: 'mem',
      title: '内存',
      width: 150,
      cell: ({ row }) => (
        <div className="stack">
          <UsageBar percent={row.mem_percent} />
          <span className="muted small">
            {humanBytes(row.mem_usage)} / {humanBytes(row.mem_total)}
          </span>
        </div>
      ),
    },
    {
      colKey: 'disk',
      title: '根盘',
      width: 150,
      cell: ({ row }) => (
        <div className="stack">
          <UsageBar percent={row.disk_percent} />
          <span className="muted small">
            {humanBytes(row.disk_used)} / {humanBytes(row.disk_total)}
          </span>
        </div>
      ),
    },
    { colKey: 'uptime', title: '运行时长', width: 100, cell: ({ row }) => <span className="mono">{humanUptime(row.uptime_seconds)}</span> },
    {
      colKey: 'gpu',
      title: 'GPU',
      width: 80,
      cell: ({ row }) => <span className="mono">{row.gpus.length ? `${row.gpus.length} 张` : '未挂载'}</span>,
    },
  ];

  // Statistic 只收数字，单位走 unit 属性：概览才不会退化成自己拼字符串
  const mem = capacity ? byteParts(capacity.memory.allocated) : null;
  const disk = capacity ? byteParts(capacity.disk.allocated) : null;

  return (
    <div className="workspace">
      {error && <div className="banner error">加载失败：{error}</div>}
      {health && !health.ok && health.error && <div className="banner warn">Incus 连接异常：{health.error}</div>}

      {/* 概览：四格统计走 TDesign Statistic，单位与精度由框架处理。
          首帧盖一层 Loading，避免先显示 0 再跳成真实值。 */}
      <Loading loading={loading} text="加载中…">
        <Row gutter={[16, 16]} className="cards">
          <Col xs={12} sm={6} lg={3}>
            <Card bordered className="stat-card">
              <Statistic
                title="实例"
                value={running}
                suffix={<span className="stat-suffix">/ {instances.length}</span>}
              />
              <p className="muted small">运行中 / 总数</p>
            </Card>
          </Col>
          <Col xs={12} sm={6} lg={3}>
            <Card bordered className="stat-card">
              <Statistic
                title="vCPU"
                value={capacity ? capacity.cpu.allocated : 0}
                suffix={capacity?.cpu.total ? <span className="stat-suffix">/ {capacity.cpu.total}</span> : undefined}
              />
              <p className="muted small">
                {capacity?.cpu.total
                  ? `宿主 ${capacity.cpu.total} 线程，已分配 ${Math.round((capacity.cpu.allocated / capacity.cpu.total) * 100)}%`
                  : '宿主线程数未知'}
              </p>
            </Card>
          </Col>
          <Col xs={12} sm={6} lg={3}>
            <Card bordered className="stat-card">
              <Statistic title="内存配额" value={mem?.value ?? 0} unit={mem?.unit} />
              <p className="muted small">
                {capacity?.memory.total ? `宿主总量 ${humanBytes(capacity.memory.total)}` : '宿主总量未知'}
              </p>
            </Card>
          </Col>
          <Col xs={12} sm={6} lg={3}>
            <Card bordered className="stat-card">
              <Statistic title="根盘配额" value={disk?.value ?? 0} unit={disk?.unit} />
              <p className="muted small">不含宿主系统与存储池余量</p>
            </Card>
          </Col>
        </Row>
      </Loading>

      <section className="panel">
        <div className="panel-head">
          <h2>实例列表</h2>
          <div className="panel-head-right">
            <Tag theme={health?.ok ? 'success' : 'danger'} variant="light" size="small">
              {health?.ok ? '数据链路正常' : '数据链路异常'}
            </Tag>
            <span className="muted small">
              {updatedAt
                ? `刷新于 ${humanTime(updatedAt.toISOString())} · 每 ${POLL_MS / 1000}s`
                : '等待首次刷新…'}
            </span>
            <Button size="small" variant="outline" onClick={() => void load()}>
              立即刷新
            </Button>
          </div>
        </div>

        <Table
          rowKey="name"
          size="small"
          hover
          loading={loading}
          data={instances}
          columns={columns}
          empty="当前没有实例"
          onRowClick={({ row }) => setSelected(row.name)}
        />
      </section>

      <InstanceDrawer instance={selectedInstance} series={series} onClose={() => setSelected(null)} />
    </div>
  );
}
