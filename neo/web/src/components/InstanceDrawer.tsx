import { Descriptions, Drawer, Table } from '../td';
import type { PrimaryTableCol } from 'tdesign-react';
import { humanBytes, humanPercent, humanTime, humanUptime } from '../format';
import { Sparkline, UsageBar } from './ui';
import type { InstanceView, Sample } from '../types';

interface Props {
  instance: InstanceView | null;
  series: Sample[];
  onClose: () => void;
}

/**
 * 详情抽屉。
 *
 * 分组照着宿主机侧 tenant-ctl.sh status 的信息结构排：
 * 运行状态 / 配额 / 磁盘占用 / GPU / 入口。这样两边对照时不用重新对齐心智。
 *
 * 抽屉本体与描述列表都走 TDesign，不再手写遮罩层和 kv 网格。
 */
export default function InstanceDrawer({ instance, series, onClose }: Props) {
  if (!instance) return null;

  const cpuSeries = series.map((s) => s.cpu_percent);
  const memSeries = series.map((s) => s.mem_usage);

  const nicColumns: PrimaryTableCol<InstanceView['nics'][number]>[] = [
    { colKey: 'name', title: '网卡', cell: ({ row }) => <span className="mono">{row.name}</span> },
    { colKey: 'ipv4', title: '地址', cell: ({ row }) => <span className="mono">{row.ipv4 || '-'}</span> },
    { colKey: 'hwaddr', title: 'MAC', cell: ({ row }) => <span className="mono small">{row.hwaddr || '-'}</span> },
    {
      colKey: 'io',
      title: '接收 / 发送',
      cell: ({ row }) => (
        <span className="mono small">
          {humanBytes(row.rx_bytes)} / {humanBytes(row.tx_bytes)}
        </span>
      ),
    },
  ];

  return (
    <Drawer
      visible
      onClose={onClose}
      // 固定 620px 在窄屏会横向溢出，这里按视口收一次
      size="min(620px, 94vw)"
      destroyOnClose
      header={
        <div>
          <h2>{instance.name}</h2>
          <p className="muted small">
            {instance.type_label} · {instance.architecture} · id {instance.pid || '-'}
          </p>
        </div>
      }
    >
      <div className="drawer-body">
        <Descriptions title="运行状态" column={2} size="small">
          <Descriptions.DescriptionsItem label="状态">{instance.status_label}</Descriptions.DescriptionsItem>
          <Descriptions.DescriptionsItem label="进程数">{instance.processes || '-'}</Descriptions.DescriptionsItem>
          <Descriptions.DescriptionsItem label="运行时长">{humanUptime(instance.uptime_seconds)}</Descriptions.DescriptionsItem>
          {/* CPU 累计是秒数，跑几天就是六位数，跟运行时长用同一套人性化写法 */}
          <Descriptions.DescriptionsItem label="CPU 累计">{humanUptime(instance.cpu_seconds)}</Descriptions.DescriptionsItem>
          <Descriptions.DescriptionsItem label="启动时间">{humanTime(instance.started_at)}</Descriptions.DescriptionsItem>
          <Descriptions.DescriptionsItem label="创建时间">{humanTime(instance.created_at)}</Descriptions.DescriptionsItem>
        </Descriptions>

        <Descriptions title="配额" column={2} size="small">
          <Descriptions.DescriptionsItem label="vCPU">
            <span className="mono">{instance.quota_cpu}</span>
          </Descriptions.DescriptionsItem>
          <Descriptions.DescriptionsItem label="内存">
            <span className="mono">{instance.quota_memory}</span>
          </Descriptions.DescriptionsItem>
          <Descriptions.DescriptionsItem label="根盘">
            <span className="mono">{instance.quota_disk}</span>
          </Descriptions.DescriptionsItem>
          <Descriptions.DescriptionsItem label="安全模板">{instance.profiles.join(', ') || '-'}</Descriptions.DescriptionsItem>
        </Descriptions>

        <h3>磁盘占用</h3>
        <UsageBar percent={instance.disk_percent} />
        <p className="muted small">
          {humanBytes(instance.disk_used)} / {humanBytes(instance.disk_total)} · 已用{' '}
          {humanPercent(instance.disk_percent)}
        </p>

        <h3>内存</h3>
        <UsageBar percent={instance.mem_percent} />
        <p className="muted small">
          {humanBytes(instance.mem_usage)} / {humanBytes(instance.mem_total)} · 已用{' '}
          {humanPercent(instance.mem_percent)}
          {instance.swap_usage > 0 ? ` · swap ${humanBytes(instance.swap_usage)}` : ''}
        </p>

        {instance.running && (
          <>
            <h3>走势（近 30 分钟）</h3>
            <div className="charts">
              <div>
                <p className="muted small">CPU 占用 %</p>
                <Sparkline values={cpuSeries} />
              </div>
              <div>
                <p className="muted small">内存用量</p>
                <Sparkline values={memSeries} />
              </div>
            </div>
          </>
        )}

        <h3>GPU</h3>
        {instance.gpus.length === 0 ? (
          <p className="muted small">未挂载 GPU</p>
        ) : (
          <ul className="list">
            {instance.gpus.map((g) => (
              <li key={g.device}>
                {/* 设备名（gpu0）对不上产品型号，型号优先，取不到才退回设备名 */}
                <span className="mono">{g.product || g.device}</span>
                <span className="muted small">
                  {g.device} · pci={g.pci || '通配'} · {g.type || 'physical'}
                </span>
              </li>
            ))}
          </ul>
        )}

        <h3>入口</h3>
        {instance.ingress.length === 0 ? (
          <p className="muted small">没有配置端口转发</p>
        ) : (
          <ul className="list">
            {instance.ingress.map((g) => (
              <li key={g.device}>
                <span className="mono">
                  {g.listen}:{g.port} → {g.connect}
                </span>
                <span className="muted small">
                  {/* 第一版登录账户恒为 root，不再给用户一个 <user> 占位符 */}
                  {g.purpose === 'ssh' ? `ssh -p ${g.port} root@${g.listen}` : g.purpose}
                </span>
              </li>
            ))}
          </ul>
        )}

        <h3>网络</h3>
        <Table rowKey="name" size="small" data={instance.nics} columns={nicColumns} />
      </div>
    </Drawer>
  );
}
