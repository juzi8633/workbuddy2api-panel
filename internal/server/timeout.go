package server

import (
	"context"
	"errors"
	"net"
	"os"
)

// errUpstreamTimeout 上游超时/停滞的哨兵错误：轮转已止损（不再换号），末端据此
// 返回可区分的 code=upstream_timeout，避免与 no_healthy_account 混为一谈——两者的
// 排查方向完全不同（前者是上游慢/卡，后者是池里没有可用号）。
var errUpstreamTimeout = errors.New("upstream timeout")

// isUpstreamTimeout 判定传输层错误是否属于「上游超时 / 停滞」这一类。
//
// 为什么需要单独判定：超时不是账号的问题。同一份请求换到别的账号，撞上的是同一个
// 慢上游——换号只会把客户端拖到 MaxRotate × header_timeout，期间还给一串健康号
// 喂连败计数（NoteFailures）。生产实证：归档里 503 的 duration 精确为 125s
// （= 2×60s 响应头超时 + 退避），即请求确实被轮转放大了一次。
//
// 三态判定（任一命中即算超时）：
//  1. net.Error.Timeout() —— ResponseHeaderTimeout / Client.Timeout 的形态；
//  2. 显式 deadline exceeded —— context.DeadlineExceeded / os.ErrDeadlineExceeded；
//  3. 客户端仍在，但 ctx 被取消 —— 那只能是我们自己的空闲看门狗掐的流，即
//     「上游停滞」（客户端主动断连时 clientGone 为 true，不属于这一态）。
//
// clientGone 是 r.Context().Err() != nil 的求值结果，由调用方传入以保持本函数纯粹、
// 可单测（不依赖真实 http.Request）。
func isUpstreamTimeout(err error, clientGone bool) bool {
	if err == nil {
		return false
	}
	// 态 3：客户端还在等，却已经被取消 → 我方看门狗判定上游停滞。
	if !clientGone && errors.Is(err, context.Canceled) {
		return true
	}
	// 态 2：显式 deadline。
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	// 态 1：实现了 net.Error 且自报超时（os.ErrDeadlineExceeded 未覆盖的自定义形态）。
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
