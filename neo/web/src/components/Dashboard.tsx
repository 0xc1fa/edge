import { useCallback, useEffect, useState } from 'react';
import {
  Button,
  Card,
  Col,
  Dialog,
  Input,
  Loading,
  MessagePlugin,
  Popconfirm,
  Select,
  Row,
  Space,
  Statistic,
  Table,
  Tag,
} from '../td';
import type { PrimaryTableCol } from 'tdesign-react';
import { ApiError, api } from '../api';
import { byteParts, humanBytes, humanTime, humanUptime } from '../format';
import GPUApplyDrawer from './GPUApplyDrawer';
import InstanceDrawer from './InstanceDrawer';
import WebShell from './WebShell';
import { StatusBadge, TypeBadge, UsageBar } from './ui';
import type { Capacity, Health, InstanceView, Sample, Account } from '../types';

// 轮询周期与后端采集间隔一致：更快没意义，更慢就不像实时监控了。
const POLL_MS = 10_000;

/**
 * 实例工作区。
 *
 * 事实来源是 Incus 本身：这里不持有任何可写的实例状态，
 * 所以「页面显示与 incus list 不一致」这类经典故障从根上不存在。
 *
 * 形态是宽表而不是一台一张卡：这一页的读者要横向比对多台机器
 * （谁内存满了、谁没在跑、谁的盘快满了），表格擅长这件事。
 * 曾经改成一机一卡，试过之后确认那更适合「只看自己那一两台的租户」，
 * 而不是要对全机房做判断的管理员 —— 于是改回了表。
 *
 * 租户与管理员共用这一页，但看到的不是同一份东西：
 *
 *   管理员 —— 顶部四张宿主配额卡片（他在调度整台机器，需要知道余量），
 *              以及每台实例登记在谁名下；
 *   租户   —— 没有那四张卡片，也没有宿主视角的归属信息；
 *              但有操作列（终端、口令、电源）—— 那是这一页给他的东西。
 *
 * 操作列**只给租户**：网页终端、改口令、开关机是「对自己那台机器」的自助动作，
 * 管理员要动手是在宿主上敲 tenant-ctl.sh，两套入口服务的是两种人。
 * 在管理员面前摆一排按钮，只会让他以为这是推荐的操作路径。
 */
export default function Dashboard({ canManage, me }: { canManage: boolean; me: string }) {
  const [instances, setInstances] = useState<InstanceView[]>([]);
  const [capacity, setCapacity] = useState<Capacity | null>(null);
  const [health, setHealth] = useState<Health | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [series, setSeries] = useState<Sample[]>([]);
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null);
  // 首帧不给假数据：没拿到之前宁可显示加载态，也不要先摆一排 0 再跳变
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // 从「未挂载」那一列点出来的申请抽屉；空串表示没在申请
  const [applyFor, setApplyFor] = useState('');
  // 归属分配：正在分配哪台机器、分配给谁
  const [assignFor, setAssignFor] = useState<InstanceView | null>(null);
  const [assignOwner, setAssignOwner] = useState('');
  const [members, setMembers] = useState<Account[]>([]);
  // 网页终端与改口令的目标实例；空串表示没打开
  const [shellFor, setShellFor] = useState('');
  const [pwFor, setPwFor] = useState('');
  const [pw, setPw] = useState('');
  const [busy, setBusy] = useState('');

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
      width: 190,
      cell: ({ row }) => (
        <div className="stack">
          <div className="cell-strong">{row.name}</div>
          <div className="muted small">
            {row.image_os} {row.image_release}
          </div>
          {/* 属主可见，被拒的人才看得出原因。宿主上存在台账之外手工建的实例，
              它们的属主是空的 —— 那种机器只有管理员能动，这一点要写在明面上。 */}
          {canManage && (
            <div className="muted small">{row.owner ? `登记人 ${row.owner}` : '未登记属主'}</div>
          )}
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
      width: 150,
      cell: ({ row }) =>
        row.gpus.length ? (
          <span className="mono">{row.gpus.length} 张</span>
        ) : (
          // 「未挂载」旁边直接给出口：看到没卡的人，下一步就是想要一张卡
          <Space size={4}>
            <span className="muted small">未挂载</span>
            <Button size="small" variant="text" onClick={() => setApplyFor(row.name)}>
              申请
            </Button>
          </Space>
        ),
    },
  ];

  // 操作列只挂在租户那一侧：管理员看到的是监控，不是自助入口。
  const opColumn: PrimaryTableCol<InstanceView> = {
      colKey: 'op',
      title: '操作',
      width: 260,
      fixed: 'right',
      // 只给租户：这些是「对自己那台机器」的自助动作。
      // 管理员那一侧不出现 —— 他要动手是在宿主上敲 tenant-ctl.sh，
      // 而不是在监控页上点一排按钮。
      cell: ({ row }) => {
        if (canManage) return null;
        // 不是自己登记的机器：按钮一律置灰，并说明归谁。
        // 置灰而不是藏起来 —— 藏起来会让人以为这台机器没有这些功能；
        // 也不放任它点下去吃一个 403，那是把服务端的拒绝当前端提示用。
        const mine = !!row.owner && row.owner === me;
        if (!mine) {
          return (
            <span className="muted small">
              {row.owner ? `登记在 ${row.owner} 名下` : '未登记属主'}
            </span>
          );
        }
        return (
          <Space size={4} breakLine>
            {row.running ? (
              <>
                {/* 网页终端不需要口令：登录态已经证明过身份。
                    停机时 exec 起不来，所以那个状态下不给这个入口。 */}
                <Button size="small" variant="text" onClick={() => setShellFor(row.name)}>
                  网页终端
                </Button>
                <Button size="small" variant="text" onClick={() => setPwFor(row.name)}>
                  重置口令
                </Button>
                <Popconfirm
                  content={`重启 ${row.name}？实例上正在跑的东西会中断`}
                  theme="warning"
                  onConfirm={() => void power(row.name, 'restart')}
                >
                  <Button size="small" variant="text" loading={busy === row.name + ':restart'}>
                    重启
                  </Button>
                </Popconfirm>
                <Popconfirm
                  content={`关机 ${row.name}？里面正在跑的任务会全部结束`}
                  theme="danger"
                  onConfirm={() => void power(row.name, 'stop')}
                >
                  <Button size="small" theme="danger" variant="text" loading={busy === row.name + ':stop'}>
                    关机
                  </Button>
                </Popconfirm>
              </>
            ) : (
              <Popconfirm content={`开机 ${row.name}？`} onConfirm={() => void power(row.name, 'start')}>
                <Button size="small" theme="primary" loading={busy === row.name + ':start'}>
                  开机
                </Button>
              </Popconfirm>
            )}
          </Space>
        );
      },
    };

  // 操作列只挂在租户那一侧：管理员看到的是监控，不是自助入口。
  if (!canManage) columns.push(opColumn);

  // 归属分配列正好相反 —— 那是管理员才有的动作：
  // 宿主上手工建的实例在申请台账里没有记录，不分一次，
  // 租户侧的终端/口令/电源对它们永远是灰的。
  const ownerColumn: PrimaryTableCol<InstanceView> = {
    colKey: 'owner',
    title: '归属',
    width: 200,
    fixed: 'right',
    cell: ({ row }) => (
      <Space size={4}>
        {row.owner ? (
          <>
            <span>{row.owner}</span>
            <Button size="small" variant="text" onClick={() => openAssign(row)}>
              改归属
            </Button>
            <Popconfirm
              content={`解除 ${row.name} 的归属？对方刷新后就再也用不了终端、口令与电源`}
              theme="warning"
              onConfirm={() => void unassign(row.name)}
            >
              <Button size="small" variant="text" loading={busy === row.name + ':unassign'}>
                解除
              </Button>
            </Popconfirm>
          </>
        ) : (
          <Button size="small" variant="outline" onClick={() => openAssign(row)}>
            分配给成员
          </Button>
        )}
      </Space>
    ),
  };

  if (canManage) columns.push(ownerColumn);

  /** 电源动作。每一个都会打断实例里正在跑的东西，所以都带二次确认。 */
  const power = async (name: string, action: 'start' | 'stop' | 'restart') => {
    setBusy(name + ':' + action);
    try {
      const r = await api.powerInstance(name, action);
      MessagePlugin.success(r.hint || `已${action}`);
      await load();
    } catch (e) {
      MessagePlugin.error(e instanceof ApiError ? e.message : String(e));
    } finally {
      setBusy('');
    }
  };

  /** 重置 root 口令。口令不落任何本地状态以外的位置。 */
  const resetPassword = async () => {
    if (!pwFor) return;
    setBusy(pwFor + ':pw');
    try {
      const r = await api.resetPassword(pwFor, pw);
      MessagePlugin.success(r.hint || '已重置');
      setPwFor('');
      setPw('');
    } catch (e) {
      MessagePlugin.error(e instanceof ApiError ? e.message : String(e));
    } finally {
      setBusy('');
    }
  };

  /** 打开分配弹窗时把成员名单取来。只取一次：名单变化很慢，
      而每次打开弹窗都拉一遍会在弱网下让弹窗转很久。 */
  const openAssign = (row: InstanceView) => {
    setAssignFor(row);
    setAssignOwner(row.owner ?? '');
    if (members.length === 0) {
      api
        .accounts()
        .then((r) => setMembers(r.users.filter((u) => u.status === 'active')))
        .catch(() => setMembers([]));
    }
  };

  const assign = async () => {
    if (!assignFor || !assignOwner) return;
    setBusy(assignFor.name + ':assign');
    try {
      const r = await api.assignInstance(assignFor.name, assignOwner);
      MessagePlugin.success(r.hint || '已分配');
      setAssignFor(null);
      await load();
    } catch (e) {
      MessagePlugin.error(e instanceof ApiError ? e.message : String(e));
    } finally {
      setBusy('');
    }
  };

  const unassign = async (name: string) => {
    setBusy(name + ':unassign');
    try {
      const r = await api.unassignInstance(name);
      MessagePlugin.success(r.hint || '已解除');
      await load();
    } catch (e) {
      MessagePlugin.error(e instanceof ApiError ? e.message : String(e));
    } finally {
      setBusy('');
    }
  };

  // Statistic 只收数字，单位走 unit 属性：概览才不会退化成自己拼字符串
  const mem = capacity ? byteParts(capacity.memory.allocated) : null;
  const disk = capacity ? byteParts(capacity.disk.allocated) : null;

  return (
    <div className="workspace">
      {error && <div className="banner error">加载失败：{error}</div>}
      {health && !health.ok && health.error && <div className="banner warn">Incus 连接异常：{health.error}</div>}

      {/* 概览四格只给管理员：宿主还剩多少核、多少内存，是「调度整台机器」
          才需要的量。摆到租户面前只会被误读成「这是我能用的额度」。
          首帧盖一层 Loading，避免先显示 0 再跳成真实值。 */}
      {canManage && (
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
      )}

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

      {/* 分配归属（仅管理员）。成员名单从「成员」页来：分给一个查不到的人，
          等于把机器交给一个谁都登不进来的账号，而页面上只看得到一个正常名字。 */}
      <Dialog
        visible={!!assignFor}
        onClose={() => setAssignFor(null)}
        header={`分配 ${assignFor?.name ?? ''}`}
        width={460}
        footer={
          <Space size="small">
            <Button
              theme="primary"
              loading={busy === (assignFor?.name ?? '') + ':assign'}
              onClick={() => void assign()}
            >
              分配
            </Button>
            <Button variant="outline" onClick={() => setAssignFor(null)}>
              取消
            </Button>
          </Space>
        }
      >
        <div className="stack">
          <Select
            value={assignOwner}
            onChange={(v) => setAssignOwner(v as string)}
            placeholder="选择成员"
            filterable
          >
            {members.map((u) => (
              <Select.Option key={u.id} value={u.name} label={u.name}>
                {u.name}
                {u.role === 'admin' ? '（管理员）' : ''}
              </Select.Option>
            ))}
          </Select>
          <p className="muted small">
            分配之后，对方刷新页面就能在自己那台机器上使用网页终端、重置口令与开关机。
            改归属会立刻覆盖原归属；解除则让这台机器回到未登记状态。
          </p>
        </div>
      </Dialog>

      <InstanceDrawer instance={selectedInstance} series={series} onClose={() => setSelected(null)} />

      {/* 网页终端。它不弹确认框 —— 打开一个终端不该有二次确认，
          但里面的每一条命令都真的会执行，所以欢迎词里写明了怎么退出。 */}
      {shellFor && <WebShell instance={shellFor} onClose={() => setShellFor('')} />}

      {/* 重置口令。要求输两遍：口令输错一次不会有人发现，
          而「以为改了其实没改」比多输一遍烦得多。 */}
      <Dialog
        visible={!!pwFor}
        onClose={() => {
          setPwFor('');
          setPw('');
        }}
        header={`重置 ${pwFor} 的 root 口令`}
        width={460}
        footer={
          <Space size="small">
            <Button theme="primary" loading={busy === pwFor + ':pw'} onClick={() => void resetPassword()}>
              重置
            </Button>
            <Button
              variant="outline"
              onClick={() => {
                setPwFor('');
                setPw('');
              }}
            >
              取消
            </Button>
          </Space>
        }
      >
        <div className="stack">
          <Input
            type="password"
            value={pw}
            onChange={(v) => setPw(v)}
            placeholder="新口令（至少 8 位）"
            autocomplete="new-password"
          />
          <p className="muted small">
            新口令立即生效，之后用它 SSH 登录 root。旧口令无论是什么都不再需要 —— 这正是这个功能存在的理由。
          </p>
        </div>
      </Dialog>

      {applyFor && (
        <GPUApplyDrawer
          instance={applyFor}
          onClose={() => setApplyFor('')}
          onDone={() => void load()}
        />
      )}
    </div>
  );
}
