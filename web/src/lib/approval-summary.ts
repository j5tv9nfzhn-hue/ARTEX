// 后端 msgToolApprovalRequestFmt 使用“工具 X 审批请求 (#N)”格式。
// 保留 (#N) 标记，以便详情尚未加载时即可显示审批操作。
export function parseApprovalSummary(summary: string): { pendingId: number | null; toolName: string } {
  const pending = /\(#(\d+)\)/.exec(summary);
  const tool = /工具\s+(\S+)\s+审批/.exec(summary);
  return {
    pendingId: pending ? parseInt(pending[1], 10) : null,
    toolName: tool ? tool[1] : summary,
  };
}
