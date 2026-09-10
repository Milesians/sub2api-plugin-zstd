# Sub2API zstd 测试环境

这是独立的本地测试实例，使用 `18080` 端口、独立容器名和独立数据目录，不连接生产数据库。

首次启动：

```bash
cd testenv
cp .env.example .env
sed -i "s/replace-with-a-test-password/$(openssl rand -hex 16)/" .env
sed -i "s/replace-with-a-test-admin-password/$(openssl rand -hex 16)/" .env
sed -i "s/replace-with-another-openssl-rand-hex-32/$(openssl rand -hex 32)/" .env
sed -i "s/replace-with-openssl-rand-hex-32/$(openssl rand -hex 32)/" .env
docker compose up -d
```

访问 `http://127.0.0.1:18080`，管理员账号使用 `.env` 中的值。查看日志：

```bash
docker compose logs -f sub2api
```

安装生产签名插件前，把 Release 的 `plugin-publisher.txt` 中的 Base64 公钥配置到这个测试实例。将公钥作为参数传给脚本：

```bash
python3 configure-publisher.py 'BASE64_ED25519_PUBLIC_KEY'
docker compose restart sub2api
```

然后在测试实例的插件页面上传 `.s2plugin`。检查插件日志：

```bash
docker compose logs --tail=200 sub2api | grep -i plugin
```

停止测试环境但保留数据：

```bash
docker compose down
```

只有确认要清空测试数据时才执行：

```bash
docker compose down -v
rm -rf data postgres_data redis_data
```
