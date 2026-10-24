// P2 OpenAPI 面：GET /api/openapi.json 返回本平台「协议面 + 核心资源」的
// OpenAPI 3.1 描述（手写规范文档，纯只读新增路由；SDK 可据此生成客户端）。
package server

import "net/http"

// apiOpenAPI serves the hand-written OpenAPI 3.1 document.
func (s *Server) apiOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(openapiSpec))
}

const openapiSpec = `{
  "openapi": "3.1.0",
  "info": {
    "title": "ARTEX-HYDRA Protocol Facade",
    "version": "artex-rea.p1",
    "description": "Codex app-server 风格的 Thread/Turn/Item/Approval 协议面（薄壳转发+聚合）。所有 /api/protocol/* 均在既有 REST 之上，经同一鉴权与同一业务处理器执行；data 字段为上游处理器原始响应的再封装。领域 REST（/api/tasks /api/exploration /api/intercept 等）继续全量可用。"
  },
  "paths": {
    "/api/protocol/threads": {
      "get": {"summary": "列出线程(=任务)，query 透传 GET /api/tasks", "responses": {"200": {"description": "thread 列表（data=上游响应）"}}},
      "post": {"summary": "创建线程(=任务)，body 透传 POST /api/tasks", "responses": {"200": {"description": "创建结果"}}}
    },
    "/api/protocol/threads/{id}": {
      "get": {"summary": "线程快照，id 形如 task-<数字>，映射 GET /api/tasks/{id}", "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}], "responses": {"200": {"description": "线程快照"}, "400": {"description": "非法线程标识"}}}
    },
    "/api/protocol/threads/{id}/turns": {
      "post": {
        "summary": "创建轮次：kind=chat(主 Agent，要求线程为当前激活任务) | intent(向暂停/运行中 Worker 意图递话)",
        "requestBody": {"content": {"application/json": {"schema": {"type": "object", "properties": {
          "kind": {"type": "string", "enum": ["chat", "intent"]},
          "intent_id": {"type": "integer"},
          "message": {"type": "string"},
          "payload": {"description": "上游 DTO 原样透传（优先于 message 糖）"}
        }, "required": ["kind"]}}}},
        "responses": {"200": {"description": "轮次已受理"}, "409": {"description": "chat 轮次线程非激活任务"}}
      }
    },
    "/api/protocol/threads/{id}/steer": {
      "post": {
        "summary": "向运行中 work 递纠偏（不打断），映射 POST /api/tasks/{id}/intents/{iid}/steer",
        "requestBody": {"content": {"application/json": {"schema": {"type": "object", "properties": {
          "intent_id": {"type": "integer"}, "text": {"type": "string"}, "message": {"type": "string"}, "payload": {"description": "上游 DTO 透传"}
        }, "required": ["intent_id"]}}}},
        "responses": {"200": {"description": "已递交"}}
      }
    },
    "/api/protocol/threads/{id}/kill": {
      "post": {
        "summary": "强制终止运行中 work（引擎 kill_work 语义：AbortKilledByPlanner + 方向门控 hard bump）",
        "requestBody": {"content": {"application/json": {"schema": {"type": "object", "properties": {"intent_id": {"type": "integer"}}, "required": ["intent_id"]}}}},
        "responses": {"200": {"description": "已终止"}, "409": {"description": "无运行中 work / 任务删除中"}}
      }
    },
    "/api/protocol/threads/{id}/control": {
      "post": {"summary": "线程级控制动词，body 透传 POST /api/tasks/{id}/control", "responses": {"200": {"description": "控制结果"}}}
    },
    "/api/protocol/threads/{id}/work/{iid}/control": {
      "post": {"summary": "work 级 pause|resume|cancel，body 透传 POST /api/tasks/{id}/intents/{iid}/control", "responses": {"200": {"description": "控制结果"}}}
    },
    "/api/protocol/threads/{id}/items": {
      "get": {
        "summary": "线程事项双账本并排：activity(领域活动) + trace(P0 语义转录，?after=&limit= 游标)",
        "responses": {"200": {"description": "{activity, trace:{events,next,total}}"}}
      }
    },
    "/api/protocol/threads/{id}/tree": {
      "get": {
        "summary": "P2 会话树：checkpoints(可回滚时间线) + main_sessions(主 Agent 分段) + trace_threads；回滚仍走既有 revert/rollback 端点",
        "responses": {"200": {"description": "会话树"}}
      }
    },
    "/api/protocol/approvals": {
      "get": {"summary": "待审批列表（=拦截审批队列），query 透传 GET /api/intercept/pending", "responses": {"200": {"description": "approval 列表"}}}
    },
    "/api/protocol/approvals/{id}": {
      "get": {"summary": "单条审批，映射 GET /api/intercept/pending/{id}", "responses": {"200": {"description": "审批详情"}}},
      "post": {"summary": "裁决审批（批准/拒绝），body 透传 POST /api/intercept/pending/{id}/decide", "responses": {"200": {"description": "裁决结果"}, "400": {"description": "已处理或不存在"}}}
    },
    "/api/protocol/hooks": {
      "get": {"summary": "P1 hooks 插桩链自省（空 slots=无扩展挂载，默认 no-op）", "responses": {"200": {"description": "槽位列表"}}}
    },
    "/api/traces": {
      "get": {"summary": "P0 语义转录线程列表（newest first）", "responses": {"200": {"description": "thread 列表"}}}
    },
    "/api/traces/{thread}": {
      "get": {"summary": "P0 语义事件时间线（?after= 行游标 &limit=，next==total 即读尽）", "responses": {"200": {"description": "事件流"}}}
    },
    "/api/openapi.json": {
      "get": {"summary": "本文档", "responses": {"200": {"description": "OpenAPI 3.1 文档"}}}
    }
  },
  "x-legacy-mapping": {
    "thread": "task（/api/tasks*）",
    "turn.chat": "POST /api/chat（active-task 语义）",
    "turn.intent": "POST /api/tasks/{id}/intents/{iid}/messages（body: {message, request_id}）",
    "steer": "POST /api/tasks/{id}/intents/{iid}/steer（body: {text}）",
    "work.control": "POST /api/tasks/{id}/intents/{iid}/control（pause|resume|cancel）",
    "kill": "Engine.KillWork（AbortKilledByPlanner + 方向门控）",
    "approval": "/api/intercept/pending*（拦截审批队列）",
    "item.activity": "GET /api/exploration/activity*",
    "item.trace": "GET /api/traces*（P0 语义转录）",
    "undo": "POST /api/tasks/{id}/checkpoints/{cid}/revert | POST /api/tasks/{id}/messages/{seq}/rollback"
  }
}`
