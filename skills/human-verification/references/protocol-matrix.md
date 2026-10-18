# 版本化契约与来源（2026-10-18）

本文件说明源码适配范围，不是运行验收结论。

| 协议/模块 | 源码路径 | 关键契约 | 未覆盖 |
|---|---|---|---|
| ALTCHA v1 部分 SHA-2 | agent/tools_verification.go | SHA-256/512(salt + 十进制number)，标准Base64 payload，公开签名原样带回 | SHA-1、所有旧SDK容错 |
| ALTCHA v2 部分KDF | agent/verification_solvers.go | parameters内hex salt/nonce；nonce字节+uint32大端counter；SHA迭代或PBKDF2；derivedKey hex，保留原challenge | Scrypt、Argon2、未知KDF和签名伪造 |
| Anubis旧fast/slow | agent/verification_solvers.go、verification_adapters.go、verification_flow.go | SHA-256(randomData+十进制nonce)，difficulty个前导hex零；公开challenge id；兼容对象与v1.22字符串API封套；显式同源提交与跳转 | 新WASM/sha256/argon2id/hashx方法，preact/metarefresh |
| 指纹HTTP | integrations/verification-http（独立Go模块） | req/v3 v3.59.0，固定Chrome/Firefox预设，标准TLS证书验证与有界stdio | 现代浏览器全环境、通用Turnstile/视觉求解、动态遥测仿造 |
| 软件逆向过滤 | agent/software_re.go（上一阶段） | flow是主动Web工具，software_re过滤继续阻止注入 | 不把网页下载障碍变成任意站点探测授权 |

## 资料来源

- ALTCHA官方Go库与版本划分：https://github.com/altcha-org/altcha-lib-go
- 本次v2契约参考官方 main/v2（2026-10-18读取，未锁定commit）；公开源码描述uint32大端counter、字段和KDF行为。实现为独立编写的有界客户端计算，不复制服务端签名能力。
- https://github.com/altcha-org/altcha-lib-go/blob/main/v2/altcha.go
- RFC 8018 PBKDF2：https://www.rfc-editor.org/rfc/rfc8018
- Anubis原生公开流程参考：https://github.com/fzakaria/anubis-fetch （本轮只参考旧SHA256契约，不引入其Chromium/WASM/缓存依赖）
- 已核对的官方旧版fast/slow验证器：https://github.com/TecharoHQ/anubis/blob/v1.22.0/lib/challenge/proofofwork/proofofwork.go
- 已核对的官方id/nonce/response/elapsedTime契约及字符串API封套：https://github.com/TecharoHQ/anubis/blob/v1.22.0/lib/anubis.go
- 官方旧版script JSON标记：https://github.com/TecharoHQ/anubis/blob/v1.22.0/web/index.templ
- req指纹教程：https://req.cool/docs/tutorial/http-fingerprint/
- helper锁定版本：https://github.com/imroc/req/tree/v3.59.0
- Cloudflare挑战响应：https://developers.cloudflare.com/cloudflare-challenges/challenge-types/challenge-pages/detect-response/

版本、算法名和数据类型不符时拒绝继续，不能根据厂商名称猜测兼容。服务端票据验收和业务成功必须另行观测。
