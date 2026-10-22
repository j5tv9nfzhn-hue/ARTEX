// 允许局域网明文 HTTP（Android）。
//
// 手机端只跟「你自己电脑上的 ARTEX」说话，而那个服务是纯 http://192.168.x.x:8787。
// Android 9 起默认禁止明文，请求会在 native 层直接失败，JS 侧只能看到一句：
//
//   fetch failed: java.net.UnknownServiceException: CLEARTEXT communication to
//   192.168.1.61 not permitted by network security policy
//
// 这种失败和网络不通长得不一样，重试多少次都不会好——必须换安装包。
// Expo 的托管配置里没有对应字段，所以在 prebuild 阶段把
// android:usesCleartextTraffic="true" 写进 AndroidManifest。
//
// CI 的 `npx expo prebuild --platform android` 会自动执行本插件（见 app.json 的 plugins）。
// 以后手机端改走 HTTPS + 反向代理时，删掉这个插件和 app.json 里那一行即可。
const { withAndroidManifest } = (() => {
  try {
    return require("expo/config-plugins");
  } catch {
    // 老版本 Expo 只暴露 @expo/config-plugins（它是 expo 的传递依赖）。
    return require("@expo/config-plugins");
  }
})();

module.exports = function withCleartextLan(config) {
  return withAndroidManifest(config, (cfg) => {
    const application = cfg.modResults?.manifest?.application?.[0];
    if (application) {
      application.$ = application.$ ?? {};
      application.$["android:usesCleartextTraffic"] = "true";
    }
    return cfg;
  });
};
