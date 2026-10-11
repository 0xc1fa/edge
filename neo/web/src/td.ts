/**
 * TDesign 的按需出口。
 *
 * 整包 `import { Button } from 'tdesign-react'` 会把全量组件与全量样式都打进产物
 * （实测 gzip 从 54KB 涨到 208KB）。这里只注册真正用到的组件，
 * 子路径的 index 只导出组件 JS，样式统一在 main.tsx 里引入
 * `tdesign-react/dist/tdesign.css`，避免按需 import 几十条 style/css.js。
 *
 * 分两组：
 *   基础件 —— 按钮 / 输入 / 下拉 / 分段 / 表格 / 标签 / 提示
 *   布局件 —— 栅格 / 卡片 / 分隔 / 步骤 / 表单 / 描述列表 / 抽屉 / 统计
 * 布局也用框架，是为了不再出现「框架控件 + 手写布局」的混搭：
 * 栅格间距、卡片圆角、表单标签对齐这些细节由框架统一，页面只写信息层级。
 *
 * 类型仍从 'tdesign-react' 引入：`import type` 编译期就被擦掉，不进产物。
 */
export { default as Badge } from 'tdesign-react/es/badge';
export { default as Button } from 'tdesign-react/es/button';
export { default as Input } from 'tdesign-react/es/input';
export { default as Radio } from 'tdesign-react/es/radio';
export { default as Select } from 'tdesign-react/es/select';
export { default as Table } from 'tdesign-react/es/table';
export { default as Tabs } from 'tdesign-react/es/tabs';
export { default as Tag } from 'tdesign-react/es/tag';
export { default as Textarea } from 'tdesign-react/es/textarea';
export { MessagePlugin } from 'tdesign-react/es/message';

export { Col, Row } from 'tdesign-react/es/grid';
export { default as Alert } from 'tdesign-react/es/alert';
export { default as Card } from 'tdesign-react/es/card';
export { default as Descriptions } from 'tdesign-react/es/descriptions';
export { default as Divider } from 'tdesign-react/es/divider';
export { default as Drawer } from 'tdesign-react/es/drawer';
export { default as Form } from 'tdesign-react/es/form';
export { default as Skeleton } from 'tdesign-react/es/skeleton';
export { default as Loading } from 'tdesign-react/es/loading';
export { default as Space } from 'tdesign-react/es/space';
export { default as Statistic } from 'tdesign-react/es/statistic';
export { default as Steps } from 'tdesign-react/es/steps';
