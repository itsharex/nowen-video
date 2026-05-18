package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/nowen-video/nowen-video/internal/service"
	"go.uber.org/zap"
)

// SmartRenameHandler 智能扫描重命名 HTTP 入口
type SmartRenameHandler struct {
	svc    *service.SmartRenameService
	logger *zap.SugaredLogger
}

// NewSmartRenameHandler 构造
func NewSmartRenameHandler(svc *service.SmartRenameService, logger *zap.SugaredLogger) *SmartRenameHandler {
	return &SmartRenameHandler{svc: svc, logger: logger}
}

// ===== 请求体 =====

type scanRequest struct {
	RootPath              string   `json:"root_path" binding:"required"`
	LibraryID             string   `json:"library_id"`
	NamingStyle           string   `json:"naming_style"`
	Template              string   `json:"template"`
	EnableAIFallback      *bool    `json:"enable_ai_fallback"`
	AIConfidenceThreshold *float64 `json:"ai_confidence_threshold"`
	SafeRoots             []string `json:"safe_roots"`
}

type executeRequest struct {
	PlanID       string   `json:"plan_id" binding:"required"`
	Confirm      bool     `json:"confirm"`
	ItemIDs      []string `json:"item_ids"`
	IgnoreSafety bool     `json:"ignore_safety"`
}

type updateItemRequest struct {
	OverrideName string `json:"override_name"`
	Excluded     *bool  `json:"excluded"`
}

// ===== 路由实现 =====

// Scan POST /api/admin/smart-rename/scan
func (h *SmartRenameHandler) Scan(c *gin.Context) {
	var req scanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	createdBy, _ := c.Get("userID")
	uid, _ := createdBy.(string)

	plan, err := h.svc.Scan(service.ScanInput{
		RootPath:              req.RootPath,
		LibraryID:             req.LibraryID,
		NamingStyle:           req.NamingStyle,
		Template:              req.Template,
		EnableAIFallback:      req.EnableAIFallback,
		AIConfidenceThreshold: req.AIConfidenceThreshold,
		SafeRoots:             req.SafeRoots,
		CreatedBy:             uid,
	})
	if err != nil {
		h.logger.Errorf("smart-rename scan 失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": plan})
}

// Execute POST /api/admin/smart-rename/execute
//
// 注意：confirm=false 时仅 dry-run（不动盘），confirm=true 才真正落盘。
func (h *SmartRenameHandler) Execute(c *gin.Context) {
	var req executeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	plan, err := h.svc.Execute(service.ExecuteInput{
		PlanID:       req.PlanID,
		Confirm:      req.Confirm,
		ItemIDs:      req.ItemIDs,
		IgnoreSafety: req.IgnoreSafety,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": plan, "dry_run": !req.Confirm})
}

// Rollback POST /api/admin/smart-rename/rollback/:planId
func (h *SmartRenameHandler) Rollback(c *gin.Context) {
	planID := c.Param("planId")
	plan, err := h.svc.Rollback(planID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": plan})
}

// Cancel POST /api/admin/smart-rename/cancel/:planId
func (h *SmartRenameHandler) Cancel(c *gin.Context) {
	planID := c.Param("planId")
	if err := h.svc.Cancel(planID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// ListPlans GET /api/admin/smart-rename/plans
func (h *SmartRenameHandler) ListPlans(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	libraryID := c.Query("library_id")
	plans, total, err := h.svc.ListPlans(page, size, libraryID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"items": plans,
		"total": total,
		"page":  page,
		"size":  size,
	}})
}

// GetPlan GET /api/admin/smart-rename/plans/:planId
func (h *SmartRenameHandler) GetPlan(c *gin.Context) {
	planID := c.Param("planId")
	plan, err := h.svc.GetPlan(planID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": plan})
}

// DeletePlan DELETE /api/admin/smart-rename/plans/:planId
func (h *SmartRenameHandler) DeletePlan(c *gin.Context) {
	planID := c.Param("planId")
	if err := h.svc.DeletePlan(planID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// UpdateItem PUT /api/admin/smart-rename/items/:itemId
//
// 允许用户在 draft 阶段编辑某条目的 override_name / excluded。
func (h *SmartRenameHandler) UpdateItem(c *gin.Context) {
	itemID := c.Param("itemId")
	var req updateItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	it, err := h.svc.UpdateItemOverride(itemID, req.OverrideName, req.Excluded)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": it})
}
