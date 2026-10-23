# 对象存储选型调研（2026-10-18）

范围：bakaWFS 约20GB漫画、浏览器直传、腾讯SCF运行控制逻辑、完整在线管理；仅调研，没有购买或创建资源。用户各家有余额，因此不以既有充值绑定厂商。公开价格不等于账号合同价，未读取私人账单或优惠包；地域、计量进制、冗余与套餐需实施时统一口径。

## 公开参考价

均为标准存储的公开参考，排除归档类、活动新客价和账号折扣。中国大陆常见地域；火山存储单位为GiB。流量列是直接从对象存储公网下载，不是CDN最终分发价格。

| 服务 | 存储参考价 / 月 | 公网直接下载参考价 | 资料 |
|---|---|---|---|
| 腾讯 COS | 0.118元/GB | 0.50元/GB | [存储示例](https://cloud.tencent.com/document/product/436/74742)、[价格页](https://buy.cloud.tencent.com/price/cos) |
| 阿里 OSS | 0.12元/GB（本地冗余） | 0.50元/GB（文档忙时示例） | [计费说明](https://help.aliyun.com/zh/oss/billing/)、[计费案例](https://help.aliyun.com/zh/oss/billing-examples) |
| 七牛 Kodo | 0.115–0.148元/GB（不同国内区域） | 0.26–0.29元/GB | [价格](https://www.qiniu.com/prices/kodo) |
| 华为 OBS | 0.099元/GB（单AZ示例） | 0.50元/GB（忙时示例） | [价格说明](https://www.huaweicloud.com/product/obs/price.html)、[计费PDF](https://support.huaweicloud.com/price-obs/对象存储服务计费说明.pdf) |
| 火山 TOS | 0.099元/GiB | 0.50元/GB | [官方200GiB存储/10GB下载案例](https://docs.volcengine.com/docs/TorchObjectStorage/Billingoverview-2?lang=zh) |
| Cloudflare R2 | 0.015美元/GB | 免费出站，仍有请求费 | [价格](https://developers.cloudflare.com/r2/pricing/) |

七牛当前价格页：实名认证账户每月标准存储10GB、回源10GB、写请求10万次、读请求100万次免费；注意免费CDN下载额度写的是HTTP，不直接套到HTTPS。国内CDN的HTTPS按量价第一阶0.28元/GB，Kodo回源另按0.15元/GB；[CDN价格](https://www.qiniu.com/prices/qcdn)。2026-02-11公告曾上调CDN目录价格约30%、存储约20%，不要继续套旧博客价格：[公告](https://news.qiniu.com/archives/1770791700462)。

约20计费GB的存储本体，国内标准存储按上述参考价约1.98–2.96元/月（不扣免费、不含缩略图等）。单看容量难以解释总体账单；流量通常更值得比较。直接100GB下载，0.26–0.29元/GB约26–29元，0.50元/GB约50元，这还不是带CDN的完整报价。

总成本至少应分开估算：存储 + CDN最终下行 + 未命中回源 + 对象请求 + 图片处理 + SCF/目录状态服务。跨云CDN回源不自动享受同厂回源优惠：[阿里CDN/OSS口径说明](https://help.aliyun.com/zh/cdn/product-overview/billing-of-oss-content-acceleration)。充值余额与已购资源包不同，选型前看能抵扣哪些计费项。

## 适配判断（工程判断，非性能实测）

- 腾讯COS：SCF、临时授权、存储事件可在同厂体系下接入，优先验证候选；普通桶依然不自动提供目录原子rename。
- 七牛：沿用已绑定域名与已有工具最省初期接入工作，公开直出流量较低；SCF到七牛的跨云鉴权、通知、网络和密钥管理要自己接，不能当成COS原生事件触发。
- 阿里OSS：SDK/工具/功能体系完整；如果核心已定腾讯SCF，须有具体功能或价格收益才值得跨云接入。
- 华为OBS/火山TOS：存储单价略低，但20GB规模每月差额很小；不能仅为省几毛钱增加另一套接入。
- R2：免出站在流量大时很有吸引力；中国大陆读者的可达性、首图和连续翻页延迟需真实网络测试，未做性能保证。请求费、Workers和图片处理不因免出站自动免费。

## 需要修正早期假设：部分特殊桶有原子目录能力

先前只按普通对象桶讨论“没有目录原子rename”，不能扩展为所有对象存储产品都没有。候选必须纳入：

### 腾讯COS元数据加速桶

[官方SDK文档](https://cloud.tencent.com/document/product/436/73685)明确描述目录原子高效Rename，并提供普通桶没有的Rename操作。普通REST/SDK可以访问相关能力，不能误认为必须长期挂载到自管主机才可用。

[费用](https://cloud.tencent.com/document/product/436/98037)：国内0.01元/万个文件及目录/日，另外收取。按原库10069文件+324目录、30天估算约0.312元/月；缩略图等会增加数量。这是元数据附加费，不含原本存储/流量/请求。

但[概述文档](https://intl.cloud.tencent.com/zh/document/product/436/43305)列出CORS、数据万象等能力限制，且概述中的旧公测/免费表述与现行收费文档存在时间差。当前账号开通条件、功能兼容性须查最新支持说明/工单并做隔离验证。尤其CORS能否满足浏览器直传，是选型硬条件，不能只凭Rename接口就宣布适用。

### 阿里OSS分层命名空间

[官方文档](https://www.alibabacloud.com/help/zh/oss/user-guide/enable-hierarchical-namespace)明确目录或文件重命名是单个原子操作。该页2026-03-01版本列明：仅美国硅谷、日本东京、英国伦敦、马来西亚吉隆坡；不支持CORS、图片处理、回调等多项普通桶能力。因此不能直接作为中国大陆漫画站的通用替代。范围和限制实施时重新核对。

### 华为OBS并行文件系统

[官方概述](https://support.huaweicloud.com/intl/zh-cn/usermanual-obs/obs_13_0007.html)描述真实目录对象，目录改名不需要逐一处理子对象，并提供REST API。它与普通OBS对象桶不同；CDN、浏览器直传、对象处理及具体目录删除语义需再核对，不能套用普通桶价格和能力后直接下结论。

## 下次决策顺序

1. 先验证腾讯COS元数据加速桶是否可在目标账号/地域使用，且满足CORS直传、CDN分发、原生分片与目录Rename；确认删除可否通过先Rename到私有垃圾区再物理清理保持原子可见，不能把递归Delete本身默认当原子。
2. 若可行，比较它与普通COS+自建目录提交、七牛+自建目录提交的实现复杂度，优先减少权威状态和事务代码。
3. 若限制冲突，退回普通桶方案：COS优先评估同厂接入，七牛优先评估现有域名/工具复用与实际CDN套餐；不因已充值而锁定。
4. 用同一组真实漫画页，在国内不同网络测首图、连续翻页、失败率和浏览器直传；查看现有账号资源包/折扣后，再按100GB、1TB月分发量做完整账单比较。

本调研没有变更最终厂商决定，七牛仍是原提案而非已迁移事实。语言、目录权威模型和特殊桶可用性都未实测。
