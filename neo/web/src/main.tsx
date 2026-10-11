import React from 'react';
import { createRoot } from 'react-dom/client';
// TDesign 的样式必须排在自定义样式之前，否则下面这层覆盖不住它
import 'tdesign-react/dist/tdesign.css';

import './styles.css';
import App from './App';

const el = document.getElementById('root');
if (!el) throw new Error('找不到挂载点 #root');

// 主色在 styles.css 里改 --td-* 变量，其余沿用 TDesign 默认值：
// 组件细节（间距、圆角、hover、focus）交给框架，这里不再手写第二套
createRoot(el).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
