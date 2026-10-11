/**
 * Script: extract-i18n.ts。
 * ARTEX UI 文案提取器，使用 TypeScript AST 提取 src 下 .ts、.tsx 中
 * 硬编码、包含汉字的用户消息：字符串、模板字符串和 JSX 文本。
 * 例如 title: 仪表盘、已删除 {var0} 个对话、<span>故障转移</span>。
 * 注释不是消息提取目标，但项目语言检查器仍检查注释中的韩文残留。
 * 输出 zh.json 中文词库、ko.json 中文兼容副本与 zh.sources.json 来源元数据。
 * 提取结果按路径命名空间保存，如 app.main.system.llm.page；
 * 原文 sha1 前八位为键，同一文案获得稳定键，减少重复提取的差异。
 * 递归增量合并，保留人工键与已改为 t(key) 的键，不自动删除旧键。
 * en/es 保留已有译文，新提取键暂用中文回退。运行：npm run extract:i18n。
 */

import * as ts from "typescript";

import { type CatalogTree, mergeCatalogTree } from "../i18n/catalog-merge";
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";

const SRC_DIR = path.resolve(__dirname, "..");
const MESSAGES_DIR = path.resolve(__dirname, "../../messages");
const REPO_WEB_DIR = path.resolve(__dirname, "../..");

/* 是否包含至少一个 Han 字符，是本消息提取器的筛选标准。 */
const HAN = /\p{Script=Han}/u;

/* 排除提取脚本自身与构建产物目录。 */
const SKIP_DIRS = new Set(["scripts", "node_modules", ".next"]);

type Kind = "string" | "template" | "jsx";

interface Message {
  ns: string;
  key: string;
  text: string;
  kind: Kind;
  placeholders: string[]; // 模板中 ${...} 的原始表达式，仅供参考。
  occurrences: string[]; // 相对路径与行号。
}

/* 将 src 相对路径转换为点分命名空间。 */
function toNamespace(absFile: string): string {
  const rel = path.relative(SRC_DIR, absFile).replace(/\.[tj]sx?$/, "");
  return rel
    .split(path.sep)
    .map((seg) =>
      seg
        .replace(/^\((.*)\)$/, "$1") // 去除路由组括号：(main) → main。
        .replace(/^_/, "") // _components → components
        .replace(/[^A-Za-z0-9\u4e00-\u9fff]+/g, "-")
        .replace(/^-+|-+$/g, ""),
    )
    .filter(Boolean)
    .join(".");
}

function hashKey(text: string): string {
  return crypto.createHash("sha1").update(text).digest("hex").slice(0, 8);
}

/* 递归收集 .ts、.tsx 文件。 */
function collectFiles(dir: string, out: string[] = []): string[] {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      if (SKIP_DIRS.has(entry.name)) continue;
      collectFiles(path.join(dir, entry.name), out);
    } else if (/\.(ts|tsx)$/.test(entry.name)) {
      out.push(path.join(dir, entry.name));
    }
  }
  return out;
}

/* 将模板重建为固定文本与 {varN} 占位符；固定文本无汉字时返回 null。 */
function reconstructTemplate(node: ts.TemplateExpression): { text: string; placeholders: string[] } | null {
  let text = node.head.text;
  const placeholders: string[] = [];
  node.templateSpans.forEach((span, i) => {
    placeholders.push(span.expression.getText());
    text += `{var${i}}${span.literal.text}`;
  });
  // 仅 ${} 内包含汉字时，由对应内部节点单独提取。
  if (!HAN.test(node.head.text) && !node.templateSpans.some((s) => HAN.test(s.literal.text))) {
    return null;
  }
  return { text, placeholders };
}

/* JSX 文本按浏览器规则规范化：去除首尾空白，连续空格和换行合并为单个空格。 */
function normalizeJsxText(raw: string): string {
  return raw.replace(/\s+/g, " ").trim();
}

const messages = new Map<string, Message>(); // "ns\u0000key" → Message
const scannedFiles = collectFiles(SRC_DIR).sort();
const capturedLines = new Map<string, Set<number>>(); // 每个文件中命中提取节点的行号集合。
let fileWithHan = 0;

function record(ns: string, text: string, kind: Kind, placeholders: string[], relFile: string, line: number) {
  const key = hashKey(text);
  const id = `${ns}\u0000${key}`;
  let msg = messages.get(id);
  if (!msg) {
    msg = { ns, key, text, kind, placeholders, occurrences: [] };
    messages.set(id, msg);
  }
  const occ = `${relFile}:${line}`;
  if (!msg.occurrences.includes(occ)) msg.occurrences.push(occ);
}

for (const absFile of scannedFiles) {
  const source = fs.readFileSync(absFile, "utf8");
  if (!HAN.test(source)) continue;
  fileWithHan++;

  const relFile = path.relative(REPO_WEB_DIR, absFile);
  const ns = toNamespace(absFile);
  const scriptKind = absFile.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
  const sf = ts.createSourceFile(absFile, source, ts.ScriptTarget.Latest, true, scriptKind);
  const lineOf = (pos: number) => sf.getLineAndCharacterOfPosition(pos).line + 1;
  const markLine = (pos: number) => {
    const set = capturedLines.get(relFile) ?? new Set<number>();
    set.add(lineOf(pos));
    capturedLines.set(relFile, set);
  };

  const visit = (node: ts.Node): void => {
    if (ts.isStringLiteralLike(node) && !ts.isTemplateExpression(node.parent)) {
      // 字符串和无插值模板；带插值模板的 head、middle 不在此分支处理。
      if (HAN.test(node.text)) {
        record(ns, node.text, "string", [], relFile, lineOf(node.getStart(sf)));
        markLine(node.getStart(sf));
      }
    } else if (ts.isTemplateExpression(node)) {
      const r = reconstructTemplate(node);
      if (r) {
        record(ns, r.text, "template", r.placeholders, relFile, lineOf(node.getStart(sf)));
        markLine(node.getStart(sf));
      }
    } else if (ts.isJsxText(node)) {
      const text = normalizeJsxText(node.text);
      if (text && HAN.test(text)) {
        record(ns, text, "jsx", [], relFile, lineOf(node.getStart(sf)));
        markLine(node.getStart(sf));
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
}

// 组装嵌套 JSON。
type Tree = { [k: string]: Tree | string };

function setNested(root: Tree, nsPath: string, key: string, value: string) {
  const segs = nsPath.split(".");
  let node: Tree = root;
  for (const seg of segs) {
    if (typeof node[seg] !== "object") node[seg] = {};
    node = node[seg] as Tree;
  }
  node[key] = value;
}

/* 递归排序键，尽量减少重新提取的差异。 */
function sortTree(t: Tree): Tree {
  const out: Tree = {};
  for (const k of Object.keys(t).sort()) {
    const v = t[k];
    out[k] = typeof v === "string" ? v : sortTree(v);
  }
  return out;
}

const zhTree: Tree = {};
const sources: Record<string, { text: string; kind: Kind; placeholders: string[]; occurrences: string[] }> = {};

for (const msg of messages.values()) {
  setNested(zhTree, msg.ns, msg.key, msg.text);
  sources[`${msg.ns}.${msg.key}`] = {
    text: msg.text,
    kind: msg.kind,
    placeholders: msg.placeholders,
    occurrences: msg.occurrences.sort(),
  };
}

const sortedSources: typeof sources = {};
for (const k of Object.keys(sources).sort()) sortedSources[k] = sources[k];

fs.mkdirSync(MESSAGES_DIR, { recursive: true });
const write = (name: string, data: unknown) =>
  fs.writeFileSync(path.join(MESSAGES_DIR, name), `${JSON.stringify(data, null, 2)}\n`, "utf8");

/* 只有文件不存在时才返回空树；坏 JSON 必须失败，不能静默清空人工文案。 */
function readTree(name: string): Tree {
  try {
    return JSON.parse(fs.readFileSync(path.join(MESSAGES_DIR, name), "utf8")) as Tree;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return {};
    throw error;
  }
}

const chinese = sortTree(mergeCatalogTree(readTree("zh.json"), zhTree, true));
write("zh.json", chinese);
write("ko.json", chinese); // 旧路径只保留中文兼容副本，不再提供韩文界面。
for (const locale of ["en", "es"]) {
  write(`${locale}.json`, sortTree(mergeCatalogTree(readTree(`${locale}.json`), chinese)));
}
write("zh.sources.json", sortedSources);

// 摘要报告。
const byKind: Record<Kind, number> = { string: 0, template: 0, jsx: 0 };
const byNs = new Map<string, number>();
let totalOccurrences = 0;
for (const msg of messages.values()) {
  byKind[msg.kind]++;
  byNs.set(msg.ns, (byNs.get(msg.ns) ?? 0) + 1);
  totalOccurrences += msg.occurrences.length;
}

// 覆盖率估算：含汉字的源码行中，提取节点命中的比例。
// 未命中的部分主要为不参与消息提取的注释。
let hanLines = 0;
let capturedHanLines = 0;
for (const absFile of scannedFiles) {
  const relFile = path.relative(REPO_WEB_DIR, absFile);
  const lines = fs.readFileSync(absFile, "utf8").split(/\r?\n/);
  const capSet = capturedLines.get(relFile) ?? new Set<number>();
  lines.forEach((ln, i) => {
    if (HAN.test(ln)) {
      hanLines++;
      if (capSet.has(i + 1)) capturedHanLines++;
    }
  });
}

const topNs = [...byNs.entries()].sort((a, b) => b[1] - a[1]).slice(0, 15);

console.log("─".repeat(64));
console.log("ARTEX i18n 提取完成");
console.log("─".repeat(64));
console.log(`扫描文件数            ： ${scannedFiles.length} （src/ 下 .ts/.tsx，不含 scripts）`);
console.log(`含汉字文件数          ： ${fileWithHan}`);
console.log(`唯一消息数（键）      ： ${messages.size}`);
console.log(`  ├─ 字符串字面量      ： ${byKind.string}`);
console.log(`  ├─ 模板字面量        ： ${byKind.template}`);
console.log(`  └─ JSX 文本          ： ${byKind.jsx}`);
console.log(`总出现位置            ： ${totalOccurrences} （包含重复使用）`);
console.log(`命名空间数            ： ${byNs.size}`);
console.log("");
console.log(`含汉字源码行数        ： ${hanLines}`);
console.log(`  └─ 提取覆盖行数      ： ${capturedHanLines} (${((capturedHanLines / hanLines) * 100).toFixed(1)}%)`);
console.log(`     其余 ${hanLines - capturedHanLines} 行主要为代码注释（不提取）`);
console.log("");
console.log("消息最多的前 15 个命名空间：");
for (const [ns, n] of topNs) console.log(`  ${String(n).padStart(4)}  ${ns}`);
console.log("");
console.log(`输出：web/messages/{zh.json, ko.json, en.json, es.json, zh.sources.json}`);
console.log("─".repeat(64));
