import type { ShellCapability } from "@/lib/types";

/**
 * 执行类工具在 UI 上显示的名字。
 *
 * 为什么不能直接显示后端回传的 tool 名：SDK 那个工具永远叫 `Bash`，哪怕它底下跑的是
 * Windows PowerShell。用户看到「Bash」会照着 bash 语法写命令，然后 422 或语法错误，
 * 这是最典型的误导来源。真实执行器由后端 /health 的 shell 字段给出，这里只负责把
 * 工具名翻译成「人看到的那个 shell」。
 */

/** 归到「命令执行」一类的工具名。逻辑判断仍用原始名，只有显示时改写。 */
const EXEC_TOOLS = new Set([
  "Bash",
  "bash",
  "shell",
  "shell_open",
  "shell_send",
  "Shell",
  "powershell",
  "pwsh",
]);

export function isExecTool(tool: string): boolean {
  return EXEC_TOOLS.has(tool);
}

/** 把 /health.shell 归一成显示名。探测不到时退回中性的 Terminal。 */
export function shellDisplayName(shell?: ShellCapability | null): string {
  if (!shell) return "Terminal";
  const os = (shell.os || "").toLowerCase();
  const ex = (shell.executor || "").toLowerCase();
  if (ex.includes("pwsh") || ex.includes("powershell")) return "PowerShell";
  if (ex.includes("bash")) return "Bash";
  if (ex.includes("zsh") || ex.includes("sh")) return "Terminal";
  // 执行器没报出来时用平台兜底：Windows 一定是 PowerShell 系，其余按 Bash 处理。
  if (os.includes("windows") || os.startsWith("win")) return "PowerShell";
  if (os) return "Bash";
  return "Terminal";
}

/**
 * 执行类工具显示名；非执行类工具原样返回。
 *
 * 保持不变的部分很关键：工具名参与用户对行为的预期（Bash 能跑管道/&&，PowerShell 5.1
 * 不能跑 &&），所以只在「确实是命令执行」这一类上做替换，其余一律不动。
 */
export function execToolLabel(tool: string, shell?: ShellCapability | null): string {
  if (!isExecTool(tool)) return tool;
  return shellDisplayName(shell);
}
