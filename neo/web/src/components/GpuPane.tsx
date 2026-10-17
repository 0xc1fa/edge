import { useCallback, useEffect, useState } from 'react';
import {
  Button,
  Card,
  Dialog,
  MessagePlugin,
  Popconfirm,
  Space,
  Table,
  Tag,
  Tooltip,
} from '../td';
import type { PrimaryTableCol } from 'tdesign-react';
import { ApiError, api } from '../api';
import GPUApplyDrawer from './GPUApplyDrawer';
import type { GPUBooking, GPUOverview, InstanceView } from '../types';

// 排期是慢变量，15 秒足够；但「正在使用中」那一条要显示倒计时，
// 所以倒计时由本地每秒走一次，不靠轮询。
const POLL_MS = 15_000;

const STATUS: Record<GPUBooking['status'], { label: string; theme: 'warning' | 'success' | 'danger' | 'primary' | 'default' }> = {
  pending: { label: '待批准', theme: 'warning' },
  approved: { label: '已批准', theme: 'primary' },
  rejected: { label: '已驳回', theme: 'danger' },
  active: { label: '使用中', theme: 'success' },
  ended: { label: '已结束', theme: 'default' },
  cancelled: { label: '已取消', theme: 'default' },
};

/** 距离时段结束还有多少秒。按服务端给的结束时刻算，不用本地推算几点 */
const remainOf = (end: string) => {
  const t = new Date(end).getTime();
  if (!t) return 0;
  return Math.max(0, Math.floor((t - Date.now()) / 1000));
};

/** 倒计时：把秒数说成人读的话 */
const countdown = (sec: number) => {
  if (sec <= 0) return '时段已结束，即将自动摘卡';
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (h > 0) return `${h} 小时 ${m} 分后自动摘卡`;
  return `${m} 分钟后自动摘卡`;
};

interface Props {
  /** 当前登录者是不是管理员：决定审批与挂载按钮在不在 */
  canManage: boolean;
  /** 当前登录者的名字，用于「只能取消自己的预约」 */
  me?: string;
  instances: InstanceView[];
  onChanged?: () => void;
}

/**
 * GPU 排期与挂载。
 *
 * 这张表回答三个问题：今天谁在用、接下来谁约了、还有没有位子。
 * 挂载与摘卡是两个分开的按钮 —— 批准只是同意，插卡才是执行，
 * 中间那一下要有人按，是因为它会动到宿主上真实的东西。
 *
 * 到期由服务端自动摘（16:00），这里只负责把倒计时显示清楚，
 * 让人知道还剩多久，而不是到了点才发现任务被掐断。
 */
export default function GpuPane({ canManage, me = '', instances, onChanged }: Props) {
  const [data, setData] = useState<GPUOverview | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [applying, setApplying] = useState(false);
  const [tick, setTick] = useState(0);
  // 挂载成功之后的提示：设备进了实例不等于容器里看得见，这件事要明说
  const [restartAsk, setRestartAsk] = useState<{ instance: string; hint: string } | null>(null);

  const load = useCallback(async () => {
    try {
      setData(await api.gpuBookings());
      setError(null);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), POLL_MS);
    return () => window.clearInterval(timer);
  }, [load]);

  // 只在使用中有活的时候才每秒走一次，避免一个空页面也在空转
  const hasActive = (data?.bookings ?? []).some((b) => b.status === 'active');
  useEffect(() => {
    if (!hasActive) return;
    const timer = window.setInterval(() => setTick((n) => n + 1), 1000);
    return () => window.clearInterval(timer);
  }, [hasActive]);

  const act = async (key: string, fn: () => Promise<unknown>, ok?: string) => {
    setBusy(key);
    try {
      await fn();
      if (ok) MessagePlugin.success(ok);
      await load();
      onChanged?.();
    } catch (e) {
      MessagePlugin.error(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  };

  const attach = (b: GPUBooking) =>
    void act(
      b.id,
      async () => {
        const r = await api.attachGPU(b.id);
        // 挂载成功之后必须把「要不要重启」摆到管理员面前：
        // 这台机器上容器里的 /dev/nvidia* 通常要重启才出现，
        // 而自动重启会打断实例上正在跑的东西 —— 这个决定只能由人来做。
        if (r.needs_restart) setRestartAsk({ instance: b.instance, hint: r.hint });
      },
      '已挂载',
    );

  const columns: PrimaryTableCol<GPUBooking>[] = [
    {
      colKey: 'date',
      title: '日期',
      width: 150,
      cell: ({ row }) => (
        <div className="stack">
          <span className="mono">{row.date}</span>
          {row.date === data?.today && <Tag size="small" theme="primary" variant="light">今天</Tag>}
        </div>
      ),
    },
    { colKey: 'instance', title: '实例', width: 130, cell: ({ row }) => <span className="mono">{row.instance}</span> },
    {
      colKey: 'applicant',
      title: '申请人',
      width: 160,
      cell: ({ row }) => (
        <div className="stack">
          <span>{row.applicant}</span>
          {row.purpose && <span className="muted small">{row.purpose}</span>}
        </div>
      ),
    },
    {
      colKey: 'status',
      title: '状态',
      width: 200,
      cell: ({ row }) => {
        const s = STATUS[row.status];
        return (
          <div className="stack">
            <Tag theme={s.theme} variant="light" size="small">
              {s.label}
            </Tag>
            {/* 倒计时只在真正挂着卡的时候出现：让人看得见还剩多久，
                而不是到了 16 点才发现任务被掐断。tick 每秒变化触发重算 */}
            {row.status === 'active' && (
              <span className="muted small" data-tick={tick}>
                {countdown(remainOf(row.window_end))}
              </span>
            )}
            {row.device && <span className="muted small">设备 {row.device}</span>}
          </div>
        );
      },
    },
    {
      colKey: 'reason',
      title: '意见',
      width: 140,
      cell: ({ row }) => <span className="muted small">{row.reason || '—'}</span>,
    },
    {
      colKey: 'op',
      title: '操作',
      width: 260,
      fixed: 'right',
      cell: ({ row }) => {
        const mine = row.applicant === me;
        return (
          <Space size={4} breakLine>
            {canManage && row.status === 'pending' && (
              <>
                <Popconfirm content={`批准「${row.applicant}」在 ${row.date} 用卡？`} onConfirm={() => void act(row.id, () => api.decideGPU(row.id, 'approve', ''), '已批准')}>
                  <Button size="small" theme="primary" variant="text" loading={busy === row.id}>
                    批准
                  </Button>
                </Popconfirm>
                <Popconfirm content="驳回这条预约？" theme="warning" onConfirm={() => void act(row.id, () => api.decideGPU(row.id, 'reject', '管理员驳回'), '已驳回')}>
                  <Button size="small" variant="text">
                    驳回
                  </Button>
                </Popconfirm>
              </>
            )}

            {canManage && row.status === 'approved' && (
              <Popconfirm
                content={`把卡挂到 ${row.instance} 上？实例上正在跑的任务不会中断，但租户侧可能要重启才看得到卡`}
                onConfirm={() => attach(row)}
              >
                <Button size="small" theme="primary" loading={busy === row.id}>
                  挂载
                </Button>
              </Popconfirm>
            )}

            {canManage && row.status === 'active' && (
              <>
                <Popconfirm
                  content={`现在把卡从 ${row.instance} 上摘掉？实例上正在用卡的任务会中断`}
                  theme="warning"
                  onConfirm={() => void act(row.id, () => api.detachGPU(row.id), '已摘卡')}
                >
                  <Button size="small" theme="danger" variant="outline" loading={busy === row.id}>
                    摘卡
                  </Button>
                </Popconfirm>
                <Popconfirm
                  content={`重启 ${row.instance}？实例上所有正在跑的东西都会中断`}
                  theme="danger"
                  onConfirm={() => void act(row.id, () => api.restartInstance(row.instance), '已重启')}
                >
                  <Button size="small" variant="text">
                    重启实例
                  </Button>
                </Popconfirm>
              </>
            )}

            {/* 还没挂卡的可以取消：自己的随便取消，管理员能取消任何人的 */}
            {(row.status === 'pending' || row.status === 'approved') && (canManage || mine) && (
              <Popconfirm
                content="取消这条预约？"
                theme="warning"
                onConfirm={() => void act(row.id, () => api.cancelGPU(row.id), '已取消')}
              >
                <Button size="small" variant="text">
                  取消
                </Button>
              </Popconfirm>
            )}
          </Space>
        );
      },
    },
  ];

  const used = (data?.bookings ?? []).filter((b) => b.status === 'active').length;

  return (
    <div className="workspace">
      {error && <div className="banner error">加载失败：{error}</div>}

      <Card bordered className="panel">
        <div className="panel-head">
          <h2>GPU 排期</h2>
          <Space size="small" align="center">
            <Tooltip content="此刻还能批几张：总数减去实例上真实挂着的卡">
              <span className="muted small">
                可用 {data?.available ?? 0} / {data?.total ?? 0} 张
              </span>
            </Tooltip>
            <span className="muted small">时段 {data?.window ?? ''}</span>
            <Button size="small" theme="primary" onClick={() => setApplying(true)}>
              申请时段
            </Button>
            <Button size="small" variant="outline" onClick={() => void load()}>
              刷新
            </Button>
          </Space>
        </div>

        <div className="muted small" style={{ marginBottom: 8 }}>
          到期自动摘卡：服务端在时段结束（16:00）时把卡摘下并写回台账，
          不用人工记着；请提醒使用者别留下跑不完的任务。
        </div>

        <Table
          rowKey="id"
          size="small"
          hover
          data={data?.bookings ?? []}
          columns={columns}
          empty="还没有人预约 GPU"
          loading={!data && !error}
        />
      </Card>

      {applying && (
        <GPUApplyDrawer
          instances={instances}
          defaultApplicant={me}
          onClose={() => setApplying(false)}
          onDone={() => void load()}
        />
      )}

      {/* 挂载成功后的重启询问：不自动重启，把决定留给管理员 */}
      <Dialog
        visible={restartAsk !== null}
        onClose={() => setRestartAsk(null)}
        header="卡已挂上，需要重启才能生效"
        width={520}
        footer={
          <Space size="small">
            <Button
              theme="danger"
              onClick={() => {
                const name = restartAsk?.instance ?? '';
                void act('restart-' + name, () => api.restartInstance(name), '已重启');
                setRestartAsk(null);
              }}
            >
              重启实例
            </Button>
            <Button variant="outline" onClick={() => setRestartAsk(null)}>
              稍后自己处理
            </Button>
          </Space>
        }
      >
        <div className="stack">
          <div>{restartAsk?.hint}</div>
          <div className="muted small">
            这台机器上，容器里的 /dev/nvidia* 往往要重启实例才会出现。
            重启会中断实例上正在跑的一切（包括 SSH 连接），所以不替你做这个决定。
          </div>
          <div className="muted small">
            也可以在租户侧自行重启，或让使用者重新登录后再试 nvidia-smi。
            {used > 0 && ' 当前有 ' + used + ' 条预约正在使用中。'}
          </div>
        </div>
      </Dialog>
    </div>
  );
}
