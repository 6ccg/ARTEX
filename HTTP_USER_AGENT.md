# HTTP User-Agent

在“系统设置 → 网络 → HTTP User-Agent”修改默认请求标识，保存后无需重启。
留空时从 20 个标准桌面浏览器样本中随机选择，显式指定的请求头优先。

- WebFetch、资产补全、出口探测、自定义 HTTP 工具、重放器和自更新使用统一策略。
- 同一次请求的重定向、重试复用同一值；采集脚本每次运行复用同一值。
- 流量记录代理保留客户端已有的 UA，也保留未提供 UA 的语义。
- Go 与 Python 的样本保持一致。此功能只修改 HTTP 请求头。

两个独立采集脚本仍支持 `--user-agent` 显式覆盖。由 ARTEX 启动时，脚本继承
后台保存的设置；已经运行的脚本保持其启动时的值。

实现来自上游 PR #138；默认值保留其 2026-11-11 的 20 个浏览器样本。
配置统一保存在系统设置，未新增 `.env` 或 Docker 配置项。

验证：

```sh
go test ./internal/httpidentity ./internal/webfetch ./agent ./enrich ./traffic ./selfupdate
python -m unittest discover -s skills/api-recon/scripts -p 'test_user_agent.py'
```
