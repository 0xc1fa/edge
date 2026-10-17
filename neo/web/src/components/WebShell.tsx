import { useEffect, useState } from 'react';
import { Drawer } from '../td';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { api } from '../api';
import '@xterm/xterm/css/xterm.css';

interface Props {
  /** 要连的实例名；为空时不渲染 */
  instance: string;
  onClose: () => void;
}

/**
 * 网页终端。
 *
 * 走的是 Incus 的 exec 通道而不是转发 SSH：租户实例的 22 端口虽然开着，
 * 但那条路要口令、还要再实现一套 SSH 客户端；而登录态早就由控制台的
 * 会话 cookie 证明过了，再问一次口令只是多一个能忘的东西。
 *
 * 因此这里**不需要输入密码**—— 打开就连。服务端在那一边还会判
 * 「这台机器是不是你的」，越权时连接会被拒，终端里会显示原因。
 *
 * 尺寸同步不能省：不告诉容器 pty 变宽了，bash 的 readline 与 vim
 * 会按旧宽度折行，拖动浏览器窗口后光标会越来越偏。
 */
export default function WebShell({ instance, onClose }: Props) {
  // 用 state 存容器而不是 ref：Drawer 是首次打开时才把内容挂上 DOM 的，
  // 而 effect 跑的时候那个 div 往往还不存在。用 ref 的话
  // `if (!hostRef.current) return` 会把整个初始化静默跳过 ——
  // 页面表现就是「全黑一块，连光标都没有」，而控制台里一个错都没有。
  const [host, setHost] = useState<HTMLDivElement | null>(null);
  const [status, setStatus] = useState<'connecting' | 'open' | 'closed'>('connecting');

  useEffect(() => {
    if (!instance || !host) return;

    const term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      // 允许粘贴多行：往终端里贴一段脚本是常见操作，
      // 而 xterm 默认会把换行当成回车直接执行，那太危险
      convertEol: false,
      scrollback: 5000,
      theme: { background: '#0d1117', foreground: '#c9d1d9' },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(host);
    fit.fit();
    term.writeln('\x1b[2m正在连接 ' + instance + ' …\x1b[0m');

    const ws = new WebSocket(api.shellSocket(instance));
    ws.binaryType = 'arraybuffer';

    ws.onopen = () => {
      setStatus('open');
      // 连上之后先把当前尺寸报过去：容器那一边的 pty 默认 80x24，
      // 不报的话第一屏就会按错误宽度折行
      sendResize();
    };
    ws.onmessage = (ev) => {
      const data = typeof ev.data === 'string' ? ev.data : new Uint8Array(ev.data);
      term.write(data as string);
    };
    ws.onerror = () => {
      setStatus('closed');
      term.writeln('\r\n\x1b[31m连接出错\x1b[0m');
    };
    ws.onclose = () => {
      setStatus('closed');
      term.writeln('\r\n\x1b[2m连接已关闭\x1b[0m');
    };

    // 输入直接进容器。这里不做本地回显：pty 自己会回显，
    // 本地再回一次就是每个字符出现两次。
    const inputDisposable = term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(data);
    });

    const sendResize = () => {
      if (ws.readyState !== WebSocket.OPEN) return;
      // 尺寸走控制消息：格式是 Incus 约定的 JSON，
      // 不是终端数据 —— 混在 stdin 里送进去会被当成用户输入。
      ws.send(
        JSON.stringify({
          command: ['window-resize'],
          args: { width: term.cols, height: term.rows },
        }),
      );
    };

    const resizeDisposable = term.onResize(sendResize);
    const onWindowResize = () => fit.fit();
    window.addEventListener('resize', onWindowResize);

    return () => {
      window.removeEventListener('resize', onWindowResize);
      resizeDisposable.dispose();
      inputDisposable.dispose();
      fit.dispose();
      term.dispose();
      ws.close();
    };
  }, [instance, host]);

  return (
    <Drawer
      visible={!!instance}
      onClose={onClose}
      header={`网页终端 · ${instance}`}
      size="680px"
      footer={
        <div className="muted small">
          {status === 'open'
            ? '已连接 · 退出请键入 exit 或 Ctrl-D'
            : status === 'connecting'
              ? '正在连接…'
              : '连接已断开，关闭后重开即可'}
        </div>
      }
    >
      <div className="shell-host" ref={setHost} />
    </Drawer>
  );
}
