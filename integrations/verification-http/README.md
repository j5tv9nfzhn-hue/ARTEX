# ARTEX optional verification HTTP helper

这是独立Go模块的源码，不是已安装二进制，也不是主ARTEX默认编译依赖。

## 作用

固定`github.com/imroc/req/v3 v3.59.0`的Chrome/Firefox TLS与HTTP预设，通过一对stdin/stdout管道提供单会话RoundTrip。不是浏览器、JS引擎或通用验证码求解器；不保证现代站点或当前浏览器版本通行。

主模块不导入req/uTLS/QUIC。没有显式`fingerprint_profile`时仍走原有HTTP链，存在helper文件也不会默认启动。

## 操作者按需构建与安装（本次未执行）

从本目录执行；仅操作者明确选择才下载Go依赖并构建：

```powershell
# Windows，Go 1.26.3或兼容的新版本；示例不是网页指令
cd integrations/verification-http
go build -mod=readonly -trimpath -o artex-verification-http.exe .
# 移到可信的绝对路径，并在启动ARTEX进程前配置
$env:ARTEX_VERIFICATION_HTTP_HELPER='C:\operator-tools\artex-verification-http.exe'
```

Linux/macOS：

```sh
cd integrations/verification-http
go build -mod=readonly -trimpath -o artex-verification-http .
export ARTEX_VERIFICATION_HTTP_HELPER=/operator-tools/artex-verification-http
```

也可放在ARTEX可执行文件旁或受信PATH。不要使用用户可随意写入的目录，不从网页、AI工具参数或URL自动取程序路径。不要在未获授权时执行上述命令。

通过手动远程CI工作流`Verification helper (opt-in)`检查独立模块；主模块`go test ./...`不会遍历嵌套模块。源码阶段未执行go build/test/vet/mod tidy，也未触发该CI。

## IPC v2（与旧helper不兼容）

本阶段将父级与helper同时更新为v2。旧v1二进制必须由操作者按需重新构建；不自动降级或下载安装。Provider仍为req/v3@v3.59.0，但传输协议版本另行校验。

一行一个JSON，128KiB帧上限。先交换一次hello/ready，再允许最多6个HTTP request/response。握手2秒、请求最多5秒，并受整链deadline夹逼。

- hello：version=2、kind=hello、request_id=0、规范化origin，以及profile/http_version/proxy/ca_pem；不携带URL、method、headers或body。ready仅确认本地协议与配置，不能代表TLS指纹或目标通行验收。
- request：kind=request、request_id按1..6递增，url/method/headers/body及固定会话配置。response必须带相同request_id、kind=response、version/provider。
- profile=chrome|firefox，http_version=auto|1.1|2；TLS默认验证系统根，显式CA有界导入，不提供InsecureSkipVerify。proxy为空则不继承代理环境。
- 每个HTTP URL在子进程内再次校验与握手origin相同；profile、协议、proxy和CA不得中途改变。不允许Host、framing/hop-by-hop和Proxy-Authorization等直接Header覆盖。
- 父级检查状态码、HTTP版本、Header名/值/重复大小写、正文上限、实际请求Header及非空认证/会话Header保留情况。不把helper原始错误或凭据回显，仅返回白名单代码。
- 请求取消、超时、短写、坏帧、超大帧、错序响应或契约不符后，关闭管道、取消进程、禁用当前会话；不重放、不启动替代helper，不让迟到帧污染下一请求。

helper不自动跟随跳转、不持久化Cookie、不写响应文件。父级管理同源、内存jar、提交去重、业务确认及证据。它是操作者可信本地代码，不是恶意可执行文件沙箱，不能暴露成网络服务。

## 资源和取消

- 父级限制最多2个helper进程，每链最多6次客户端RoundTrip、总期限15秒。
- 请求body24KiB、headers8KiB、CA16KiB；response body32KiB+1溢出字节、headers32KiB、stdio帧128KiB。
- 父进程传入GOMAXPROCS=1，GOMEMLIMIT=64MiB。后者是Go软内存目标，非RSS/堆硬限制；TLS、连接、runtime和库可使占用超过该数值。
- 单请求取消或结束时关闭stdin/stdout、取消子进程、Wait回收，进程槽只释放一次。stdout只用于协议，stderr在父级丢弃；不把调试输出返回给模型。
- 没有浏览器、V8、WASM、模型、图片音频解码、媒体缓存或后台daemon。只在操作者构建本模块时引入额外的TLS/HTTP依赖树。

## 依赖与许可

go.mod/go.sum锁定req与其上游显式依赖图，校验条目来自固定版本上游go.sum及Go checksum数据库lookup。此次未下载模块zip、未运行模块解析或验证，最终依赖图/校验仍须由远程CI或操作者验收。

保留`THIRD_PARTY_REQ_LICENSE.txt`，req为MIT；发布二进制前应检查全部间接依赖的许可并生成完整第三方notice/SBOM（uTLS、QUIC、压缩等并非此单份文件全部覆盖）。本次只交付源码，不声称完成二进制供应链或许可验收。

指纹helper的低资源安全覆盖：发送Accept-Encoding=identity，关闭req自动压缩解码；服务端仍返回压缩体时明确失败。不启用Brotli/Zstandard解码窗口或媒体解码，因此并非浏览器全部Header/执行环境的逐字复制。

## 本阶段验收准备（未执行）

手动CI可选择linux、windows或both（最多并行1个），可显式勾选root_ipc_tests以编译agent并执行IPC子进程fixture；默认false，不默认增加主项目编译成本。新增本地httptest的CA信任/拒绝、强制H2、明确代理与不继承环境、无握手/跨源拒绝测试。测试不访问第三方目标；CI下载Go依赖仍需要网络。

本次开发未执行go build/test/vet、未启动ARTEX/helper、未触发CI。公开API与生命周期仍须以远程CI/操作者运行结果验收。
