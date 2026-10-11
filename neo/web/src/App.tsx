import { Suspense, lazy, useEffect, useState } from 'react';
import ApplyPage from './components/ApplyPage';
import PublicSite from './components/PublicSite';

/**
 * 路由只有三个面：
 *
 *   `#/`               公开产品页 —— 无访问控制，任何人可浏览规格
 *   `#/apply?...`      申请页 —— 从产品页带着选型进来，独立成页
 *   `#/admin`          管理员控制台 —— 登录后才能看到实例与申请人的联系方式
 *
 * 用 hash 而不是 pathname：静态资源由 Go 同源托管，hash 不产生额外的服务端路由，
 * 将来套进 Tauri 桌面壳也不会因为子路径找不到 index.html。
 *
 * 控制台走懒加载：它才用到 TDesign 的表格与标签页组件，而这两个几乎把整库拖进来。
 * 分开打包后，公开页不必为后台那一堆组件付流量。
 */
const Admin = lazy(() => import('./components/Admin'));

/** 申请页的选型从 URL 带过来：`#/apply?flavor=t2.small&gpu=rtx-4090&cards=1` */
interface ApplyQuery {
  flavor: string;
  gpu: string;
  cards: number;
}

function parseHash(hash: string): { view: string; query: ApplyQuery } {
  const [path, search] = hash.replace(/^#/, '').split('?');
  const p = new URLSearchParams(search ?? '');
  return {
    view: path || '/',
    query: {
      flavor: p.get('flavor') ?? '',
      gpu: p.get('gpu') ?? '',
      cards: Number(p.get('cards') ?? '1') || 1,
    },
  };
}

export default function App() {
  const [hash, setHash] = useState(() => window.location.hash);

  useEffect(() => {
    const onHashChange = () => setHash(window.location.hash);
    window.addEventListener('hashchange', onHashChange);
    return () => window.removeEventListener('hashchange', onHashChange);
  }, []);

  const { view, query } = parseHash(hash);

  const goHome = () => {
    window.location.hash = '#/';
  };

  if (view.startsWith('/admin')) {
    return (
      <Suspense fallback={<div className="center muted">正在加载控制台…</div>}>
        <Admin onBack={goHome} />
      </Suspense>
    );
  }

  if (view.startsWith('/apply')) {
    return <ApplyPage query={query} onBack={goHome} />;
  }

  return <PublicSite onAdmin={() => (window.location.hash = '#/admin')} />;
}
