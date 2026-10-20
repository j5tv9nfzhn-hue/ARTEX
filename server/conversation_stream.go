package server

// 会话级 SSE：GET /api/conversations/{id}/events?since=<cursor>
//
// 为什么需要这个文件：探索任务（task）有 Broadcaster，引擎每写一条活动就 Publish 一次，
// streamActivity 直接订阅即可。会话（conversation）没有发布点 —— pgSendConversationMessage
// 的注释写明 "steps stream to conversation_activities, polled by the client"，
// 所以控制台聊天页与 harness 都只能轮询。
//
// 这里不改写入路径（不碰 agent 循环），只把「读」包装成长连接：
// 服务端 700ms 打一次增量查询，有新行就推给浏览器。数据源仍是 conversation_activities
// 这一张表，因此不存在第二份状态，也不会出现实时流与历史不一致。
//
// 契约与 streamActivity 对齐：
//   - 每帧 `id: <seq>` + `data: <Activity JSON>`（前端按 seq 去重）
//   - 支持 Last-Event-ID：浏览器自动重连时优先于 ?since=
//   - 20s 一次 `: ping` 心跳，防止中间层断流
//
// 以后若在 AppendConvActivity 的调用点补上 Broadcaster.Publish，只需把这里的
// tick 分支换成订阅，handler 与前端都不用动。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// convStreamTick 是服务端补查的节奏。700ms 对「人看时间线」足够，单条连接每拍一次
// 只读查询（走 conversation_id + id 索引），代价远低于浏览器端每秒一条 HTTP 请求。
const convStreamTick = 700 * time.Millisecond

func (s *Server) streamConversation(w http.ResponseWriter, r *http.Request) {
	pg, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}

	// 游标优先级：自动重连带的 Last-Event-ID 覆盖 query，保证断点续传精确。
	since := int64(atoiDefault(r.URL.Query().Get("since"), 0))
	if le := r.Header.Get("Last-Event-ID"); le != "" {
		if n, err := strconv.ParseInt(le, 10, 64); err == nil {
			since = n
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	tick := time.NewTicker(convStreamTick)
	defer tick.Stop()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-tick.C:
			items, cursor, err := pg.ConvActivityList(c.ID, since, 200)
			if err != nil {
				// 查询失败不结束流：下一拍重试，客户端游标没动，不会漏步骤。
				continue
			}
			for _, a := range items {
				// 用前端同款 DTO：/messages 返回的就是 activityDTO，detail 才不会被 json:"-" 吞掉。
				b, err := json.Marshal(activityDTO(a))
				if err != nil {
					continue
				}
				fmt.Fprintf(w, "id: %d\ndata: %s\n\n", a.ID, b)
				if a.ID > since {
					since = a.ID
				}
			}
			if cursor > since {
				since = cursor
			}
			flusher.Flush()
		}
	}
}
