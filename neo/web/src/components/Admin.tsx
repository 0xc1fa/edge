import { useCallback, useEffect, useState } from 'react';
import { Badge, Button, Tabs } from '../td';
import { api } from '../api';
import { BRAND, Logo } from './ui';
import ApplicationsPane from './ApplicationsPane';
import Dashboard from './Dashboard';
import GpuPane from './GpuPane';
import LoginCard from './LoginCard';
import SessionsPane from './SessionsPane';
import type { InstanceView, SessionInfo } from '../types';

interface Props {
  onBack: () => void;
  /** `#/login?token=` 带进来的一次性凭证；有值时交给登录卡自动兑换 */
  token?: string;
}

type Tab = 'instances' | 'gpu' | 'applications' | 'sessions';

/**
 * 控制台。
 *
 * 与公开站点的分界线是「数据必须带会话 cookie」：第一道关卡是这里的登录门，
 * 第二道是后端对 /api/* 的 401 收口。少了后一道，前端登录页就只是个摆设。
 *
 * 进来的人只看得到三类东西：实例、申请、会话。
 * 没有「成员管理」这一页 —— 让谁进来，是在会话页上签一条链接，
 * 而不是先去某个页面建档再回来发证。身份只有管理员一个，
 * 其他人全靠那条链接，没批过就停在登录页上。
 */
export default function Admin({ onBack, token = '' }: Props) {
  const [me, setMe] = useState<SessionInfo | null>(null);
  const [unknown, setUnknown] = useState(true);
  const [tab, setTab] = useState<Tab>('instances');
  const [pending, setPending] = useState(0);
  // 实例名单给 GPU 申请抽屉用：申请必须挂在某台具体的机器上
  const [instances, setInstances] = useState<InstanceView[]>([]);

  const refresh = useCallback(async () => {
    try {
      setMe(await api.session());
    } catch {
      setMe(null);
    } finally {
      setUnknown(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // 心跳。服务端把「90 秒没有请求」当作离开，所以这里要定期说一句我还在；
  // 页面上其它轮询会顺带完成这件事，这一层只是为了停在某个标签不动时也不掉线。
  useEffect(() => {
    if (!me?.authenticated) return;
    const timer = window.setInterval(() => void refresh(), 20_000);
    return () => window.clearInterval(timer);
  }, [me?.authenticated, refresh]);

  useEffect(() => {
    if (!me?.authenticated) return;
    api
      .instances()
      .then((r) => setInstances(r.instances))
      .catch(() => undefined);
  }, [me?.authenticated]);

  // 待办数量要常显：管理员不该为了知道有没有新申请而反复点进申请页
  useEffect(() => {
    if (!me?.authenticated || me.role !== 'admin') return;
    const pull = () =>
      api
        .applications()
        .then((r) => setPending(r.pending))
        .catch(() => undefined);
    pull();
    const timer = window.setInterval(pull, 15_000);
    return () => window.clearInterval(timer);
  }, [me?.authenticated, me?.role]);

  const logout = useCallback(async () => {
    await api.logout().catch(() => undefined);
    setMe(null);
  }, []);

  if (unknown) {
    return <div className="center muted">正在确认登录状态…</div>;
  }

  if (!me?.authenticated) {
    return <LoginCard token={token} onSuccess={() => void refresh()} onBack={onBack} admin />;
  }

  const isAdmin = me.role === 'admin';

  return (
    <>
      <header className="topbar">
        <div className="brand">
          <Logo animated />
          <span>{BRAND}</span>
        </div>
        <div className="topbar-right">
          <span className="muted small">
            {me.user?.name ?? '已登录'}
            {isAdmin ? ' · 管理员' : ' · 成员'}
          </span>
          <Button variant="text" onClick={onBack}>
            查看产品主页
          </Button>
          <Button variant="text" onClick={logout}>
            退出
          </Button>
        </div>
      </header>
      <main className="app">
        <Tabs
          value={tab}
          onChange={(v) => setTab(v as Tab)}
          className="app-tabs"
        >
          <Tabs.TabPanel value="instances" label="实例" destroyOnHide={false}>
            <Dashboard />
          </Tabs.TabPanel>
          {/*
            GPU 排期对所有人开放：成员要回答的第一件事就是「我想约那天还有没有位子」，
            看不到别人的占用就只能一遍遍提交、一遍遍被拒。
            审批与挂载按钮由 canManage 决定在不在，后端还会再挡一道。
          */}
          <Tabs.TabPanel value="gpu" label="GPU" destroyOnHide={false}>
            <GpuPane
              canManage={isAdmin}
              me={me.user?.name ?? ''}
              instances={instances}
              onChanged={() => undefined}
            />
          </Tabs.TabPanel>
          {/*
            申请审批只属于管理员。
            这里必须收口：后端对 GET /api/applications 判定的是 levelAdmin，
            把面板摆给成员只会让他看到一个必然报 403 的空壳 ——
            而申请人自己的进度，走的是产品页上凭回执号自助查询那条路。
          */}
          {isAdmin && (
            <Tabs.TabPanel
              value="applications"
              destroyOnHide={false}
              label={
                <span className="tab-label">
                  申请
                  {pending > 0 && <Badge count={pending} size="small" />}
                </span>
              }
            >
              <ApplicationsPane />
            </Tabs.TabPanel>
          )}
          {isAdmin && (
            <Tabs.TabPanel value="sessions" label="会话" destroyOnHide={false}>
              <SessionsPane />
            </Tabs.TabPanel>
          )}
        </Tabs>
      </main>
    </>
  );
}
