package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/nowen-video/nowen-video/internal/service"
	"go.uber.org/zap"
)

// AICostHandler 「AI 模型选择 / 成本估算」HTTP 入口
//
// 路由：
//
//	GET  /api/admin/ai/models?provider=openai            列出某 provider 下的可选模型（无 provider 返回全部）
//	GET  /api/admin/ai/cost/estimate?model=...&prompt_tokens=...&completion_tokens=...&provider=...
//	                                                       单次调用估价（USD + CNY）
//	GET  /api/admin/ai/cost/summary                       基于当前 AIService 累计 token 估算总花费
//
// 设计：
//   - 完全不写 DB；模型 catalog 内置，token 取自 AIService 内存计数；
//   - 估价错误（参数非法）返回 400，其余 200 返回 data。
type AICostHandler struct {
	svc    *service.AICostService
	logger *zap.SugaredLogger
}

// NewAICostHandler 构造
func NewAICostHandler(svc *service.AICostService, logger *zap.SugaredLogger) *AICostHandler {
	return &AICostHandler{svc: svc, logger: logger}
}

// ListModels GET /api/admin/ai/models?provider=...
func (h *AICostHandler) ListModels(c *gin.Context) {
	provider := c.Query("provider")
	models := h.svc.ListModels(provider)
	c.JSON(http.StatusOK, gin.H{"data": models, "total": len(models)})
}

// Estimate GET /api/admin/ai/cost/estimate
//
// 必填：model；可选：provider（无则按 model 自动匹配 catalog）、prompt_tokens、completion_tokens。
// 当未传 tokens 时，给出"假设 1K input + 0.5K output"的样本估价，便于前端快速展示。
func (h *AICostHandler) Estimate(c *gin.Context) {
	model := c.Query("model")
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model 必填"})
		return
	}
	provider := c.Query("provider")
	pt, _ := strconv.Atoi(c.DefaultQuery("prompt_tokens", "1000"))
	ct, _ := strconv.Atoi(c.DefaultQuery("completion_tokens", "500"))

	est, err := h.svc.Estimate(provider, model, pt, ct)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": est})
}

// Summary GET /api/admin/ai/cost/summary
func (h *AICostHandler) Summary(c *gin.Context) {
	sum, err := h.svc.Summary()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": sum})
}
