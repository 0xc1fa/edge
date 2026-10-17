import { Suspense, lazy, useEffect, useState } from 'react';
import ApplyPage from './components/ApplyPage';
import PublicSite from './components/PublicSite';

/**
 * 路由有四个面：
 *
 *   `#/`               公开产品页 —— 无访问控制，任何人可浏览规格
 *   `#/apply?...`      申请页 —— 从产品页带着选型进来，独立成页
 *   `#/login?token=`   登录页 —— 凭证由管理员在后台签发后发给使用者的那条链接
 *   `#/admin`          控制台 —— 登录后才能看到实例与申请人的联系方式
 *
 * 登录为什么独立成页而不是挂在 /admin 里：凭证要放在 URL 上才能一次点开就登进来，
 * 而 #/admin 是不该外发的地址。分开之后，/admin 保持匿名不可读，
 * 使用者拿到的是一条让别人看不懂、自己也只用一次的链接。
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

function parseHash(hash: string): { view: string; token: string; query: ApplyQuery } {
  const [path, search] = hash.replace(/^#/, '').split('?');
  const p = new URLSearchParams(search ?? '');
  return {
    view: path || '/',
    token: p.get('token') ?? '',
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

  const { view, token, query } = parseHash(hash);

  const goHome = () => {
    window.location.hash = '#/';
  };

  // 控制台的两个入口共用同一个组件：未登录时它自己会落到登录卡上
  if (view.startsWith('/admin') || view.startsWith('/login')) {
    return (
      <Suspense fallback={<div className="center muted">正在加载控制台…</div>}>
        <Admin onBack={goHome} token={view.startsWith('/login') ? token : ''} />
      </Suspense>
    );
  }

  if (view.startsWith('/apply')) {
    return <ApplyPage query={query} onBack={goHome} />;
  }

  return <PublicSite onAdmin={() => (window.location.hash = '#/admin')} />;
}
