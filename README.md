# EasyDist

通过 token 上传 ZIP，将 Godot Web 导出包发布到固定地址。Go 单服务、SQLite、Svelte 管理台；Docker Compose 只运行 EasyDist，配合服务器已有的 Caddy。

## 一键启动

需要 Linux 服务器、Docker Compose 和已有 Caddy。将两个域名分别解析到服务器，例如 `admin.example.com` 和 `games.example.com`。

```sh
cp .env.example .env
# 编辑 .env：设置两个域名、管理员初始密码及部署目录。
sudo install -d -m 0755 -o 10001 -g 10001 /srv/easydist/deploy
docker compose up -d --build
docker compose ps
```

应用以 UID/GID `10001:10001` 运行。部署目录必须可写；Caddy 必须有权限穿过父目录并读取 `public/` 和 `releases/`。系统生成的目录为 `0755`，游戏文件为 `0644`。不要把 Caddy 的根目录指向整个 `/deploy`，也不要让其他程序修改这个目录。

首次数据库为空时，系统使用 `ADMIN_USERNAME`、`ADMIN_PASSWORD` 创建唯一管理员。用户名为 3–32 位 ASCII 字母、数字、下划线或连字符，以字母或数字开头；密码为 12–72 **字节**。已有用户时，环境变量不会创建新管理员或重置密码。登录后台即可创建普通用户、重置其密码、禁用或启用其账号；不提供注册。管理员不能被禁用。

`.env` 含初始密码，应限制文件读取权限，不能提交到 Git。后续管理员密码通过后台“修改密码”修改。Compose 默认在 `127.0.0.1:8080` 提供服务，不直接开放到公网。

## 配置已有 Caddy

将 [Caddyfile.example](./Caddyfile.example) 的两个站点块合并进现有配置，并替换域名、端口及部署目录：

```caddyfile
admin.example.com {
    reverse_proxy 127.0.0.1:8080
}

games.example.com {
	root * /srv/easydist/deploy/public
	header {
		Cross-Origin-Opener-Policy same-origin
		Cross-Origin-Embedder-Policy require-corp
		X-Content-Type-Options nosniff
		Cache-Control "public, no-cache"
	}
	@wasm path *.wasm
	header @wasm Content-Type application/wasm
	@version path_regexp version ^/_versions/([A-Za-z0-9_-]{43})(/.*)?$
	handle @version {
		root * /srv/easydist/deploy/releases
		rewrite * /{re.version.1}{re.version.2}
		header Cache-Control "public, max-age=604800, immutable" {
			match status 2xx 304
		}
		file_server {
			precompressed gzip
		}
	}
	@entry path_regexp entry ^/([a-z0-9](?:[a-z0-9-]{1,46}[a-z0-9]))(?:/|/index\.html)?$
	handle @entry {
		header >Cache-Control no-store
		rewrite * /game/{re.entry.1}
		reverse_proxy 127.0.0.1:8080
	}
	handle {
		file_server {
			precompressed gzip
		}
	}
}
```

```sh
caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

Caddy 自动管理 HTTPS；公网证书需要 DNS 正确并允许访问 80/443 端口。`file_server` 不启用 `browse`；固定入口（包括无尾斜杠和 `index.html` 地址）由 EasyDist 提供；版本目录提供 `index.html`。不要配置游戏的 SPA fallback，也不要把未知游戏路径转发到管理后台。Caddy 可以跟随系统生成的相对目录链接，链接目标位于同一部署卷内的 `releases/`。

上传 ZIP 和公开 URL 保持不变。固定游戏入口由 EasyDist 提供、不缓存，在同源全屏 iframe 中加载当前版本的 `/_versions/<版本ID>/` 页面，地址栏仍为原游戏 URL。版本目录中的相对资源 URL 自动包含版本 ID，Caddy 对成功响应设置 `public, max-age=604800, immutable`（7 天）；发布新版本后重新进入或刷新入口即可加载新版。直接使用旧版本 URL 会继续访问旧版，不会自动跟随更新。旧的非版本化资源地址仍使用 `public, no-cache`，只用于兼容已有链接。上传包无需提供 `.gz`：解压校验完成后，系统在正式发布之前顺序、流式压缩 HTML、JS、CSS、JSON、SVG、WASM 和 PCK 文件，只有压缩后更小才保留 `.gz` 副本。ZIP 中同名的 `.gz` 会从原文件重新生成，避免旧压缩文件与新游戏不一致。压缩失败或上传取消时不会替换已部署游戏。Caddy 根据浏览器的 `Accept-Encoding` 提供 `.gz` 文件，没有压缩副本时提供原文件，不进行实时压缩。

如果现有 Caddy **运行在容器内**，将相同宿主机部署目录只读挂载到 Caddy，例如 `/srv/easydist/deploy:/srv/easydist/deploy:ro`，必须挂载整个目录以解析相对链接。将 EasyDist 与现有 Caddy 加入同一 Docker network，管理站点和游戏入口的代理均改为 `reverse_proxy easydist:8080`。可以使用单独的 Compose override 加入现有网络：

```yaml
# compose.caddy-network.yaml
services:
  easydist:
    networks:
      - caddy
networks:
  caddy:
    external: true
    name: your-existing-caddy-network
```

```sh
docker compose -f compose.yaml -f compose.caddy-network.yaml up -d --build
```

此配置复用现有 Caddy，不新增反代容器。避免为多个服务设置相同的 `easydist` 网络别名。

## 上传与 Token

1. 在管理台创建 dist name，保存仅显示一次的 token。
2. 导出 Godot Web 包，入口命名为 `index.html`，压缩全部导出文件。
3. 使用 API 上传；成功后访问返回的 URL。

```sh
curl --fail-with-body -X POST 'https://admin.example.com/api/deploy' \
  -H "Authorization: Bearer $EASYDIST_TOKEN" \
  -H 'Content-Type: application/zip' \
  --data-binary @game.zip
```

成功响应示例：

```json
{"dist_name":"my-game","url":"https://games.example.com/my-game/"}
```

ZIP 可以在根目录包含 `index.html`，也可以将所有内容放在唯一顶层文件夹内；后一种会自动剥离一层。忽略 `__MACOSX/` 和 `.DS_Store`。非法路径、符号链接、特殊文件、重复路径、文件目录冲突和损坏文件会被拒绝。上传、解压或校验失败会保留旧部署。

名称规则为 `^[a-z0-9](?:[a-z0-9-]{1,46}[a-z0-9])$`：3–48 位小写字母、数字或连字符，首尾不能是连字符；名称全局唯一且不可修改。每个用户最多拥有 16 个 token（删除待完成的 token 也占额度）。数据库仅保存 token 的 SHA-256 摘要，无法找回明文。

同一 token 再次上传会完全替换旧内容，包括移除新包中不存在的文件。轮换只更新凭据，旧 token 立即失效，URL 和部署内容保留。删除 token 同时撤销凭据、删除内容、释放名称，不能撤销；若文件清理失败，名称暂不释放，可重试删除或重启继续清理。禁用用户会撤销其登录并禁止上传，已部署内容仍公开可读；重新启用后原 token 恢复可用。

同时只能进行一个上传部署请求；忙时返回 `409`，调用方稍后重试。上传过程中撤销或轮换 token、禁用用户，都会阻止最终发布。

## 配置变量

| 变量 | 默认值 / 说明 |
|---|---|
| `ADMIN_ORIGIN` | 必填，管理域名完整 origin，无路径，例如 `https://admin.example.com` |
| `BASE_URL` | 必填，另一个游戏域名完整 origin，无路径 |
| `ADMIN_USERNAME` | Compose 默认为 `admin`，仅首次初始化 |
| `ADMIN_PASSWORD` | 必填，12–72 字节，仅首次初始化 |
| `PORT` | 宿主机端口，默认 `8080`，Compose 使用 |
| `DEPLOY_HOST_DIR` | 宿主机目录，Compose 默认 `./deploy`；示例为 `/srv/easydist/deploy` |
| `MAX_ZIP_BYTES` | `536870912`（512 MiB），上传 ZIP 大小 |
| `MAX_EXTRACT_BYTES` | `2147483648`（2 GiB），实际解压总字节数 |
| `MAX_ZIP_ENTRIES` | `10000`，包括目录和忽略的元数据条目 |
| `DB_PATH` | 容器内 `/data/easydist.db`，通常无需修改 |
| `DEPLOY_DIR` | 容器内 `/deploy`，通常无需修改 |
| `WEB_DIR` | 容器内 `/app/web`，通常无需修改 |
| `LISTEN` | `:8080`，Compose 和健康检查固定使用容器端口 8080 |

上传请求最长 15 分钟；临时磁盘需容纳 ZIP、新版本和当前旧版本。所有发布目录须处于同一文件系统。仅支持单应用实例，不要将多个实例挂到同一数据库或部署目录。

登录会话有效期 7 天，Cookie 使用 HttpOnly、SameSite=Strict 和 HTTPS Secure，并且不设置 Domain。修改/重置密码会撤销该用户全部登录。管理写接口要求 `Origin` 精确等于 `ADMIN_ORIGIN`；API 调用这些接口也需携带此头。部署接口仅接受 Bearer token，不接受登录 Cookie。进程内登录限制为全局每分钟 60 次、每用户名每 15 分钟 10 次，成功尝试也计数，重启会清空计数。

## API

管理接口使用登录 Cookie；普通用户只能操作自己的 token。错误响应为 `{"error":"描述"}`。

| 方法与路径 | JSON 请求 / 用途 |
|---|---|
| `POST /api/login` | `{"username":"...","password":"..."}` |
| `POST /api/logout` | 退出当前会话 |
| `GET /api/me` | 当前用户信息 |
| `POST /api/password` | `{"current_password":"...","password":"..."}` |
| `GET /api/tokens` | 自己的 token 元数据、公开 URL 和部署状态，不返回凭据 |
| `POST /api/tokens` | `{"dist_name":"my-game"}`；仅此响应返回新 token |
| `POST /api/tokens/{id}/rotate` | 轮换，响应返回一次新 token |
| `DELETE /api/tokens/{id}` | 撤销凭据并删除部署 |
| `GET /api/admin/users` | 管理员查看用户 |
| `POST /api/admin/users` | `{"username":"...","password":"..."}`，创建普通用户 |
| `POST /api/admin/users/{id}/password` | `{"password":"..."}`，重置密码 |
| `PATCH /api/admin/users/{id}` | `{"disabled":true}` 或 `false` |
| `POST /api/deploy` | `application/zip` 原始请求体 + Bearer token |
| `GET /healthz` | 健康状态及数据库可用性 |

部署接口常见状态：`400` 无效 ZIP/上传中断，`401` token 失效，`409` 正在部署，`413` 超限，`415` 内容类型错误，`500` 文件系统或数据库错误。后台名称冲突和 token 额度超限返回 `409`。

## Godot 与覆盖发布

导出文件需使用相对资源地址，确保游戏能在 `/dist-name/` 子路径加载。配置为 WASM 提供 `application/wasm`，为 Godot 多线程导出提供 COOP/COEP；跨域资源需要满足相应的 CORS/CORP 规则。生产访问使用 HTTPS。

固定入口使用 `Cache-Control: no-store`，版本资源缓存 7 天。Godot PWA 的 Service Worker 缓存独立于 HTTP 缓存；需要即时更新时关闭游戏导出的 PWA，或自行管理其缓存版本。管理台自身不注册 Service Worker。

发布通过完整解压目录和原子链接切换实现，入口将一次游戏加载固定到同一个版本。旧版本从被替换时起至少保留 7 天，发布和应用启动时清理到期版本；没有上传或重启时可能保留更久，需要为频繁发布预留磁盘。删除 token 会删除该游戏全部版本。已有部署会在升级启动时自动登记，无需重新上传。已运行的游戏不会自动切换版本；打开旧页面超过保留期后再请求未缓存文件可能失败。浏览器仍可能因存储配额清理大文件缓存，缓存期限不能保证文件实际保存满 7 天。

游戏域名与后台分开，游戏脚本无法读取后台登录 Cookie。不同游戏路径仍共享同一个浏览器 origin（包括 localStorage、Service Worker 等），不能当作彼此隔离的不可信租户。请只允许可信用户部署。

## 备份、恢复与排错

数据库位于 Compose 的 `data` 命名卷，部署文件位于 `DEPLOY_HOST_DIR`。二者应一起备份；保留相对符号链接，不要用会跟随链接的备份选项。

```sh
docker compose stop
mkdir -p backup
docker compose cp easydist:/data/. ./backup/data
sudo tar -C /srv/easydist -czf backup/deploy.tar.gz deploy
docker compose start
```

恢复时先停止服务，将部署目录完整替换为备份内容（保留相对链接），数据库目录也完整替换，避免混入其他时间点的文件。例如在干净的目标目录/卷上：

```sh
docker compose create
docker compose cp ./backup/data/. easydist:/data
sudo tar -C /srv/easydist -xzf backup/deploy.tar.gz
sudo chown -R 10001:10001 /srv/easydist/deploy
docker compose run --rm --no-deps --user 0 --entrypoint sh easydist -c \
  'chown -R 10001:10001 /data && chmod 700 /data && chmod 600 /data/easydist.db'
docker compose up -d
```

已有部署时先移动旧目录到备份位置，再恢复归档，不要直接叠加。数据库目录需要 UID/GID `10001:10001`，权限 `0700`，数据库文件 `0600`。备份包含密码摘要、会话和 token 摘要，应限制访问。不要只恢复数据库或只恢复公开链接。

启动时继续中断的删除，清除未完成上传和无引用版本；已完成的部署保留。健康检查失败、权限错误或上传失败可使用 `docker compose logs --tail=100 easydist` 检查，日志不输出密码或 token。`docker compose down` 保留命名卷；`down -v` 会删除数据库，请谨慎使用。

## 本地开发与检查

需要 Go 1.26、Node.js 22.12+ 或 24。生产发布推荐在 Linux Docker 中验证链接切换。

```sh
cd web
npm ci
npm run dev
```

另一个终端（项目根目录）：

```sh
ADMIN_ORIGIN=http://localhost:5173 BASE_URL=http://localhost:8081 \
ADMIN_USERNAME=admin ADMIN_PASSWORD=a-long-development-password \
DB_PATH=./data/easydist.db DEPLOY_DIR=./deploy WEB_DIR=./web/build \
go run .
```

开发 HTTP origin 仅用于本地，此时 Cookie 不设 Secure。Vite 将 `/api` 代理到 Go。本地浏览游戏需另行用现有 Caddy 提供 `deploy/public`。

```sh
go test ./...
go vet ./...
cd web
npm run check
npm run build
```

测试覆盖用户权限、会话撤销、token 额度与轮换、ZIP 校验、覆盖与失败保留、上传期间撤销、删除与恢复清理。Docker 构建会运行 Go 测试与 Svelte 检查。前端构建结构基于 [svelte-static-pwa-template](https://github.com/Nigh/svelte-static-pwa-template)，主题使用 [xianii-theme](https://github.com/Nigh/xianii-theme)；第三方许可见 [THIRD_PARTY_NOTICES.md](./THIRD_PARTY_NOTICES.md)。

真实 Godot 联调示例位于 `testdata/godot/`，使用 Godot 4.4.1 及对应 Web 导出模板验证过。可重新导出并上传：

```sh
mkdir -p /tmp/easydist-game
godot --headless --path testdata/godot --import
godot --headless --path testdata/godot --export-release Web /tmp/easydist-game/index.html
(cd /tmp/easydist-game && zip -r /tmp/easydist-game.zip .)
```

上传后访问 `/dist-name/`，应显示持续变化的计时器。此示例启用线程，用于验证真实 WASM、子路径加载及 COOP/COEP；HTTPS 证书、响应头和尾斜杠跳转还需在对应 Caddy 配置下检查。系统目标运行环境是 Linux 容器；Windows 原生运行的目录链接行为不作为生产兼容保证。
