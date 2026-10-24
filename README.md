# bakaWFS

轻量、无数据库的自托管文件服务器。Go 标准库 HTTP、JWT 鉴权、串行文件操作队列，附带内置 Web UI。

统一代码提供两种模式：默认是通用文件管理；显式开启 `manga_mode` 后提供漫画封面墙、缩略图和渐进阅读器。后续只维护 `main`，`manga` 保留历史，不再重复修复和单独发版。

## 功能

- 文件浏览、全库搜索、下载、文本/图片/视频预览
- 上传、分片上传及 xxhash 校验、远程下载与取消
- 重命名、移动、复制、新建目录、删除
- admin / guest 读写分级、JWT 自动续签、可选全站鉴权
- 内置或外置前端，支持同源及前后端分离部署
- Linux systemd / Windows SCM / macOS launchd 服务管理
- 可选漫画模式：封面墙、连载展示、缩略图和渐进加载

## 快速开始

```sh
go build -o bakaWFS .
./bakaWFS
```

首次启动生成 `config.yaml` 和 `users.yaml`。共享只读账号为 `baka/baka`，可写 `admin` 口令随机生成并打印一次。已有用户未指定 `role` 时按 guest 处理。

```yaml
address: "0.0.0.0"
https_port: -1
http_port: 8080
secret: "首次启动自动生成"
cert_path: "certificate.crt"
key_path: "private.key"
file_dir: "files"
html_dir: "built-in"     # 内置前端 / 外部目录 / 留空禁用前端
users_file: "users.yaml"
temp_dir: ".uploads"
download_workers: 2
audit_log: ""
cors_enabled: false
auth_mode: false        # true 时浏览和下载也需登录
manga_mode: false       # 默认关闭；开启后会向原图写入哈希元数据
trash_dir: ""           # 默认在真实 file_dir 旁创建 .bakawfs-trash 后缀的目录
```

服务工作目录为二进制目录。TLS 端口设为 `-1` 表示关闭；公网部署应配置 TLS 或反向代理。前后端分离时修改 `html/main.js` 的 `API_BASE`，并开启 `cors_enabled`；空 `API_BASE` 表示同源部署。

```sh
./bakaWFS install
./bakaWFS start
./bakaWFS stop
./bakaWFS restart
./bakaWFS status
./bakaWFS uninstall
```

## 从原有分支升级

- 原 `main` 用户保持 `manga_mode: false`（省略也为 false），原图不会因浏览而被改写。
- 原 `manga` 用户在原配置中加入 `manga_mode: true`，继续使用原文件目录和 `.thumbcache`。若省略则进入普通模式。
- 使用外置前端时同步更新整个 `html/`（含 `style.css`）；原部署的 `API_BASE` 和本地 vendor 地址需自行保留。
- 两种模式使用同一份二进制及版本号，不再需要双线 cherry-pick。

漫画模式生成缩略图时会向 PNG/JPEG 原图嵌入哈希元数据：像素不变，文件字节和 mtime 会变。具体语义见 [漫画模式](docs/manga.md)。

## 删除语义

删除先在串行队列内把整个目标 `rename` 到文件区外的暂存目录，再由独立 worker 物理清理。API 返回 204 表示原路径已整体移除；磁盘空间可能稍后释放。后台清理失败会记录日志，每分钟重试，重启也会自动扫描残留。它不是可恢复回收站。

暂存区须与文件区位于同一文件系统，且不能位于文件区内或包含文件区。默认使用解析软链后的文件根目录加 `.bakawfs-trash` 后缀。若文件根本身是挂载点，须把 `trash_dir` 指向同盘且不公开的位置；跨盘或权限不足会拒绝删除并保留原目录，不会降级为递归删除。更改暂存区路径前须处理旧区残留。

## 验证

```sh
go test ./...
go vet ./...
go test -race ./...  # 支持 race 的主机；CI 使用 Linux amd64
npm ci
npm run build:ui
npx playwright-core install --with-deps chromium
npm test
```

浏览器回归使用临时数据启动真实后端，覆盖两种模式、原图写入边界、搜索、登录退出、目录操作和 320/390/1280px 布局。可用 `CHROMIUM_PATH` 指定浏览器。运行库和 Tailwind 静态 CSS 已包含在 `html/`，普通 Go 编译与部署不需要 Node；修改前端模板/依赖后执行 `npm run build:ui` 更新产物。测试会拦截外站请求，验证页面无需外部 CDN。

前端依赖锁定 Vue 3.5.44（[生产构建](https://vuejs.org/guide/best-practices/production-deployment)）、Tailwind CSS 4.3.3（[静态 CLI 构建](https://tailwindcss.com/docs/installation/tailwind-cli)）、[xxhash-wasm 1.1.0](https://github.com/jungomi/xxhash-wasm)。许可证随运行库提供，不包含私有字体。Tailwind 4 面向现代浏览器（Safari 16.4+、Chrome 111+、Firefox 128+）。

## API 与维护

[API 文档](bakaWFS_API.md) · [分支收敛与迁移说明](docs/maintenance.md) · [云函数迁移计划（实施中）](docs/serverless-migration-plan.md) · [FaaS 实施进度与验收](docs/serverless-progress-20261024.md) · [SCF 引导入口](docs/scf-bootstrap.md)

`GET /api/config` 返回 `auth_mode` 和 `manga_mode`。`/thumb/`、`/thumbs` 仅在漫画模式开放；其他文件 API 两种模式共用。文件系统始终是唯一状态来源，无索引数据库或持久会话。

MIT License © 2026 Zhang Feng
