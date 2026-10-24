# 云端部署与迁移

云端运行链路是浏览器 → 腾讯 SCF 控制 API → 七牛私有 Kodo。静态 UI 由现有七牛 CDN 托管，封面、列表预览、阅读器和下载统一从私有数据 CDN 直接加载原图。云端不生成、不存储、不请求缩略图。云端运行不连接树莓派或旧 `www` API，也不需要本地常驻进程、数据库或挂载目录。普通版继续共用 `html/`，`manga_mode` 默认 false。

## 构建与部署

```sh
go test ./...
go vet ./...
npm ci && npm run build:ui && npm test
python3 scripts/package-scf.py
python3 tools/cloud/deploy.py --function bakawfs-api \
  --region ap-guangzhou --bucket PRIVATE_BUCKET --catalog-prefix catalog/v1/ \
  --data-domain https://manga-data.example.com \
  --users /private/users.yaml --config /private/config.yaml --manga --yes
```

部署工具从 `TENCENTCLOUD_SECRET_ID` / `TENCENTCLOUD_SECRET_KEY`（可选临时会话 token）读取腾讯凭证；也可显式指定 `--tencent-ddns /private/ddns.yaml`。腾讯账号凭证不会进入函数环境。七牛凭证默认读取 `/etc/qiniu/keys.env`，仅含 `QINIU_AK`、`QINIU_SK`；可通过 `--keys` 替换。真实账号和 JWT 密钥只由显式部署输入提供，不进入代码包或版本库。`dist/scf/deployment.json` 仅记录资源名、公开 API 地址、包校验和与日志状态。

创建函数明确设置 `AutoCreateClsTopic=FALSE`、`AutoDeployClsTopicIndex=FALSE`、`TraceEnable=FALSE`。首次调用前再清空 `ClsLogsetId` / `ClsTopicId`，并设置 `IgnoreSysLog=true`；应用 `BAKAWFS_LOG_ENABLED=false`。不要用 `LogType=None` 代替关闭 CLS，不要删除其他业务的日志主题。

## 一次性导入

```sh
go build -o /tmp/bakawfs-cloud-migrate ./cmd/cloud-migrate
/tmp/bakawfs-cloud-migrate --mode setup --bucket PRIVATE_BUCKET --yes
/tmp/bakawfs-cloud-migrate --mode inventory --root /path/to/library --state dist/migration
/tmp/bakawfs-cloud-migrate --mode upload --bucket PRIVATE_BUCKET --state dist/migration --workers 16 --yes
/tmp/bakawfs-cloud-migrate --mode source-check --state dist/migration --workers 4
/tmp/bakawfs-cloud-migrate --mode verify --bucket PRIVATE_BUCKET --state dist/migration --workers 16
/tmp/bakawfs-cloud-migrate --mode publish --bucket PRIVATE_BUCKET --state dist/migration --catalog-prefix catalog/v1/ --yes
```

原档始终只读：记录路径、大小、mtime、SHA-256、xxhash64 和七牛 ETag；云导入不执行图片解码、像素哈希或缩略图生成。隐藏项、日志及运行配置/密钥文件不导入。扫描遇到遗漏条目或源文件变化会报错。

上传按内容寻址，逐个核对七牛大小/哈希并写私有本地回执，可直接重跑 `upload` 断点续传。`source-check` 重新逐字节核对原库，`verify` 再检查全部原档对象，确认回执完整；两份验收都绑定完整清单的 SHA-256，防止旧验收误用于同大小的新清单。只有完整核验后才创建版本零；同一命名空间已有不同初始目录时拒绝覆盖。生产首次导入前不要先初始化一个空根目录。

2026-10-24 清点规模：10,070 文件、324 目录、20,905,919,800 原始字节；初始快照 10,394 节点，JSON 2,998,212 字节，gzip 1,207,683 字节。实际最终状态以部署验收记录为准。

## 上传与远程任务

小文件使用限定单个对象、禁止覆盖的短期表单凭证。大文件使用七牛原生 multipart：浏览器计算实际上传协议对应的 ETag，直传分片，服务端从七牛列举已上传分片、原生合并并核对最终大小/哈希后提交目录；浏览器完成请求仅传任务 ID，避免大文件的分片清单超过控制面 256 KiB 限制。表单 ETag V1 与非 4 MiB 分片的 ETag V2 不可混用；实现按官方 [ETag V2 算法](https://github.com/qiniu/java-sdk/blob/master/src/main/java/com/qiniu/util/EtagV2.java) 和真实上传结果核对。准备请求复用任务 ID 和原生上传会话，续传时跳过已存在分片，凭证过期时重新授权；超过 24 小时的任务须重新开始。

远程 URL 使用七牛异步抓取，浏览器关闭后继续执行。抓取只写 `staging/`，回调通过 HMAC 校验，服务端用 `force=false` 的原生 copy 创建不可变正式对象，再通过目录快照 CAS 发布。即使抓取被重试、暂存对象随后变化，已发布对象也不会改写。提供“云端抓取中”的真实阶段状态；供应商接口不提供字节进度，不显示虚构的百分比。

取消与发布竞争同一个持久目录提交点：取消先提交则禁止迟到发布；发布先提交则取消返回冲突。七牛已接收的抓取可能继续，但结果不再进入可见目录。父目录以稳定 ID 绑定，改名后仍归原目录，删除后同名重建也不会接收旧上传。

SCF 定时器每 15 分钟恢复回调遗漏的任务并标记过期任务；每天北京时间 02:00 回收至少保留七天、已不被当前目录或有效任务引用的原档和暂存对象，以及历史版本残留的废弃缩略图，单轮最多 5,000 个，原生 batch 每批最多 1,000 个。目录历史快照和回执不回收。历史快照用于提交/审计，不代表被删除文件的永久备份。

## 静态站发布

```sh
python3 scripts/package-cloud-ui.py --api 'https://FUNCTION_URL' --output dist/cloud-ui
go run ./cmd/cloud-publish --dir dist/cloud-ui --bucket STATIC_BUCKET \
  --domain https://YOUR_DOMAIN --yes
```

所有依赖按内容 hash 改名；先上传、读取校验依赖，最后发布入口。入口通过已设置缓存元数据的版本对象原子复制，避免新入口短暂带一年缓存；旧 hash 依赖保留，已打开标签页仍可使用。入口 `Cache-Control: no-cache, max-age=0, must-revalidate`，CDN 入口默认零缓存，hash 资源长缓存，同时刷新 `/` 和 `/index.html`。旧浏览器已经存下的一年缓存无法远程撤回，首次切换可用带版本参数的入口 URL。

默认 Kodo 源站签名有效期不能按普通 CDN 长链接处理：本轮实际 180/300 秒可读，900/1800/3600 秒被拒绝；实现保留时钟余量使用 240 秒，过期后从 API 重新签名。生产部署使用独立的私有数据 CDN，签名有效期一小时，支持原文件名下载和 Range；默认源站仅作未配置 CDN 时的兼容通道。

## 验收与边界

`tools/cloud/validate.py`、`tools/cloud/browser.cjs` 只使用独立目录命名空间和合成测试账号，不读取真实用户配置。显式传入私有测试凭证文件；报告和截图保存在 `dist/`。验证权限、并发幂等、直接读写/Range/CORS、特殊名称、空文件、分片续传、迟到上传、回调、自主远程完成及 320/390/1280 布局。

`TestLiveRetentionCollection` 仅在显式设置 `BAKAWFS_TEST_KODO_BUCKET` 和 `BAKAWFS_TEST_KODO_KEYS` 后运行，要求名称含 `validation` 的独立空桶；真实验证压缩快照、不可覆盖 copy、缓存元数据随 copy 保留和引用回收，最后清理自己的对象及测试桶。禁止对漫画桶模拟未来时间做回收测试。

单个解压快照上限 32 MiB，回执持续累计，容量达到上限返回 507；不能删除历史版本来“腾位置”，后续扩容须设计新的检查点协议。云端图片链路完全使用原图，不存在云端缩略图解码或尺寸限制。有限云端实测不构成供应商在所有地域/故障条件下的线性一致性保证。

## 私有数据 CDN 与本轮落地

`cloud-data-domain` 创建专用数据域名并复用账号里已有且覆盖该域名的证书；源站必须为对应私有桶，启用内置 `bsauth.enable=true,isQiniuPrivate=true`，配置 CORS，再添加 CNAME。`qiniuPrivate` 顶层展示字段不能替代实际鉴权验收。上线前验证有效签名 206、无签名/篡改/过期均 403，且缓存预热后无签名仍被拒绝。

本轮数据域名为 `manga-data.northwind.site`，CNAME 为 `manga-data-northwind-site-idvs6kc.qiniudns.com`。证书复用七牛账号现有通配证书，无私钥迁出；当前证书到期时间为 2026-11-30，按现有证书维护流程续期。

用户随后明确取消缩略图：云端生成器和缩略图路由已移除，前端封面/列表/阅读器改原图，列表预览延迟加载。已删除历史云端 `thumbs/` 生成物；本地原档保持初始清单逐字节一致。最终验收以原档对象为准：10,070 文件、9,390 个去重原档对象、20,905,919,800 逻辑字节。
