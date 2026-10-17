import { useCallback, useEffect, useState } from 'react';
import { Button, Card, Space, Table, Tag, Tooltip } from '../../td';
import type { PrimaryTableCol } from 'tdesign-react';
import { ApiError, api } from '../../api';
import type { GPUBooking, GPUOverview } from '../../types';
import { STATUS, countdown, remainOf, stamp } from './Shared';

// 排期是慢变量，15 秒足够；「使用中」那一条的倒计时由本地每秒走。
const POLL_MS = 15_000;

interface Props {
  /** 留着是为了与管理员视图同形：两边都由 GpuPane 用同一种方式调用 */
  onChanged?: () => void;
}

/**
 * 租户看到的 GPU 调度：一张只读的日志。
 *
 * 这一页回答的问题只有一个 —— 「那张卡现在有没有被挂走、什么时候挂的、
 * 什么时候摘的」。所以这里没有审批、没有挂载按钮、也没有待批准队列：
 * 那些记录服务端根本不会发过来（成员拿到的每一条都是真的挂上去过的）。
 *
 * 「已批准」这种状态刻意不出现：看见它的人会以为卡已经到手了，
 * 而那一刻卡其实还原封不动地在机器上插着。
 */
export default function MemberLog({ onChanged }: Props) {
  const [data, setData] = useState<GPUOverview | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [tick, setTick] = useState(0);

  const load = useCallback(async () => {
    try {
      setData(await api.gpuBookings());
      setError(null);
      // 与管理员视图保持同一节律：数据变了就通知外层（由它决定要不要做什么）
      onChanged?.();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    }
  }, [onChanged]);

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

  const columns: PrimaryTableCol<GPUBooking>[] = [
    { colKey: 'instance', title: '实例', width: 150, cell: ({ row }) => <span className="mono">{row.instance}</span> },
    {
      colKey: 'applicant',
      title: '使用者',
      width: 170,
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
      cell: ({ row }) => (
        <div className="stack">
          <span className="mono">挂 {stamp(row.attached_at)}</span>
          <span className="muted small">
            {row.detached_at ? `摘 ${stamp(row.detached_at)}` : '尚未摘卡'}
          </span>
        </div>
      ),
    },
    {
      colKey: 'status',
      title: '状态',
      width: 190,
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
          </div>
        );
      },
    },
  ];

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
          这里只记真正发生过的调度：哪台机器、什么时候挂上、什么时候摘下。
          要用卡请到「实例」页，在自己那台机器的 GPU 列点「申请」。
        </div>

        <Table
          rowKey="id"
          size="small"
          hover
          data={data?.bookings ?? []}
          columns={columns}
          empty="还没有实际调度记录"
          loading={!data && !error}
        />

        <div className="muted small" style={{ marginTop: 8 }}>
          卡挂上之后由服务端在当天 16:00 自动摘下并写回这里，不用人工记着；
          请提醒自己别留下跑不完的任务。
        </div>
      </Card>
    </div>
  );
}
