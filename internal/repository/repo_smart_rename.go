package repository

import (
	"errors"

	"github.com/nowen-video/nowen-video/internal/model"
	"gorm.io/gorm"
)

// RenameRepo SmartRename 子系统数据访问层。
//
// 它独立于 FileOpLogRepo（后者是 FileManagerService 的轻量审计日志），
// 用于持久化"扫描 -> 规划 -> 执行 -> 回滚"完整生命周期。
type RenameRepo struct {
	db *gorm.DB
}

// NewRenameRepo 构造 SmartRename 数据访问层
func NewRenameRepo(db *gorm.DB) *RenameRepo {
	return &RenameRepo{db: db}
}

// ===== Plan =====

// CreatePlan 持久化一个新的规划任务
func (r *RenameRepo) CreatePlan(plan *model.RenamePlan) error {
	return r.db.Create(plan).Error
}

// UpdatePlan 更新规划任务（全量）
func (r *RenameRepo) UpdatePlan(plan *model.RenamePlan) error {
	return r.db.Save(plan).Error
}

// UpdatePlanFields 部分字段更新
func (r *RenameRepo) UpdatePlanFields(planID string, fields map[string]interface{}) error {
	return r.db.Model(&model.RenamePlan{}).Where("id = ?", planID).Updates(fields).Error
}

// GetPlan 仅取规划主表
func (r *RenameRepo) GetPlan(planID string) (*model.RenamePlan, error) {
	var p model.RenamePlan
	if err := r.db.First(&p, "id = ?", planID).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// GetPlanWithItems 取规划任务并预加载条目
func (r *RenameRepo) GetPlanWithItems(planID string) (*model.RenamePlan, error) {
	var p model.RenamePlan
	if err := r.db.Preload("Items").First(&p, "id = ?", planID).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// ListPlans 分页列出规划
func (r *RenameRepo) ListPlans(page, size int) ([]model.RenamePlan, int64, error) {
	return r.ListPlansFiltered("", page, size)
}

// ListPlansFiltered 支持按 LibraryID 过滤的分页列表。LibraryID 为空时等同于全量。
func (r *RenameRepo) ListPlansFiltered(libraryID string, page, size int) ([]model.RenamePlan, int64, error) {
	if page < 1 {
		page = 1
	}
	if size <= 0 || size > 200 {
		size = 20
	}
	var (
		plans []model.RenamePlan
		total int64
	)
	q := r.db.Model(&model.RenamePlan{})
	if libraryID != "" {
		q = q.Where("library_id = ?", libraryID)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := q.Order("created_at DESC").
		Offset((page - 1) * size).Limit(size).Find(&plans).Error; err != nil {
		return nil, 0, err
	}
	return plans, total, nil
}

// DeletePlan 软删除规划及其条目；journal 不随之软删（作为审计保留），
// 但会在 journal 表上打上 `plan_id` 干净权补充：调用方需意识到“只能查 journal，不会看到 plan/items”。
func (r *RenameRepo) DeletePlan(planID string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("plan_id = ?", planID).Delete(&model.RenamePlanItem{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&model.RenamePlan{}, "id = ?", planID).Error; err != nil {
			return err
		}
		return nil
	})
}

// PurgePlan 彻底物理删除：送同路 Plan/Items/Journals 的 Unscoped 清理，避免“软删 plan 后遬下 journal 悬挂”。
// 仅供明确需要广义清理的场景调用（如后续实现“一键清空超过 N 天的规划”需求）。
func (r *RenameRepo) PurgePlan(planID string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Where("plan_id = ?", planID).Delete(&model.RenameJournal{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("plan_id = ?", planID).Delete(&model.RenamePlanItem{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Delete(&model.RenamePlan{}, "id = ?", planID).Error; err != nil {
			return err
		}
		return nil
	})
}

// ===== Items =====

// CreateItems 批量插入条目（按 chunk 切分，规避 SQLite 默认参数上限）
func (r *RenameRepo) CreateItems(items []model.RenamePlanItem) error {
	if len(items) == 0 {
		return nil
	}
	return r.db.CreateInBatches(items, 100).Error
}

// UpdateItem 全量更新单条
func (r *RenameRepo) UpdateItem(item *model.RenamePlanItem) error {
	return r.db.Save(item).Error
}

// UpdateItemFields 部分字段更新
func (r *RenameRepo) UpdateItemFields(itemID string, fields map[string]interface{}) error {
	return r.db.Model(&model.RenamePlanItem{}).Where("id = ?", itemID).Updates(fields).Error
}

// GetItem 取单条
func (r *RenameRepo) GetItem(itemID string) (*model.RenamePlanItem, error) {
	var it model.RenamePlanItem
	if err := r.db.First(&it, "id = ?", itemID).Error; err != nil {
		return nil, err
	}
	return &it, nil
}

// ListItemsByPlan 取规划下所有条目
func (r *RenameRepo) ListItemsByPlan(planID string) ([]model.RenamePlanItem, error) {
	var items []model.RenamePlanItem
	err := r.db.Where("plan_id = ?", planID).Order("created_at ASC").Find(&items).Error
	return items, err
}

// ListItemsByIDs 按 ID 集合取条目
func (r *RenameRepo) ListItemsByIDs(ids []string) ([]model.RenamePlanItem, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var items []model.RenamePlanItem
	err := r.db.Where("id IN ?", ids).Find(&items).Error
	return items, err
}

// ===== Journal =====

// AppendJournal 追加一条物理操作日志
func (r *RenameRepo) AppendJournal(j *model.RenameJournal) error {
	return r.db.Create(j).Error
}

// ListJournalByPlan 列出某规划任务下所有 journal（用于回滚）
func (r *RenameRepo) ListJournalByPlan(planID string) ([]model.RenameJournal, error) {
	var js []model.RenameJournal
	err := r.db.Where("plan_id = ?", planID).Order("id ASC").Find(&js).Error
	return js, err
}

// ListJournalByIDs 按主键集合查询 journal 明细，按 ID 升序返回。
// 用于“本条目失败 -> 只回滚本条目已成功部分”场景，避免跨条目误伤。
func (r *RenameRepo) ListJournalByIDs(ids []uint64) ([]model.RenameJournal, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var js []model.RenameJournal
	err := r.db.Where("id IN ?", ids).Order("id ASC").Find(&js).Error
	return js, err
}

// MarkJournalReverted 标记某条 journal 已回滚
func (r *RenameRepo) MarkJournalReverted(id uint64, errMsg string) error {
	fields := map[string]interface{}{"reverted": true}
	if errMsg != "" {
		fields["error"] = errMsg
	}
	return r.db.Model(&model.RenameJournal{}).Where("id = ?", id).Updates(fields).Error
}

// ===== 辅助 =====

// IsNotFound 判断是否 gorm not found
func IsNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}
