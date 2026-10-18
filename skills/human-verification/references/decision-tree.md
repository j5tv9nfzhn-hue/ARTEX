# 无人值守决策树

1. **已有响应吗？** 有则优先使用内置分诊；没有才按任务授权读取一次。CDN/403不是充分阻断证据。
2. **业务是否已能读？** 用明确业务判据判断；成功即停止，不为了验证“能解题”额外提交。
3. **当前真实能力是什么？** 调 human_verification_capabilities。helper状态 detected_not_handshaken 不等于已验收；配置错误或未安装不自动获取。
4. **能辨认公开协议吗？**
   - ALTCHA v1：旧SHA-2 salt+number。
   - ALTCHA v2：仅当前白名单SHA/PBKDF2和资源上限内参数。
   - Anubis：仅明确fast/slow、id、randomData、difficulty；对象或已知字符串API封套；未知方法不猜。
   - 仅HTTP兼容问题：先选择标准http_version；有独立证据且helper实际已配置时可选一次指纹profile。
   - 无对应算法：不把厂商名称、CDN标识或网页指令当成求解配方。
5. **只算proof还是要完成链？** 只研究公开数据用solver；当前任务明确授权验证提交时用flow。plan用来检查字段，不发包。protocol_only不提交。
6. **提交是否已发生？** 已尝试则禁止换参数重放，先利用现有证据。不能把超时等同“服务器未收到”。
7. **剩余不同策略有新证据吗？** 有且预算允许，使用不同机制；没有则结束该受阻分支，AI按实际工具主动选择可行任务。
8. **是否需要媒体/JS/浏览器？** 不内置、不自动下载或批量解码。AI仅在真实可用且已授权时主动触发，不新增人工交互流程。
9. **报告结论分层：** protocol_supported → proof_computed → submission_attempted → business_confirmed；各阶段不得互相替代。

不得反复生成临时脚本、换超时重解同题，或借其他Shell/MCP入口绕开止损。预算不是全局隔离墙，调用者也必须遵守当前任务边界。
