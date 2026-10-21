package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/mcpserver"
)

// L3 桥：stdio 进程里没有引擎，建任务/控任务只能委派给有引擎的进程。
//
// 一个必须先说清的事实：**headless 实例没有引擎**。它只是同一份代码的另一个
// 入口，`task_create` 在它身上会往库里插一行，然后没有任何人去跑——那是最坏的
// 失败形态（看起来成功了，任务永远 pending）。所以 L3 只桥给**控制台**：
// 引擎、调度、审批都在 8787 那个进程里。
//
// 于是这里的行为刻意很笨：探活 → 转发 → 失败原样抛出。不拉进程、不重试、
// 不降级。控制台没开就明确报错，附带「怎么开」。
const (
	// bridgeProbeTTL 是「确认控制台还活着」的缓存时长。
	// 每次 L3 调用都探活 2 s 太贵；但缓存太久会在控制台退出后继续往死地址发。
	bridgeProbeTTL = 5 * time.Second
	// bridgeProbeTimeout 是单次探活超时。回环端口要么秒连要么秒拒。
	bridgeProbeTimeout = 1500 * time.Millisecond
)

// consoleL3Bridge 返回一个可以塞进 L3 工具 handler 的转发函数。
//
// addrFlag 是命令行 --addr；为空时默认 127.0.0.1:8787（控制台）。
// 用户显式给了地址就只认这个地址：他们可能把控制台开在别的端口上。
func consoleL3Bridge(token, addrFlag string) func(context.Context, string, json.RawMessage) (string, error) {
	target := strings.TrimSpace(addrFlag)
	if target == "" {
		target = "http://127.0.0.1:8787"
	}
	if i := strings.Index(target, ","); i >= 0 {
		// --addr 支持逗号分隔的候选列表；L3 只认第一个（用户写在前面的更具体）。
		target = strings.TrimSpace(target[:i])
	}

	var (
		mu      sync.Mutex
		lastOK  time.Time
		lastErr error
	)

	probe := func(ctx context.Context) (mcpserver.Endpoint, error) {
		mu.Lock()
		defer mu.Unlock()
		if lastErr == nil && !lastOK.IsZero() && time.Since(lastOK) < bridgeProbeTTL {
			return mcpserver.Endpoint{URL: target, Token: token}.Normalized(), nil
		}
		info := mcpserver.Probe(ctx, mcpserver.Endpoint{URL: target, Token: token}, bridgeProbeTimeout)
		if !info.Alive {
			lastOK = time.Time{}
			lastErr = errors.New(firstNonEmpty(info.Error, "连接失败"))
			return mcpserver.Endpoint{}, lastErr
		}
		lastOK = time.Now()
		lastErr = nil
		return mcpserver.Endpoint{URL: info.URL, Token: token}, nil
	}

	return func(ctx context.Context, name string, raw json.RawMessage) (string, error) {
		ep, err := probe(ctx)
		if err != nil {
			return "", fmt.Errorf(
				"L3 需要 ARTEX 控制台在运行：%s 连不上（%v）。"+
					"任务编排委派给控制台进程里的引擎；headless 实例没有引擎，拉起来也只会让任务停在 pending",
				target, err)
		}
		out, err := mcpserver.CallTool(ctx, ep, name, raw, 0)
		if err != nil {
			// 401 会让缓存失效：多半是有人轮换了 token，重探一次比继续用旧结论好。
			if strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "token 不匹配") {
				mu.Lock()
				lastOK = time.Time{}
				lastErr = err
				mu.Unlock()
			}
			return "", err
		}
		return out, nil
	}
}

// hasTier 判断档位集合里是否包含某一档。
func hasTier(tiers []mcpserver.Tier, want mcpserver.Tier) bool {
	for _, t := range tiers {
		if t == want {
			return true
		}
	}
	return false
}
