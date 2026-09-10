# Sub2API OpenAI OAuth zstd 插件

这是一个 Sub2API `openai.oauth.outbound_transport.v1` 插件。它只处理 `platform=openai`、`account_type=oauth` 的 POST 请求，并且只压缩 `chatgpt.com`（含子域名）下 `/backend-api/codex/*/responses` 的请求体。插件不处理 OAuth 登录、Token 刷新、API Key 账号、其他 Provider、SSE 解析或计费。

插件作为独立进程运行，通过 Sub2API v1 gRPC 协议接收 `start`、多个 `body_chunk` 和 `body_end`。符合条件的请求先完整接收并使用 zstd level 3 压缩，再以准确的 `Content-Length` 连接上游；响应头和响应体仍然流式返回。代理 URL 支持 HTTP/HTTPS、SOCKS5；请求已经交给 HTTP client 后发生的错误都标记为 `request_sent=true`，宿主据此不会自动重放到其他账号。

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

生产部署者需要将 Release 附件 `plugin-publisher.txt` 中的配置加入 Sub2API 的 `plugins.trusted_publishers`。其中的公钥是 Base64 编码的 Ed25519 原始 32 字节公钥，不是 PEM 或 DER 封装格式。

## 如何验证插件

配置页的“压缩自检”会按提交的配置调用实际压缩函数，再解压并逐字节比较，显示样例压缩前后的字节数。关闭压缩或配置无效时会提示失败。样例压缩率不代表真实请求的压缩率；自检不会保存配置，也不会访问 OpenAI。

开发环境运行 `go test ./...`，其中 `TestForwardCompressedHTTPRoundTrip` 会让实际转发代码向本地 HTTPS 测试服务发送压缩请求，验证 `Content-Encoding: zstd`、压缩后的 `Content-Length`、解压后的完整请求体，以及 SSE 响应的返回。这不需要账号，但不能证明真实上游接受请求。

真实账号验收需要在 Sub2API 中安装更新后的插件包，启用插件及压缩并保存，再通过宿主向 Responses 接口发送请求（例如用 Codex 发一条简短消息）。确认路由到的是 OpenAI OAuth 账号，且宿主实际调用了该传输插件；API Key 账号和其他接口不在压缩范围内。检查请求成功并收到完整响应，再关闭压缩保存后重复同一请求，对比两次结果。仅“请求成功”不能证明命中插件，需要结合宿主的插件调用记录或调试追踪确认。

配置测试协议只提供配置 JSON，没有账号令牌、账号选择或代理信息，因此配置页自检不能代替真实账号验收。不要把 OAuth Token 填入插件配置。

## GitHub Actions

推送到 `main` 或创建 Pull Request 都会运行单元测试和多平台构建，并上传以提交 SHA 命名的插件包 Artifact。`main` 的推送在测试、构建和包校验成功后自动发布 `v0.2.<run_number>` GitHub Release，附带可下载的 `sub2api-plugin-zstd.s2plugin`；标签指向该次构建的提交，插件版本为 `0.2.<run_number>`。重新运行同一次工作流会更新该 Release 的附件。Pull Request 不发布 Release。

发布任务通过内置 `GITHUB_TOKEN` 的 `contents: write` 权限创建标签和 Release，无需额外 PAT。`main` 推送若未配置 `PLUGIN_SIGNING_KEY` 会在构建阶段失败，避免发布生产环境不可安装的未签名包；Pull Request 仍可构建未签名开发包。
