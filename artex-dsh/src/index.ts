// artex-dsh —— ARTEX-HYDRA 的 DSH（DeepSeek Harness）原生插件。
//
// 与走官方 @deepseek-ai/dsh-mcp-client 桥接相比，这个插件多拿三件东西：
//   1. 干净的工具名（artex_binscan，而不是 mcp__artex__file_identify）；
//   2. 把 ARTEX 的多步工序（识别→结构→熵→字符串→IOC）收成一个动作，
//      少浪费模型轮次；
//   3. 档位缺失时给人话提示（L2 没开 → 告诉他去 /system/mcp 打开），
//      而不是抛一个 JSON-RPC "unknown tool"。
//
// 协议端只依赖 ARTEX 的 Streamable HTTP：POST <url> + Bearer token。

import type { Context } from "@deepseek-ai/cordis";
import { defineTool } from "@deepseek-ai/dsh-tools";

export const name = "artex";
export const inject = ["tools"];

export interface ArtexConfig {
  /** ARTEX MCP 端点，默认 http://127.0.0.1:8787/mcp */
  url?: string;
  /** 直接给 token（不推荐，优先用环境变量） */
  token?: string;
  /** 从哪个环境变量读 token，默认 ARTEX_MCP_TOKEN */
  tokenEnv?: string;
  /** 单次调用超时毫秒，默认 300000（port_probe/http_fuzz 这类批量探测要留够） */
  timeoutMs?: number;
}

const DEFAULT_URL = "http://127.0.0.1:8787/mcp";

type RpcId = number;

async function rpc(
  url: string,
  token: string,
  timeoutMs: number,
  method: string,
  params: Record<string, unknown>,
): Promise<any> {
  const ac = new AbortController();
  const timer = setTimeout(() => ac.abort(), timeoutMs);
  try {
    const res = await fetch(url, {
      method: "POST",
      signal: ac.signal,
      headers: {
        "content-type": "application/json",
        // 同时接受两种响应：我们的服务端默认回 JSON，也兼容 SSE-only 的实现。
        accept: "application/json, text/event-stream",
        ...(token ? { authorization: `Bearer ${token}` } : {}),
      },
      body: JSON.stringify({ jsonrpc: "2.0", id: Date.now() as RpcId, method, params }),
    });
    if (res.status === 401) {
      throw new Error("ARTEX 拒绝了请求：token 无效。到 系统 → MCP → 对外接入 复制最新 token，或重新设置 ARTEX_MCP_TOKEN。");
    }
    if (res.status === 403) {
      throw new Error("ARTEX 的对外 MCP 已关闭（或只允许本机来源）。请在控制台 系统 → MCP → 对外接入 打开开关。");
    }
    if (!res.ok) {
      throw new Error(`ARTEX MCP HTTP ${res.status}: ${(await res.text()).slice(0, 300)}`);
    }
    const ctype = res.headers.get("content-type") ?? "";
    let payload: any;
    if (ctype.includes("text/event-stream")) {
      const raw = await res.text();
      const line = raw
        .split(/\r?\n/)
        .filter((l) => l.startsWith("data:"))
        .map((l) => l.slice(5).trim())
        .find((l) => l.length > 0);
      if (!line) throw new Error("ARTEX MCP 返回了空的 SSE 帧");
      payload = JSON.parse(line);
    } else {
      payload = await res.json();
    }
    if (payload?.error) throw new Error(payload.error.message ?? "ARTEX MCP 调用失败");
    return payload?.result;
  } finally {
    clearTimeout(timer);
  }
}

// ---- 与 ARTEX 服务端对齐的小工具 ----

const BIN_TOOLS: Record<string, string> = {
  identify: "file_identify",
  pe: "pe_info",
  strings: "strings_scan",
  entropy: "entropy_scan",
  ioc: "ioc_extract",
};

const RECON_ACTIONS: Record<string, string> = {
  dns: "dns_recon",
  ports: "port_probe",
  tls: "tls_inspect",
  tech: "tech_hints",
};

const WEB_ACTIONS: Record<string, string> = {
  send: "http_send",
  fuzz: "http_fuzz",
  dir: "dir_probe",
  param: "param_probe",
  jwt: "jwt_inspect",
  sqli: "sqli_probe",
};

const TIER_HINT: Record<string, string> = {
  dns_recon: "L2",
  port_probe: "L2",
  tls_inspect: "L2",
  tech_hints: "L2",
  http_send: "L2",
  http_fuzz: "L2",
  dir_probe: "L2",
  param_probe: "L2",
  jwt_inspect: "L2",
  sqli_probe: "L2",
  task_create: "L3",
  task_status: "L3",
  task_pause: "L3",
  task_resume: "L3",
};

/** 把 ARTEX 的报错翻译成 DSH 里能直接照着做的提示。 */
function explain(tool: string, err: unknown): Error {
  const msg = err instanceof Error ? err.message : String(err);
  if (msg.includes("unknown tool")) {
    const tier = TIER_HINT[tool];
    if (tier) {
      return new Error(
        `${tool} 属于 ${tier} 档，当前没有开放。到 ARTEX 控制台 系统 → MCP → 对外接入，把 ${tier} 打开（${tier === "L3" ? "会真的建任务" : "会出网探测"}，确认授权范围后再开）。`,
      );
    }
  }
  return new Error(`ARTEX ${tool} 调用失败：${msg}`);
}

export function apply(ctx: Context, config: ArtexConfig = {}) {
  const url = config.url ?? process.env.ARTEX_MCP_URL ?? DEFAULT_URL;
  const tokenEnv = config.tokenEnv ?? "ARTEX_MCP_TOKEN";
  const token = config.token ?? process.env[tokenEnv] ?? "";
  const timeoutMs = config.timeoutMs ?? 300_000;

  const call = async (tool: string, args: Record<string, unknown>): Promise<string> => {
    try {
      const result = await rpc(url, token, timeoutMs, "tools/call", { name: tool, arguments: args });
      const text = (result?.content ?? [])
        .filter((c: any) => c?.type === "text")
        .map((c: any) => c.text)
        .join("\n");
      if (result?.isError) throw new Error(text || "ARTEX 工具返回了错误");
      return text;
    } catch (err) {
      throw explain(tool, err);
    }
  };

  const textOutput = { schema: { type: "string" as const }, render: (_a: any, v: string) => [{ type: "text" as const, text: v }] };

  ctx.tools.register(
    defineTool({
      name: "artex_binscan",
      description:
        "用 ARTEX 的纯 Go 引擎静态分诊一个样本（不执行它）：魔数/架构/节表与熵/可打印字符串/IOC 汇总。样本在桌面或下载目录时直接给绝对路径。",
      parameters: {
        path: { type: "string", required: true, description: "样本绝对路径" },
        kind: {
          type: "string",
          description: "identify（默认）| pe | strings | entropy | ioc",
          default: "identify",
        },
      },
      output: textOutput,
      async execute(args: any) {
        const tool = BIN_TOOLS[String(args.kind ?? "identify")] ?? BIN_TOOLS.identify;
        return call(tool, { path: args.path });
      },
    }),
  );

  ctx.tools.register(
    defineTool({
      name: "artex_recon",
      description:
        "对目标做一次主动验证（只判定不利用）：DNS 记录、TCP 端口连通、TLS 证书、技术栈指纹。需要 ARTEX 开放 L2 档。",
      parameters: {
        target: { type: "string", required: true, description: "域名、主机或 URL" },
        action: { type: "string", required: true, description: "dns | ports | tls | tech" },
        ports: { type: "string", description: "action=ports 时的端口表达式，如 80,443,8000-8100" },
      },
      output: textOutput,
      async execute(args: any) {
        const tool = RECON_ACTIONS[String(args.action)] ?? RECON_ACTIONS.dns;
        const payload: Record<string, unknown> =
          tool === "dns_recon"
            ? { domain: args.target }
            : tool === "port_probe"
              ? { host: args.target, ports: args.ports ?? "80,443" }
              : tool === "tls_inspect"
                ? { target: args.target }
                : { url: args.target };
        return call(tool, payload);
      },
    }),
  );

  ctx.tools.register(
    defineTool({
      name: "artex_web",
      description:
        "Web 侧验证动作：发原始请求、FUZZ 模板、目录探测、参数探测、JWT 解析、SQLi 只判定探测。需要 ARTEX 开放 L2 档。",
      parameters: {
        action: { type: "string", required: true, description: "send | fuzz | dir | param | jwt | sqli" },
        target: { type: "string", description: "URL 或原始请求文本（action=send/fuzz 时）" },
        param: { type: "string", description: "action=param/sqli 时的参数名" },
        token: { type: "string", description: "action=jwt 时的 JWT" },
        payloads: { type: "array", description: "action=fuzz 时的载荷列表" },
      },
      output: textOutput,
      async execute(args: any) {
        const tool = WEB_ACTIONS[String(args.action)] ?? WEB_ACTIONS.send;
        const payload: Record<string, unknown> = {};
        if (tool === "http_send" || tool === "http_fuzz") payload.request = args.target;
        else payload.url = args.target;
        if (args.param) payload.param = args.param;
        if (args.token) payload.token = args.token;
        if (Array.isArray(args.payloads)) payload.payloads = args.payloads;
        return call(tool, payload);
      },
    }),
  );

  ctx.tools.register(
    defineTool({
      name: "artex_assets",
      description: "查 ARTEX 已经攒下的资产图（域名/IP/子域/应用/服务/端点），避免重复扫描已知目标。",
      parameters: {
        query: { type: "string", description: "关键词：域名、IP、URL、指纹" },
        type: { type: "string", description: "root_domain | ip | subdomain | app | service | endpoint" },
        task_id: { type: "string", description: "只看某个任务名下的资产" },
        limit: { type: "number", description: "返回条数，默认 50，上限 200" },
      },
      output: textOutput,
      async execute(args: any) {
        return call("list_assets", {
          query: args.query,
          type: args.type,
          task_id: args.task_id,
          limit: args.limit,
        });
      },
    }),
  );

  ctx.tools.register(
    defineTool({
      name: "artex_findings",
      description: "查历史漏洞记录（含摘要与证据片段），优先复现历史结论而不是重新挖一遍。",
      parameters: {
        task_id: { type: "string", description: "任务 id" },
        severity: { type: "string", description: "critical | high | medium | low" },
        status: { type: "string", description: "pending | confirmed | false_positive | …" },
        query: { type: "string", description: "关键词" },
        page: { type: "number", description: "页码，默认 1" },
        page_size: { type: "number", description: "每页条数，默认 20" },
      },
      output: textOutput,
      async execute(args: any) {
        return call("list_findings", args);
      },
    }),
  );

  ctx.tools.register(
    defineTool({
      name: "artex_tasks",
      description: "列 ARTEX 任务（id/名称/状态/分析域/目标摘要）。",
      parameters: {
        status: { type: "string", description: "created | running | paused | done | failed | timeout" },
        limit: { type: "number", description: "返回条数，默认 50" },
      },
      output: textOutput,
      async execute(args: any) {
        return call("list_tasks", args);
      },
    }),
  );

  ctx.tools.register(
    defineTool({
      name: "artex_kb",
      description: "ARTEX 战术知识库：漏洞打法卡片、业务不变量清单、历史任务结论与技能检索。",
      parameters: {
        mode: {
          type: "string",
          required: true,
          description: "vuln_patterns | logic_invariants | past_findings | past_assets",
        },
        query: { type: "string", description: "关键词或目标类型（如 ai_gateway、jwt、ssrf）" },
        category: { type: "string", description: "vuln_patterns 下的分类，如 bola_idor / ssrf_cloud" },
        limit: { type: "number", description: "默认 8，上限 25" },
      },
      output: textOutput,
      async execute(args: any) {
        return call("tactical_kb", args);
      },
    }),
  );

  ctx.tools.register(
    defineTool({
      name: "artex_task",
      description:
        "把一次攻坚委派给 ARTEX 的后台任务（需要 L3 档）。description 写清范围与授权边界，goal 写要验证的结论；返回 task_id，用 artex_task_status 跟进。",
      parameters: {
        description: { type: "string", required: true, description: "任务描述：目标范围、授权边界、已知线索" },
        goal: { type: "string", required: true, description: "验证目标" },
        name: { type: "string", description: "可选任务名" },
      },
      output: textOutput,
      async execute(args: any) {
        return call("task_create", {
          description: args.description,
          goal: args.goal,
          name: args.name,
        });
      },
    }),
  );

  ctx.tools.register(
    defineTool({
      name: "artex_task_status",
      description: "查委派出去的 ARTEX 任务进度（状态、资产计数、最近发现），用于轮询。",
      parameters: { id: { type: "string", required: true, description: "任务 id" } },
      output: textOutput,
      async execute(args: any) {
        return call("task_status", { id: args.id });
      },
    }),
  );

  ctx.tools.register(
    defineTool({
      name: "artex_task_pause",
      description: "暂停一个 ARTEX 任务（不删除数据；恢复用 artex_task_resume）。",
      parameters: { id: { type: "string", required: true, description: "任务 id" } },
      output: textOutput,
      async execute(args: any) {
        return call("task_pause", { id: args.id });
      },
    }),
  );

  ctx.tools.register(
    defineTool({
      name: "artex_task_resume",
      description: "恢复一个被暂停的 ARTEX 任务。",
      parameters: { id: { type: "string", required: true, description: "任务 id" } },
      output: textOutput,
      async execute(args: any) {
        return call("task_resume", { id: args.id });
      },
    }),
  );

  // 常驻提示段与斜杠命令按 DSH 的可选能力尽力挂载：不支持时静默跳过，
  // 不影响工具可用性（Cordis 插件常见的兼容写法）。
  const anyCtx = ctx as any;
  if (typeof anyCtx.systemPrompt?.add === "function") {
    anyCtx.systemPrompt.add({
      id: "artex",
      text:
        "本机跑着 ARTEX-HYDRA（渗透与安全分析工具箱）。需要『这条路径到底通不通』时，用 artex_* 工具拿证据，" +
        "而不是凭感觉下结论：样本先 artex_binscan，资产与历史结论先 artex_assets / artex_findings，" +
        "主动探测只在授权范围内用 artex_recon / artex_web。所有结论都要附工具返回的证据片段。",
    });
  }
  if (typeof anyCtx.commands?.register === "function") {
    anyCtx.commands.register({
      name: "artex",
      description: "查看 ARTEX 可用工具与档位状态",
      async run() {
        const result = await rpc(url, token, 15000, "tools/list", {});
        const names = (result?.tools ?? []).map((t: any) => t.name);
        return `ARTEX 端点 ${url}\n已开放 ${names.length} 个工具：\n${names.join(", ")}`;
      },
    });
  }
}
