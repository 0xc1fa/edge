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
} from '../../td';
import type { PrimaryTableCol } from 'tdesign-react';
import { ApiError, api } from '../../api';
import type { GPUBooking, GPUOverview } from '../../types';
import { STATUS, countdown, remainOf, stamp } from './Shared';

const POLL_MS = 15_000;

interface Props {
  onChanged?: () => void;
}

/**
 * 管理员看到的 GPU 调度：一张日志 + 一摊待办。
 *
 * 与租户那一页共用同一个接口、同一套状态字典，但这里多三样东西：
 * 待批准与待挂载的记录、操作列（批准/驳回/挂载/摘卡/重启/删除）、
 * 以及按待办优先排序的表格。
 *
 * 挂载与摘卡分成两个按钮是有意的：批准只是同意，插卡才是执行。
 * 合起来做的话，批准时卡正被别人占着就会失败，而失败会让审批结论本身变得含糊。
 */
export default function AdminConsole({ onChanged }: Props) {
  const [data, setData] = useState<GPUOverview | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
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
    void act(b.id, async () => {
      const r = await api.attachGPU(b.id);
      // 挂载之后要不要重启由服务端判断（看容器是否带着卡启动过），
      // 它说不用就真的不用 —— 那个判据的实测依据写在 gpuWantsRestart 的注释里。
      if (r.needs_restart) setRestartAsk({ instance: b.instance, hint: r.hint });
    }, '已挂载');

  const columns: PrimaryTableCol<GPUBooking>[] = [
    { colKey: 'instance', title: '实例', width: 140, cell: ({ row }) => <span className="mono">{row.instance}</span> },
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
      colKey: 'scheduled',
      title: '调度',
      width: 200,
      cell: ({ row }) =>
        row.attached_at ? (
          <div className="stack">
            <span className="mono">挂 {stamp(row.attached_at)}</span>
            <span className="muted small">
              {row.detached_at ? `摘 ${stamp(row.detached_at)}` : '尚未摘卡'}
            </span>
          </div>
        ) : (
          // 只有管理员看得到这一行（成员拿不到未调度的记录），
          // 所以「未调度」三个字是待办标记，不是给使用者的进度条
          <span className="muted small">登记于 {stamp(row.created_at)}，未调度</span>
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
            {row.status === 'active' && (
              <span className="muted small" data-tick={tick}>
                {countdown(remainOf(row.window_end))}
              </span>
            )}
            {row.device && <span className="muted small">设备 {row.device}</span>}
            {row.reason && <span className="muted small">{row.reason}</span>}
          </div>
        );
      },
    },
    {
      colKey: 'op',
      title: '操作',
      width: 300,
      fixed: 'right',
      cell: ({ row }) => (
        <Space size={4} breakLine>
          {row.status === 'pending' && (
            <>
              <Popconfirm
                content={`批准「${row.applicant}」用卡？`}
                onConfirm={() => void act(row.id, () => api.decideGPU(row.id, 'approve', ''), '已批准')}
              >
                <Button size="small" theme="primary" variant="text" loading={busy === row.id}>
                  批准
                </Button>
              </Popconfirm>
              <Popconfirm
                content="驳回这条预约？"
                theme="warning"
                onConfirm={() => void act(row.id, () => api.decideGPU(row.id, 'reject', '管理员驳回'), '已驳回')}
              >
                <Button size="small" variant="text">
                  驳回
                </Button>
              </Popconfirm>
            </>
          )}

          {row.status === 'approved' && (
            <Popconfirm
              content={`把卡挂到 ${row.instance} 上？实例上正在跑的任务不会中断，但租户侧可能要重启才看得到卡`}
              onConfirm={() => attach(row)}
            >
              <Button size="small" theme="primary" loading={busy === row.id}>
                挂载
              </Button>
            </Popconfirm>
          )}

          {row.status === 'active' && (
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

          {(row.status === 'pending' || row.status === 'approved') && (
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

          {/* 删除只给「卡不在实例上」的记录：挂着的先摘卡，
              否则台账上没了这条，那张卡就没人认领，自动摘卡也找不到它 */}
          {row.status !== 'active' && (
            <Popconfirm
              content={`删除这条记录？${row.instance} 的这条调度留痕会被彻底抹掉`}
              theme="danger"
              onConfirm={() => void act(row.id, () => api.deleteGPU(row.id), '已删除')}
            >
              <Button size="small" variant="text">
                删除
              </Button>
            </Popconfirm>
          )}
        </Space>
      ),
    },
  ];

  // 待办排前面：管理员打开这页的第一件事是处理新申请，
  // 而不是从一屏历史记录里把它找出来。
  const rows = [...(data?.bookings ?? [])].sort((a, b) => {
    const rank = (b: GPUBooking) => (b.status === 'pending' ? 0 : b.status === 'approved' ? 1 : 2);
    return rank(a) - rank(b);
  });

  return (
    <div className="workspace">
      {error && <div className="banner error">加载失败：{error}</div>}

      <Card bordered className="panel">
        <div className="panel-head">
          <h2>GPU 调度</h2>
          <Space size="small" align="center">
            <Tooltip content="此刻还能批几张：总数减去实例上真实挂着的卡">
              <span className="muted small">
                可用 {data?.available ?? 0} / {data?.total ?? 0} 张
              </span>
            </Tooltip>
            <Button size="small" variant="outline" onClick={() => void load()}>
              刷新
            </Button>
          </Space>
        </div>

        <div className="muted small" style={{ marginBottom: 8 }}>
          {data && data.pending > 0
            ? `有 ${data.pending} 条待批准，排在表格最前面。挂上与摘下的时刻都记在这里。`
            : '没有待办了。挂上与摘下的时刻都记在这里，待批准与待挂载的也会一并列出。'}
        </div>

        <Table
          rowKey="id"
          size="small"
          hover
          data={rows}
          columns={columns}
          empty="还没有任何预约"
          loading={!data && !error}
        />

        <div className="muted small" style={{ marginTop: 8 }}>
          卡挂上之后由服务端在当天 16:00 自动摘下并写回台账，不用人工记着；
          请提醒使用者别留下跑不完的任务。
        </div>
      </Card>

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
          <div className="muted small">也可以在租户侧自行重启，或让使用者重新登录后再试 nvidia-smi。</div>
        </div>
      </Dialog>
    </div>
  );
}
