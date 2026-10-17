import { useEffect, useRef, useState } from 'react';
import { Button, Input, MessagePlugin } from '../td';
import { ApiError, api } from '../api';
import { deviceTag } from '../fingerprint';
import { BRAND } from './ui';

interface Props {
  onSuccess: () => void;
  onBack?: () => void;
  /** 从 `#/login?token=xxx` 带过来的凭证；有值时打开页面即自动兑换 */
  token?: string;
  admin?: boolean;
}

/**
 * 登录卡：管理员走口令，其他人走链接。
 *
 * 控制台只认一个口令，它属于部署者本人，明文躺在数据目录里（data/admin-password.txt），
 * 忘了就 cat 出来，不存在「找回密码」这条链路。
 *
 * 其他使用者没有口令。他们能不能进来，取决于管理员有没有批过一条链接 ——
 * 链接点开即兑换成会话，批过的那一刻才产生访问权。
 * 没批过的人停在这一页上：这里既不放产品目录，也不透露任何实例信息。
 *
 * 两种入口共用一张卡是有意的：口令框始终在，凭证失效时管理员不必再找出口。
 */
export default function LoginCard({ token = '', onSuccess, onBack }: Props) {
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // effect 里要读最新的回调，但不希望它变化就重新触发兑换，
  // 所以用 ref 转一层：凭证只能用一次，重复提交只会得到「已失效」。
  const done = useRef(onSuccess);
  done.current = onSuccess;

  useEffect(() => {
    if (!token) return;
    let alive = true;
    setBusy(true);
    (async () => {
      try {
        const r = await api.redeem(token, deviceTag());
        if (!alive) return;
        MessagePlugin.success(r.user ? `已登录：${r.user.name}` : '登录成功');
        // 凭证已经作废，别把它留在地址栏与浏览器历史里
        window.location.hash = '#/admin';
        done.current();
      } catch (e) {
        if (alive) {
          const msg = e instanceof ApiError ? e.message : '链接无效，请重新向管理员要一条';
          setError(msg);
        }
      } finally {
        if (alive) setBusy(false);
      }
    })();
    return () => {
      alive = false;
    };
  }, [token]);

  const submit = async (value: string) => {
    if (!value) return;
    setBusy(true);
    setError(null);
    try {
      await api.login(value, deviceTag());
      setPassword('');
      MessagePlugin.success('已登录');
      done.current();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '登录失败，请重试');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-wrap">
      <form
        className="login-card"
        onSubmit={(e) => {
          e.preventDefault();
          void submit(password);
        }}
      >
        <h1 className="login-title">{BRAND}</h1>
        <p className="login-sub">控制台 · 管理员口令</p>

        <label className="field">
          <span>管理员口令</span>
          <Input
            type="password"
            value={password}
            autofocus={!token}
            placeholder="输入管理员口令"
            onChange={setPassword}
            disabled={busy}
          />
        </label>

        {error && <p className="form-error">{error}</p>}

        <Button theme="primary" block type="submit" loading={busy} disabled={password === ''}>
          登录
        </Button>

        <p className="login-hint">
          其他人不需要口令：由管理员在「会话」页签发一条访问链接，
          点开即完成登录。链接用一次作废，没有获批就停在这一页。
        </p>

        {onBack && (
          <p className="login-hint">
            <Button variant="text" size="small" type="button" onClick={onBack}>
              ← 返回产品主页
            </Button>
          </p>
        )}
      </form>
    </div>
  );
}
