# Domain Dispatcher

[English](README_EN.md)

使用 Go 编写的 HTTP/HTTPS 请求路由器：监听一个或多个端口，依据请求的 `Host` 头分发流量，
支持反向代理（`forward`）、HTTP 302 重定向（`redirect`）和本地预设响应（`mock`）三种方式。

构建、跨平台发布包以及 Linux/systemd 部署方式请参见 [DEPLOY.md](DEPLOY.md)。

## 功能特性

- **按域名路由**：精确匹配 `Host` 头，并支持用 `*` 作为通配兜底规则
- **路径匹配**：每条规则可选 `path` 过滤，支持精确匹配（`/health`）或前缀通配（`/api/*`）
- **手动规则优先级**：`priority` 会叠加到自动匹配得分上，用于排定相互重叠的规则
- **多种分发类型**：`forward`（反向代理）、`redirect`（302 重定向）、`mock`（本地预设响应）
- **请求头控制**：转发前移除指定请求头；可选添加 `X-Forwarded-*`
- **适配 SSE 的转发**：流式响应逐块 flush 输出，且不与上游协商压缩，因此事件即时送达、不会成批堆积
- **超时配置**：上游拨号、响应头、正文空闲、正文总时长，以及服务端读请求头、空闲超时可全部配置
- **HTTP 与 HTTPS 监听**：TLS 证书与域名绑定，按分发规则逐域名配置，握手时依据 SNI 自动选择对应证书
- **来源 IP 黑名单**：以 403 拒绝指定 IP 或 CIDR 网段
- **优雅关闭**：收到 SIGINT/SIGTERM 后停止接受新连接，并在 `server.shutdown_timeout` 内处理完在途请求
- **每请求摘要日志**：每个请求（forward、redirect、mock 以及 404）都输出一行摘要
- **可选文件日志**：通过 `global.log` 按文件大小滚动
- **YAML 配置**：可配置多个监听端口，每个监听器拥有各自的规则集

## 快速开始

```bash
# 为当前平台构建
make build

# 基于纳入版本管理的模板创建本地配置（config.yaml 已被 gitignore）
cp config.example.yaml config.yaml

# 使用本地配置启动（省略 -c 时的默认路径）
./build/domain-dispatcher -c config.yaml
```

把测试域名指向本机（例如在 `/etc/hosts` 中添加）：

```
127.0.0.1 abc-api.com
```

然后即可通过分发器转发请求（替换 `-H "Host: ..."` 的值即可验证不同规则）：

```bash
# forward 规则
curl -H "Host: abc-api.com" http://127.0.0.1:8080/hello

# mock 规则
curl -H "Host: mock-api.com" http://127.0.0.1:8080/health

# 未匹配的域名 -> 404
curl -H "Host: unknown.com" http://127.0.0.1:8080/
```

## 命令行选项

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-c, --config` | `config.yaml` | YAML 配置文件路径（可从 `config.example.yaml` 复制创建） |
| `--version` | - | 打印版本号与构建时间后退出 |

退出码：收到 SIGINT/SIGTERM 并优雅关闭后为 `0`；监听器启动失败或配置非法时为 `1`。

## 配置

完整且带注释的配置说明请见 `config.example.yaml`。其中给出的每个值都是内置默认值，
因此省略某个键即沿用其默认配置。最小示例：

```yaml
global:
  forward:
    request_timeout: 60s             # 等待上游响应头的最长时间
    stream_idle_timeout: 120s        # 正文停滞达到该时长即中断（0s 表示关闭）
    response_body_timeout: 300s      # 非流式正文的总时长上限（0s 表示关闭）
  server:
    shutdown_timeout: 30s
  log:                               # 可选；不配置则输出到 stderr
    dir: /var/log/domain-dispatcher
    filename: app.log
  add_proxy_headers: true
  source_ip_blacklist:
    - 203.0.113.7
    - 192.168.0.0/16
  debug: false

listeners:
  - port: 8080
    type: http
    dispatch:
      - domain: abc-api.com
        forward_to: https://abc.com/api
      - domain: xyz-api.com
        path: /api/*                 # 仅匹配 /api/ 下的请求
        forward_to: https://xyz.com/backend
      - domain: mock-api.com
        path: /health
        type: mock                   # 本地响应，不回源
        priority: 100                # 在自动匹配得分之上加权
        mock_status: 200
        mock_headers:
          Content-Type: application/json
        mock_body: '{"status":"healthy"}'
      - domain: old-api.com
        forward_to: https://new-api.com
        type: redirect

  - port: 8443
    type: https
    dispatch:
      - domain: abc-api.com
        forward_to: https://abc.com/api
        tls:                         # 证书按域名逐条配置（https 监听器）
          cert_file: /etc/domain-dispatcher/certs/abc-api.com.crt
          key_file: /etc/domain-dispatcher/certs/abc-api.com.key
      - domain: "*"                  # 兜底规则；其证书同时作为未知 SNI 的兜底证书
        forward_to: https://abc.com/api
        tls:
          cert_file: /etc/domain-dispatcher/certs/default.crt
          key_file: /etc/domain-dispatcher/certs/default.key
```

### 配置字段说明

- `global.forward`：转发引擎的共享参数（连接池、拨号/TLS 超时、`request_timeout`、
  `stream_idle_timeout`、`response_body_timeout`、`streaming_flush`、
  `disable_compression`、`force_attempt_http2`、`buffer_size`）——名称与默认值与
  `domain-forwarder` 保持一致
- `global.server`：作用于所有监听器的 HTTP 服务端设置（`read_header_timeout`、
  `idle_timeout`、`max_header_bytes`、`shutdown_timeout`）
- `global.tls`：`type: https` 监听器使用的证书（`cert_file`、`key_file`）；
  启动校验会在文件不存在时报错
- `global.log`：可选的带滚动的文件日志（`dir`、`filename`、`max_size`（MB）、
  `max_backups`）；`dir`/`filename` 为空时日志输出到 stderr
- `global.source_ip_blacklist`：以 403 拒绝的客户端 IP 或 CIDR 网段
- `global.add_proxy_headers`：为转发的请求添加 `X-Forwarded-For`/`X-Forwarded-Proto`/
  `X-Forwarded-Host`（默认 true）
- `global.debug`：输出额外的每请求明细日志（摘要日志始终输出）
- `listeners`：监听器数组
  - `port`：监听端口（1-65535）
  - `type`：`http`（默认）或 `https`
  - `dispatch`：分发规则数组
    - `domain`：待匹配的域名（不含协议和端口）；`*` 匹配任意域名
    - `path`：可选的路径模式：精确匹配（`/health`）或前缀通配（`/api/*`）
    - `forward_to`：目标 URL（`forward` 与 `redirect` 类型必填）
    - `type`：`forward`（默认）、`redirect` 或 `mock`
    - `priority`：可选的整数，叠加到自动匹配得分上
    - `tls`：可选的证书配置（`cert_file`、`key_file`，仅 `https` 监听器生效）。
      TLS 证书与域名绑定，因此按规则逐域名配置：TLS 握手时依据客户端的 SNI 服务器名
      呈现对应域名的证书；`*` 规则的证书作为未知域名及未发送 SNI 客户端的兜底证书，
      未配置兜底时这些连接握手失败。`https` 监听器要求至少一条规则配置证书，
      启动校验会在证书文件不存在或同域名配置了不同证书时报错
    - `remove_headers`：转发前需要移除的请求头（仅 `forward` 类型生效）
    - `mock_status` / `mock_headers` / `mock_body`：预设响应定义（仅 `mock` 类型生效）

## 工作原理

1. 程序为每个配置的监听器各启动一个 HTTP 服务
2. 从 `Host` 头中提取域名（去掉端口，并兼容 IPv6 写法）
3. 监听器按域名/路径得分加上可选的 `priority`，选出最优匹配规则
4. `forward` 将请求代理到目标 URL，`redirect` 返回 302，`mock` 在本地返回预设响应
5. 来源 IP 命中黑名单的请求返回 403；域名未匹配的请求返回 404

匹配优先级（由高到低）：精确域名 > 通配域名；精确路径 > 前缀通配路径 > 不带路径。
综合得分最高的规则胜出。

## 请求转发细节

- **方法**：保持不变（GET、POST、PUT、DELETE 等）
- **请求头**：除逐跳（hop-by-hop）请求头和 `Host`（改写为目标主机）外，其余请求头全部复制；
  `Connection` 头中列出的那些请求头也会被剥离。当 `disable_compression: true`（默认）时，
  客户端的 `Accept-Encoding` 不会透传给上游
- **路径与查询串**：保留原始路径和查询参数
- **请求体**：原样转发，并保留 `Content-Length`
- **协议**：支持 HTTP 与 HTTPS 之间的互相转发
- **流式响应**：`text/event-stream` 响应逐块 flush 输出
- **失败处理**：响应尚未开始就发生转发失败，返回 502 Bad Gateway；响应已经开始之后的失败仅记录日志

## 项目结构

```
domain-dispatcher/
├── main.go                      # 命令行参数、启动流程、优雅关闭
├── config.go                    # 配置类型、默认值、加载与校验
├── handler.go                   # 请求处理、规则匹配、摘要日志
├── pkg/forwarder/forwarder.go   # 共享转发引擎（与 domain-forwarder 保持同步）
├── config.example.yaml          # 带注释的配置模板（复制为 config.yaml 使用）
├── Makefile                     # 构建自动化
├── scripts/                     # install.sh / uninstall.sh
├── DEPLOY.md                    # 部署指南
├── README.md                    # 本文件（中文）
└── README_EN.md                 # 英文文档
```

## 许可

MIT
