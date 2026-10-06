// hint.go 网关错误附加说明字段（error.gateway_hint）的单一事实来源。
//
// 纪律（任务书 gateway-hint）：
//   - error.message 永远是上游 body 原文透传（5755fe3 透传原则不动）；
//     gateway_hint 只做与 message **并列**的网关视角补充说明，绝不替换/包装 message。
//   - 文案集中在本文件（一张 Kind 表 + 11133/11135 形态判定），按 ErrKind + 上下文
//     （请求带图/模型目录能力）映射，不散落 handler 的 if-else。
//   - 未覆盖形态返回空串 → 响应不带该字段（不编造）。
//   - hint 措辞是英文（错误响应面向客户端工具链，英文是通用口径）。
package upstream

import (
	"encoding/json"
	"net/http"
	"strings"
)

// GatewayHint 按错误形态返回网关视角的补充说明（error.gateway_hint 字段值）。
// msg 是上游错误 body 原文（或 SSE error 帧 payload）；kind 是权威分类
// （Classify / *Error 信封）。返回空串 = 未覆盖形态，调用方不带字段。
//
// ctx 携带判定 hint 所需的请求侧上下文（零值合法，信息缺失时相关形态退为中性
// hint 或无 hint）：
//   - HasImage：请求体是否携带 image_url part（11133 的「模型不支持图片」指向前提）；
//   - Model / ModelInCatalog / ModelSupportsImages：模型目录对该模型的
//     supports_images 声明（目录未收录 → 不做「不支持」判定，防查不到误判成不支持）。
//
// 语言：全部 hint 用中文。面板与错误文案的主要读者是中文用户，而上游原始 body
// （message 里的 code/msg/requestId）本来就是中文/英文字段混排——再用英文写补充
// 说明会让同一响应里出现两种语言，读起来割裂。hint 只做"该怎么办"的指引，
// 上游原文始终原样透传（message 字段），所以不存在信息丢失。
//
// 判定次序：11133/11135 上游业务码**先于** Kind 表——实测这两族归 ErrClient/
// ErrBadParams 皆有可能（Classify 词表不含 11133），hint 层自带判定（hint 是补充
// 说明非权威分类，误判代价只是多一条中性补充说明）；其余走 Kind 一对一映射。
func GatewayHint(kind ErrKind, msg string, ctx HintContext) string {
	// 11133 model_param_invalid 家族（图片回归实测：不支持图片的模型传图，或任意
	// 参数被模型供应商拒绝）。只有请求确实带图、且目录能对该模型做出「不支持图片」
	// 的判定时才给「换模型」指向，否则退中性参数形态（可能是任意参数问题，不点名图片）。
	if isModelParamInvalid(msg) {
		if ctx.HasImage && ctx.ModelInCatalog && !ctx.ModelSupportsImages {
			return "模型 " + ctx.Model + " 不支持图片输入；请从 /v1/models 中选择 supports_images=true 的模型"
		}
		return "模型供应商拒绝了请求参数；请检查消息格式与模型能力（如是否支持图片/工具调用）"
	}
	// 11135 invalid_image_data 家族（图片数据无效，Discussion #77 实测形态）。
	if isInvalidImageData(msg) {
		return "上游拒绝了图片数据；请使用有效图片，必要时新建会话后重试"
	}
	switch kind {
	case ErrPromptTooLong:
		return "请求上下文超出模型上限；请减少历史消息或缩短内容"
	case ErrImageInvalid:
		return "上游拒绝了图片请求；请检查 image_url 格式与图片数据"
	case ErrWafBlock:
		// 账号级 WAF 403 与 IP 级 fail-fast 同 hint：两者对客户端的动作一致
		// （等待窗口过去再试，换号/立刻重试无意义）。
		return "上游 WAF 拦截了网关；请在封禁窗口过后再试"
	case ErrSoftRate:
		return "被上游限流；请在额度重置后重试"
	case ErrAccountFault:
		return "上游账号级故障（授权/配额状态）；网关会自动轮换或禁用该账号"
	case ErrSessionDead:
		return "上游账号会话已失效；该账号已被禁用，需重新登录后才能使用"
	case ErrHardCredit:
		return "上游账号积分已耗尽；等待每日签到恢复额度"
	case ErrModelBlocked:
		return "该后端没有此模型；请切换模型，或稍后在别的账号上重试"
	case ErrBadParams:
		// 11101：请求体自身的形态问题，与账号无关；重试/换号/换模型都不会变。
		// hint 必须说清"改请求"，否则客户端会对同一 body 无限重试（#99 的动机）。
		return "上游认为请求体格式有误；请检查 JSON 结构与消息格式"
	case ErrContentBlocked:
		// 措辞不含 "upstream"：content_blocked 响应有不含上游字样的既有口径
		// （handler_test 的泄漏守卫），hint 遵守同一口径。
		return "请求内容被内容策略拦截；请调整提示词后重试"
	default:
		// ErrNone/ErrNotFound/ErrServer/ErrClient 等未覆盖形态：无 hint。
		return ""
	}
}

// HintContext gateway_hint 判定所需的请求侧上下文（handler 侧组装，见
// Handler.hintContext）。零值合法。
type HintContext struct {
	Model               string // 请求裸模型名（可空）
	HasImage            bool   // 请求体是否携带 image_url part
	ModelSupportsImages bool   // 模型目录 supports_images 声明（仅 ModelInCatalog 时有意义）
	ModelInCatalog      bool   // 模型目录是否收录该模型（「不支持」判定的前提）
}

// noHealthyHint 本地调度类错误（池中无健康号可用/传输层抖动，无上游原文可透传）
// 的固定 hint。不进 GatewayHint：它没有 ErrKind，是网关自己的调度事实。
const noHealthyHint = "池中没有可用的健康账号；请查看 /status 或稍后重试"

// NoHealthyAccountHint 本地调度错误的 gateway_hint（与 no_healthy_account code 配套）。
func NoHealthyAccountHint() string { return noHealthyHint }

// upstreamTimeoutHint 上游超时（轮转已止损）的固定 hint。
//
// 必须与 noHealthyHint 区分：超时是**上游侧**停摆，网关一个账号都没罚
// （isUpstreamTimeout 的止损分支不冷却、不熔断、不 NoteError），此时提示
// 「池中没有可用的健康账号」会把排查引向账号池 —— 方向是错的，运维会去查余额和
// 熔断，而真正该看的是上游可用性与 header_timeout 配置。
const upstreamTimeoutHint = "上游响应超时，网关已停止轮转（换号会撞上同一个慢上游）；" +
	"账号未被罚分，请稍后重试或查看 /status"

// UpstreamTimeoutHint 上游超时的 gateway_hint（与 upstream_timeout code 配套）。
func UpstreamTimeoutHint() string { return upstreamTimeoutHint }

// ModelBlockedHint 模型级阻塞（每个账号都对该模型处于 11102/6004 冷却）的
// hint 前缀；具体账号数与最早解封时间由 handler 拼接。
//
// 必须与 noHealthyHint 区分：选号返回 nil 有两种成因——池子真没号（该等），
// 与号都在但每个号都对这个模型关闭（该换模型）。此前两者都报「没有可用账号」，
// 把「模型不可用」读成了「账号全挂了」，排查方向相反。上游原文不进这里
// （message 已透传原文，见 gateway_hint 纪律），这里只给本地调度事实。
const modelBlockedHintPrefix = "该模型在所有账号上均处于冷却（模型级不可用，换账号无用）；"

// ModelBlockedHint 模型级阻塞的 hint 前缀（与 model_unavailable code 配套）。
func ModelBlockedHint() string { return modelBlockedHintPrefix }

// FrameHintFunc 返回 SSE error 帧的 gateway_hint 判定函数（Stream 的可选参数）。
// ctxFn 惰性求值：仅在实际撞到 error 帧才调用（正常流零开销，模型目录查询
// 不会为每个成功请求触发）。
func FrameHintFunc(ctxFn func() HintContext) func(string) string {
	return func(payload string) string {
		if payload == "" || payload == "[DONE]" {
			return ""
		}
		return GatewayHint(FrameKind(payload), payload, ctxFn())
	}
}

// FrameKind 从 SSE error 帧 payload 判定 ErrKind：6004 模型级限流的流式形态
// （IsModelRateLimit 对帧 JSON 直接命中）优先；其余取帧内 error.message 走
// Classify（请求级 400 口径）。判不出 → ErrNone（无 hint）。
func FrameKind(payload string) ErrKind {
	if IsModelRateLimit(payload) {
		return ErrSoftRate
	}
	var f struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(payload), &f) != nil || f.Error.Message == "" {
		return ErrNone
	}
	return Classify(http.StatusBadRequest, f.Error.Message)
}

// isModelParamInvalid 上游 11133 body 判定（code 11133 / extError.code=
// model_param_invalid / msg 文案家族）。子串口径：hint 是补充说明非权威分类，
// 宁宽勿漏。
func isModelParamInvalid(body string) bool {
	lower := strings.ToLower(body)
	return codeMarker(lower, "11133") ||
		strings.Contains(lower, "model_param_invalid") ||
		strings.Contains(lower, "invalid request parameters") ||
		strings.Contains(lower, "request parameters do not meet the current model requirements")
}

// isInvalidImageData 上游 11135 body 判定（code 11135 / invalid_image_data /
// "replace the image" msg 家族）。
func isInvalidImageData(body string) bool {
	lower := strings.ToLower(body)
	return codeMarker(lower, "11135") ||
		strings.Contains(lower, "invalid_image_data") ||
		strings.Contains(lower, "replace the image")
}

// codeMarker JSON code 字段命中（`"code":N` / `"code": N` / `"code":"N"` 形态，
// 与 IsModelBlocked 的 code 判定同容差口径）。lower 须为小写 body。
func codeMarker(lower, code string) bool {
	for _, v := range []string{`"code":` + code, `"code": ` + code, `"code":"` + code + `"`, `"code": "` + code + `"`, `"code":" ` + code + `"`, `"code": '` + code + `'`} {
		if strings.Contains(lower, v) {
			return true
		}
	}
	return false
}
