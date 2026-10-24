# SCF 鉴权引导入口

`cmd/scf` 是 Go 事件函数，通过官方 `cloudfunction.Start` 接收函数 URL event。当前提供 `/api/config`、`/login`、`/verify`、`/healthz`、`/readyz`，不启动自托管 HTTP 服务、不读本地生产配置、不生成用户、不扫描文件、不启动内存下载队列。

文件数据不经过此适配层。当前只接函数 URL 的 `httpMethod/path/headers/queryString/body` 协议，不宣称兼容 API Gateway 的 base64 二进制协议。`path` 按函数 URL 已解码字段重建 URL，不再次把 `%`、`#`、`?` 当 URI 语法；queryString 单独编码。控制请求体上限 256 KiB，原有登录上限继续为 10 KiB；用户标识、UA、query token 及请求截止时间传给原有中间件。

## 环境设置

| 环境变量 | 含义 |
|---|---|
| `BAKAWFS_JWT_SECRET` | 必填；同一部署各实例共用，禁止每实例生成随机签名密钥 |
| `BAKAWFS_USERS_YAML` | 必填；与现有 `users.yaml` 同结构，至少一个用户，拒绝空账号、空口令和重复账号 |
| `BAKAWFS_AUTH_MODE` | 默认 true |
| `BAKAWFS_MANGA_MODE` | 默认 false，漫画部署显式 true |
| `BAKAWFS_LOG_ENABLED` | 默认关闭应用日志；仅字符串 true 开启，不能替代关闭平台 CLS 投递 |

配置在首次调用时初始化，兼容官方 Go SDK 在 `InvokeRequest.Environment` 中注入环境变量；后续热调用复用。配置错误返回 503，不回显 YAML 或凭据。修改配置后由平台更新实例。

## 打包与部署

```sh
python3 scripts/package-scf.py
```

`dist/scf/bakawfs-scf.zip` 根目录只有带执行权限的 `main`：选择事件函数、`Go1`、Linux x86_64，处理入口 `main`。发布使用显式新函数或测试版本，函数 URL 用 NONE 平台认证时仍由应用登录/JWT 验证业务请求；别把 CAM 密钥给浏览器。

`go-probe.zip` 和 `node-probe.zip` 分别为纯空处理对照，后者入口 `index.main_handler`、运行时 `Nodejs20.19`。探针与引导函数不是完整文件服务器。`/healthz` 200 只代表引导 API 可执行；`/readyz` 保持 503，直至真实存储及文件流程完成接线。

生成器固定 ZIP 时间、限定文件、关闭 Go 自动 VCS 嵌入，并在独立 manifest 记录源码提交、工作树是否有修改、包体积和 SHA-256。不会把运行时配置、漫画、本地字体、云密钥或测试票据打包。

日志服务必须在平台侧处理。新建函数后、首次调用前，用 `UpdateFunctionConfiguration` 显式设置 `ClsLogsetId: ""`、`ClsTopicId: ""`、`IgnoreSysLog: true` 并读回确认。本轮已实测该组合能清空绑定。若为诊断临时开通 CLS，测试后关闭采集并清理本次主题；既有共享主题的历史数据须先获得删除授权。最后实际调用一次并核对 CLS 主题列表，防止再次自动启用；不能只设置 Invoke 的 `LogType: None` 就声称已关闭付费日志。

参考：[Go 事件入口](https://intl.cloud.tencent.com/zh/document/product/583/46775)、[函数 URL event/response](https://intl.cloud.tencent.com/zh/document/product/583/69491)、[Go 二进制打包](https://cloud.tencent.com/document/product/583/67385)。
