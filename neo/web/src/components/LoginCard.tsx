import { useState } from 'react';
import { Button, Input } from '../td';
import { ApiError, api } from '../api';
import { BRAND } from './ui';

interface Props {
  onSuccess: () => void;
  onBack?: () => void;
  admin?: boolean;
}

/**
 * 登录卡。
 *
 * 管理员与公开站点共用同一枚口令：后台验证通过的人就是平台管理员。
 * 这是第一版刻意简化的模型 —— 没有多用户体系，也就没有「自助注册」这条路径。
 */
export default function LoginCard({ onSuccess, onBack, admin = false }: Props) {
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.login(password);
      setPassword('');
      onSuccess();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '登录失败，请重试');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-wrap">
      <form className="login-card" onSubmit={submit}>
        {admin && <p className="muted small">平台管理员入口</p>}
        <h1 className="login-title">{BRAND}控制台</h1>
        <p className="login-sub">
          {admin
            ? '此页面含全部实例与申请人的联系方式，需要验证身份'
            : '输入后台管理员口令，验证通过即成为平台管理员'}
        </p>

        <label className="field">
          <span>管理员口令</span>
          <Input
            type="password"
            value={password}
            autofocus
            autocomplete="current-password"
            placeholder="粘贴首发生成的口令"
            onChange={setPassword}
            disabled={busy}
          />
        </label>

        {error && <p className="form-error">{error}</p>}

        <Button theme="primary" block type="submit" loading={busy} disabled={password === ''}>
          登录
        </Button>

        <p className="login-hint">
          口令由服务端在首次启动时生成，保存在数据目录的 <code>bootstrap-password.txt</code>；
          该文件由管理员自行留存或删除。
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
