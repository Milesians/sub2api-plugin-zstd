# Sub2API OpenAI OAuth zstd 插件

这是一个 Sub2API `openai.oauth.outbound_transport.v1` 插件。它只处理 `platform=openai`、`account_type=oauth` 的 POST 请求，并且只压缩 `chatgpt.com`（含子域名）下 `/backend-api/codex/*/responses` 的请求体。插件不处理 OAuth 登录、Token 刷新、API Key 账号、其他 Provider、SSE 解析或计费。

插件作为独立进程运行，通过 Sub2API v1 gRPC 协议接收 `start`、多个 `body_chunk` 和 `body_end`，使用管道流式压缩后连接上游，再把原始响应头和响应体流式返回。代理 URL 支持 HTTP/HTTPS；请求已经交给 HTTP client 后发生的错误都标记为 `request_sent=true`，宿主据此不会自动重放到其他账号。

## 本地构建

需要 Go 1.25+ 和 Python 3：

```bash
./build.sh dist
python3 tools/verify_package.py dist/sub2api-plugin-zstd.s2plugin
```

生成的 `.s2plugin` 是 ZIP，包含 Linux amd64/arm64 和 Windows amd64 三个运行时、`manifest.json`、UI 以及每个文件的 SHA-256。默认生成未签名开发包；生产安装需要配置受信任发布者，因此构建时可提供 Ed25519 PEM 私钥：

```bash
PLUGIN_SIGNING_KEY_FILE=/path/to/ed25519.pem \
PLUGIN_SIGNING_KEY_ID=milesians \
PLUGIN_VERSION=0.2.0 ./build.sh dist
```

`signature.json` 签名对象是清单文件的原始字节。私钥不应提交到仓库；GitHub Actions 使用 Secret `PLUGIN_SIGNING_KEY` 和 Repository Variable `PLUGIN_SIGNING_KEY_ID`（缺少 Secret 时生成开发包）。

## GitHub Actions

推送到 `main` 或创建 Pull Request 都会运行单元测试和多平台构建。推送构建的版本为 `0.2.<run_number>`，并上传以提交 SHA 命名的插件包 Artifact；工作流不会自动发布 Release 或推送代码。
