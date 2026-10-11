import { useCallback, useEffect, useState } from 'react';
import { Badge, Button, Tabs } from '../td';
import { api } from '../api';
import { BRAND, Logo } from './ui';
import ApplicationsPane from './ApplicationsPane';
import Dashboard from './Dashboard';
import LoginCard from './LoginCard';

interface Props {
  onBack: () => void;
}

type Tab = 'instances' | 'applications';

/**
 * 管理员控制台。
 *
 * 与公开站点的分界线是「数据必须带会话 cookie」：第一道关卡是这里的登录门，
 * 第二道是后端对 /api/* 的 401 收口。少了后一道，前端登录页就只是个摆设。
 */
export default function Admin({ onBack }: Props) {
  const [authenticated, setAuthenticated] = useState<boolean | null>(null);
  const [tab, setTab] = useState<Tab>('instances');
  const [pending, setPending] = useState(0);

  useEffect(() => {
    api
      .session()
      .then((s) => setAuthenticated(s.authenticated))
      .catch(() => setAuthenticated(false));
  }, []);

  // 待办数量要常显：管理员不该为了知道有没有新申请而反复点进申请页
  useEffect(() => {
    if (!authenticated) return;
    const pull = () =>
      api
        .applications()
        .then((r) => setPending(r.pending))
        .catch(() => undefined);
    pull();
    const timer = window.setInterval(pull, 15_000);
    return () => window.clearInterval(timer);
  }, [authenticated]);

  const logout = useCallback(async () => {
    await api.logout().catch(() => undefined);
    setAuthenticated(false);
  }, []);

  if (authenticated === null) {
    return <div className="center muted">正在确认登录状态…</div>;
  }

  if (!authenticated) {
    return <LoginCard onSuccess={() => setAuthenticated(true)} onBack={onBack} admin />;
  }

  return (
    <>
      <header className="topbar">
        <div className="brand">
          <Logo animated />
          <span>{BRAND} · 管理员控制台</span>
        </div>
        <div className="topbar-right">
          <Button variant="text" onClick={onBack}>
            查看产品主页
          </Button>
          <Button variant="text" onClick={logout}>
            退出
          </Button>
        </div>
      </header>
      <main className="app">
        <Tabs value={tab} onChange={(v) => setTab(v as Tab)} className="app-tabs">
          <Tabs.TabPanel value="instances" label="实例" destroyOnHide={false}>
            <Dashboard />
          </Tabs.TabPanel>
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
        </Tabs>
      </main>
    </>
  );
}
