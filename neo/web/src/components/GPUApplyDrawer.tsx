import { useEffect, useState } from 'react';
import { Button, DatePicker, Drawer, Input, MessagePlugin, Space } from '../td';
import { ApiError, api } from '../api';
import type { InstanceView } from '../types';

interface Props {
  /** 预选的实例名；为空时由使用者在下拉里选 */
  instance?: string;
  /** 候选实例，用于有得选的场景 */
  instances?: InstanceView[];
  /** 申请人的默认名字（当前登录者），可以改 */
  defaultApplicant?: string;
  onClose: () => void;
  onDone: () => void;
}

const today = () => new Date().toISOString().slice(0, 10);

/**
 * 申请 GPU。
 *
 * 只问三件事：哪台机器、哪一天、用来做什么。
 * 钟点不问 —— 卡挂上去之后由服务端在当天 16:00 摘下，问了反而让人以为可以调。
 *
 * 提交之后不会自动挂卡：这里产生的是一条待批准的预约，
 * 管理员批准之后再点挂载。把「申请」和「拿到卡」分开，
 * 是为了让人在点上「提交」的时候知道接下来还要等一下，而不是以为已经能用。
 */
export default function GPUApplyDrawer({
  instance = '',
  instances = [],
  defaultApplicant = '',
  onClose,
  onDone,
}: Props) {
  const [target, setTarget] = useState(instance);
  const [date, setDate] = useState(today());
  const [applicant, setApplicant] = useState(defaultApplicant);
  const [contact, setContact] = useState('');
  const [purpose, setPurpose] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // 从实例行点进来时实例是确定的，抽屉每次打开都要跟上最新的那一次点击
  useEffect(() => {
    if (instance) setTarget(instance);
  }, [instance]);
  useEffect(() => {
    if (defaultApplicant) setApplicant(defaultApplicant);
  }, [defaultApplicant]);

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const r = await api.applyGPU({
        instance: target,
        applicant,
        contact,
        purpose,
        date,
        card_count: 1,
      });
      MessagePlugin.success(`已登记：${r.booking.id}`);
      onDone();
      onClose();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '提交失败，请重试');
    } finally {
      setBusy(false);
    }
  };

  const ready = target.trim() !== '' && applicant.trim() !== '' && !busy;

  return (
    <Drawer
      visible
      onClose={onClose}
      header="申请 GPU"
      size="small"
      footer={
        <Space size="small">
          <Button theme="primary" loading={busy} disabled={!ready} onClick={() => void submit()}>
            提交申请
          </Button>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
        </Space>
      }
    >
      <div className="stack">
        <label className="field">
          <span>实例</span>
          {instance ? (
            // 从某一行点进来的，就不再让他选了：这台机器是他刚看着的那台
            <Input value={target} readonly />
          ) : (
            <select className="native-select" value={target} onChange={(e) => setTarget(e.target.value)}>
              <option value="">请选择…</option>
              {instances.map((i) => (
                <option key={i.name} value={i.name}>
                  {i.name}
                  {i.gpus.length ? '（已挂 ' + i.gpus.length + ' 张）' : ''}
                </option>
              ))}
            </select>
          )}
        </label>

        <label className="field">
          <span>日期</span>
          <DatePicker
            value={date}
            format="YYYY-MM-DD"
            // 过去的日期服务端也会拒，这里先挡一道，省一次往返。
            // DisableDate 的入参可能是字符串也可能是 Date，统一转成 Date 再比
            disableDate={(v) => {
              const d = v instanceof Date ? v : new Date(String(v));
              const t = new Date();
              t.setHours(0, 0, 0, 0);
              return d.getTime() < t.getTime();
            }}
            onChange={(v) => setDate(String(v))}
          />
          <span className="muted small">卡挂上后由服务端在当天 16:00 自动摘下，不需要选钟点</span>
        </label>

        <label className="field">
          <span>申请人</span>
          <Input value={applicant} placeholder="你的名字" onChange={setApplicant} />
        </label>

        <label className="field">
          <span>联系方式（选填）</span>
          <Input value={contact} placeholder="便于管理员有疑问时联系" onChange={setContact} />
        </label>

        <label className="field">
          <span>用途</span>
          <Input value={purpose} placeholder="例如：跑一轮 7B 微调" onChange={setPurpose} />
        </label>

        {error && <div className="banner error">{error}</div>}

        <div className="muted small">
          提交后由管理员批准并挂载；当天的 16:00 会自动摘卡。
          到期前请留意不要留下跑不完的任务。
        </div>
      </div>
    </Drawer>
  );
}
