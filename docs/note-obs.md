# obs 监控

## 🔔 通过钉钉接收告警通知 260826

```text
prometheus(规则) → alertmanager(路由 group_by: alertname,job)
   → webhook-dingtalk 桥接(host 网络, 端口 8060)
   → 钉钉群机器人(markdown, 加签)
```

- 路由按 `alertname + job` 分组，`group_wait 30s` / `repeat_interval 4h`——同一告警 4 小时内不重复轰炸
- 消息文案由 `template/default.tmpl` 控制：title（`obs-alert.title`：监控告警/告警恢复）+ text（`obs-alert.message`），有 title 即按 markdown 发送

**展示格式定位**：不在告警规则里，而在钉钉桥接服务的消息模板 `obs/template/default.tmpl`：

```go
// 最终态（主题与详细分行）
**{{ .Labels.alertname }}**
{{ .Annotations.summary }}
```

**踩坑：模板目录只读挂载，改完必须重启才生效**。`docker-compose.yml` 中 `./template:/etc/.../template:ro`——文件改动实时同步进容器，但 prometheus-webhook-dingtalk 在**启动时**加载模板，不重启不会重新解析：

```bash
docker restart webhook-dingtalk
```

**验证认知**：钉钉 markdown 单换行即可正常换行显示，无需 `<br/>` 或空行。

---

## 🐳 监控服务迁移 260825

> 监控栈从原 monitoring 项目迁入 edge 仓库统一管理——编排、规则、模板随仓库走，敏感凭据不入库；数据卷复用保证历史指标不丢。

参考现在目录的简短命名风格，按推荐度排序：
obs	observability 缩写，最贴合现代术语、最短	推荐，未来可能扩展日志/链路
telemetry 强调「采集」—exporter 主动上报指标
metrics	直白表达「指标」	如果确定只做指标不做日志
prom-stack	体现技术栈	想突出 Prometheus/Grafana 时
prom-grafana	同上但更啰嗦	不推荐
observe 语义没问题，但有点被动感，且易与 observer/observation 混淆
