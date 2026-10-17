import { useCallback, useEffect, useState } from 'react';
import { Button, Dialog, Input, MessagePlugin, Popconfirm, Select, Space, Table, Tag, Tooltip } from '../td';
import type { PrimaryTableCol } from 'tdesign-react';
import { api } from '../api';
import { humanAgo, humanDuration, humanTime } from '../format';
import type { SessionRow } from '../types';

/**
 * 会话台账：谁登进来了、在哪台机器上、什么时候来的、要不要清掉。
 *
 * 「实时」是怎么来的必须说清楚，否则这个值会被误读：
 *   服务端把「超过 90 秒没有请求」判为离线，页面每 5 秒拉一次这里，
 *   翻页与轮询都会刷新活跃时间 —— 看到的在线与否是心跳的结果，
 *   不是「cookie 还没过期」。
 *
 * 会话默认是长期有效的（寿命那件事交给人决定，而不是替人决定），
 * 所以这一页上的「删除」是主要动作：不想要了就删，删完那条凭证立刻不能再用。
 */

/** 签发链接时可选的时长 */
const TTL_OPTIONS = [
  { label: '15 分钟', value: 15 * 60 },
  { label: '1 小时', value: 60 * 60 },
  { label: '24 小时', value: 24 * 60 * 60 },
];

/** UA 只保留能帮人认机器的部分：浏览器 + 平台。完整 UA 长得没有边界。 */
function browserLabel(ua: string): string {
  if (!ua) return '未知浏览器';
  const platform = ua.includes('Macintosh')
    ? 'macOS'
    : ua.includes('Windows')
      ? 'Windows'
      : ua.includes('Linux')
        ? 'Linux'
        : ua.includes('iPhone') || ua.includes('Android')
          ? '移动端'
          : '';
  const pairs: Array<[string, string]> = [
    ['Edg/', 'Edge'],
    ['OPR/', 'Opera'],
    ['Chrome/', 'Chrome'],
    ['Firefox/', 'Firefox'],
    ['Safari/', 'Safari'],
  ];
  for (const [needle, name] of pairs) {
    if (ua.includes(needle)) return platform ? `${name} · ${platform}` : name;
  }
  return ua.slice(0, 30);
}

const lifetime = (row: SessionRow) => {
  if (row.expired) return '已失效';
  if (row.remain_seconds < 0) return '长期有效';
  return humanDuration(row.remain_seconds);
};

export default function SessionsPane() {
  const [items, setItems] = useState<SessionRow[]>([]);
  const [online, setOnline] = useState(0);
  const [total, setTotal] = useState(0);
  const [currentId, setCurrentId] = useState('');
  const [at, setAt] = useState<number>(Date.now());
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  // 签发访问链接：控制台上不再有成员管理，「让某人进来」就在这个动作里
  const [issueOpen, setIssueOpen] = useState(false);
  const [who, setWho] = useState('');
  const [ttlSeconds, setTtlSeconds] = useState(60 * 60);
  const [issuing, setIssuing] = useState(false);
  const [link, setLink] = useState<{ url: string; who: string; remain: number } | null>(null);

  const load = useCallback(async () => {
    try {
      const r = await api.sessions();
      setItems(r.sessions);
      setOnline(r.online);
      setTotal(r.total);
      setCurrentId(r.current ?? '');
      setAt(Date.now());
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  // 5 秒一跳：在线窗口是 90 秒，这个间隔足以让台账跟手，又不至于把后台按在地上摩擦
  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 5_000);
    return () => window.clearInterval(timer);
  }, [load]);

  const act = async (row: SessionRow, kind: 'revoke' | 'delete') => {
    setBusy(row.id);
    try {
      if (kind === 'delete') {
        await api.deleteSession(row.id);
        MessagePlugin.success(row.id === currentId ? '已删除，你已退出登录' : `已删除：${row.user_name}`);
      } else {
        await api.revokeSession(row.id);
        MessagePlugin.success(row.id === currentId ? '当前会话已下线' : `已踢下线：${row.user_name}`);
      }
      await load();
    } catch (e) {
      MessagePlugin.error(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  };

  const issue = async () => {
    const name = who.trim();
    if (!name) return;
    setIssuing(true);
    try {
      const r = await api.issueInvite(name, ttlSeconds);
      setLink({
        url: `${window.location.origin}/#/login?token=${r.invite.token}`,
        who: r.user.name,
        remain: r.invite.remain_seconds,
      });
      setIssueOpen(false);
      setWho('');
      await load();
    } catch (e) {
      MessagePlugin.error(e instanceof Error ? e.message : String(e));
    } finally {
      setIssuing(false);
    }
  };

  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      MessagePlugin.success('已复制链接，发给使用者即可');
    } catch {
      MessagePlugin.warning('自动复制失败，请手动选中下方链接');
    }
  };

  const statusCell = (row: SessionRow) => {
    if (row.expired) {
      return (
        <Tag theme="danger" variant="light" size="small">
          已失效
        </Tag>
      );
    }
    return (
      <Space size="small" direction="vertical" align="start">
        {row.online ? (
          <Tag theme="success" variant="light" size="small">
            在线
          </Tag>
        ) : (
          <Tag theme="default" variant="light" size="small">
            离线
          </Tag>
        )}
        <span className="muted small">{humanAgo(row.last_seen_at)}活动</span>
      </Space>
    );
  };

  const columns: PrimaryTableCol<SessionRow>[] = [
    {
      colKey: 'user_name',
      title: '成员',
      width: 150,
      cell: ({ row }) => (
        <Space size="small" direction="vertical" align="start">
          <Space size="small">
            <span className="cell-strong">{row.user_name}</span>
            {row.id === currentId && (
              <Tag theme="primary" variant="light" size="small">
                本机
              </Tag>
            )}
          </Space>
          <Tag theme={row.role === 'admin' ? 'primary' : 'default'} variant="light" size="small">
            {row.role === 'admin' ? '管理员' : '成员'}
          </Tag>
        </Space>
      ),
    },
    {
      colKey: 'device',
      title: '设备',
      width: 200,
      cell: ({ row }) => (
        <Space size="small" direction="vertical" align="start">
          <span className="mono small">{row.device || '未识别'}</span>
          <span className="muted small">{browserLabel(row.user_agent)}</span>
        </Space>
      ),
    },
    {
      colKey: 'ip',
      title: '来源 IP',
      width: 130,
      cell: ({ row }) => <span className="mono small">{row.ip || '-'}</span>,
    },
    { colKey: 'status', title: '状态', width: 130, cell: ({ row }) => statusCell(row) },
    {
      colKey: 'created_at',
      title: '登录时间',
      width: 170,
      cell: ({ row }) => <span className="muted small">{humanTime(row.created_at)}</span>,
    },
    {
      colKey: 'expires_at',
      title: '寿命',
      width: 120,
      cell: ({ row }) => (
        <Tooltip content={row.max_idle ? `空闲上限 ${row.max_idle}` : '未设空闲上限'}>
          <span className="muted small">{lifetime(row)}</span>
        </Tooltip>
      ),
    },
    {
      colKey: 'op',
      title: '操作',
      width: 160,
      fixed: 'right',
      cell: ({ row }) => (
        <Space size="small">
          {!row.expired && (
            <Popconfirm
              content={row.id === currentId ? '下线后你也会立刻退出登录' : `让这台设备上的「${row.user_name}」退出登录`}
              theme="warning"
              onConfirm={() => void act(row, 'revoke')}
            >
              <Button size="small" variant="text" loading={busy === row.id}>
                下线
              </Button>
            </Popconfirm>
          )}
          <Popconfirm
            content={
              row.id === currentId
                ? '删除后你的浏览器里还留着 cookie，但服务端已经认不出它了'
                : '从台账里抹掉这条记录'
            }
            theme="danger"
            onConfirm={() => void act(row, 'delete')}
          >
            <Button size="small" variant="text" theme="danger" loading={busy === row.id}>
              删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <div className="workspace">
      {error && <div className="banner error">{error}</div>}

      <section className="panel">
        <div className="panel-head">
          <Space size="small" align="center">
            <h2>在线会话</h2>
            <Tag theme={online > 0 ? 'success' : 'default'} variant="light" size="small">
              {online} 在线
            </Tag>
            <span className="muted small">共 {total} 条</span>
          </Space>
          <Space size="small" align="center">
            <span className="muted small">{humanTime(new Date(at).toISOString())}</span>
            <Button size="small" variant="outline" onClick={() => void load()}>
              刷新
            </Button>
            <Tooltip content="给某人一条点开即登录的链接；他不需要口令">
              <Button size="small" theme="primary" onClick={() => setIssueOpen(true)}>
                签发访问链接
              </Button>
            </Tooltip>
          </Space>
        </div>

        <Table
          rowKey="id"
          size="small"
          loading={loading}
          data={items}
          columns={columns}
          empty="当前没有任何会话"
        />
        <div className="panel-foot muted small">
          按最后活跃时间倒序，每 5 秒刷新。服务端把超过 90 秒没有请求的会话判为离线 ——
          这是心跳的结果，和删除无关：一条会话可以长期在线，也可以长期有效但从不活动。
          想让谁进来就在这里签发一条链接：他没有口令，批过的那一刻才产生访问权。
        </div>
      </section>

      {/* 签发访问链接：只需要一个名字 */}
      <Dialog
        visible={issueOpen}
        onClose={() => setIssueOpen(false)}
        header="签发访问链接"
        width={480}
        footer={
          <Space size="small">
            <Button theme="primary" loading={issuing} disabled={!who.trim()} onClick={() => void issue()}>
              签发
            </Button>
            <Button variant="outline" onClick={() => setIssueOpen(false)}>
              取消
            </Button>
          </Space>
        }
      >
        <div className="stack">
          <label className="field">
            <span>使用者的名字</span>
            <Input
              value={who}
              autofocus
              placeholder="例如：张三（只用于标识这条会话属于谁）"
              onChange={setWho}
            />
          </label>
          <label className="field">
            <span>链接有效期</span>
            <Select value={ttlSeconds} options={TTL_OPTIONS} onChange={(v) => setTtlSeconds(Number(v))} />
          </label>
          <div className="muted small">
            签发后把链接发给他，点开即完成登录；链接用一次作废，到期自动失效。
          </div>
        </div>
      </Dialog>

      {/* 签发结果：链接本身就是全部秘密 */}
      <Dialog
        visible={link !== null}
        onClose={() => setLink(null)}
        header={`发给「${link?.who ?? ''}」的访问链接`}
        width={560}
        footer={
          <Space size="small">
            <Button theme="primary" onClick={() => link && void copy(link.url)}>
              复制链接
            </Button>
            <Button variant="outline" onClick={() => setLink(null)}>
              关闭
            </Button>
          </Space>
        }
      >
        <div className="stack">
          <Input value={link?.url ?? ''} readonly size="large" />
          <div className="muted small">
            {humanDuration(link?.remain ?? 0)}内有效 · 兑换一次即作废 ·
            这条链接本身就是全部秘密，转发时留意渠道
          </div>
        </div>
      </Dialog>
    </div>
  );
}
