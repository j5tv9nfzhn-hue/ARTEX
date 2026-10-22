// 网络错误的人话翻译。
//
// 两种最常见的失败，原始报错都是给开发者看的，用户看完不知道下一步做什么：
//
//  1. 安装包没放开明文（Android 9+）：
//     fetch failed: java.net.UnknownServiceException: CLEARTEXT communication to
//     192.168.1.61 not permitted by network security policy
//     —— 这不是网络问题，是构建配置问题，重试没有意义。
//  2. 地址写错、不在同一 Wi-Fi、电脑没开：Network request failed / fetch failed。
//
// 这里按原始文本分流，翻译成「发生了什么 + 下一步做什么」；认不出来的错误原样透出，
// 免得把真正的服务端报错盖掉。

const CLEARTEXT = /cleartext|UnknownServiceException|network security policy/i;
const UNREACHABLE =
  /network request failed|fetch failed|failed to fetch|ECONNREFUSED|ECONNRESET|ETIMEDOUT|timed out|请求超时/i;

export function friendlyNetError(error: unknown, fallback = "网络错误"): string {
  const raw = error instanceof Error ? error.message : String(error ?? "");

  if (CLEARTEXT.test(raw)) {
    return "这个安装包不允许明文 HTTP 连接。请安装最新构建（已放开局域网明文），或者把地址换成带 TLS 的 https://。";
  }
  if (UNREACHABLE.test(raw)) {
    return "连不上这个地址：确认手机和电脑在同一个 Wi-Fi、电脑上的 ARTEX 正在运行，地址和端口（默认 8787）没写错。";
  }
  return raw || fallback;
}
