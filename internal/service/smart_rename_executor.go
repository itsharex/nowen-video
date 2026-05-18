package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/nowen-video/nowen-video/internal/model"
	"github.com/nowen-video/nowen-video/internal/repository"
	"go.uber.org/zap"
)

// renameExecutor 真正动盘的执行器；带 journal 记录与回滚能力。
//
// 设计：
//   - 每个条目（主视频 + 关联资源）作为一个原子操作组
//   - 每次 os.Rename / os.MkdirAll 都写 journal 一行
//   - 如组内中途失败，立即按 journal 倒序回滚已成功的操作
type renameExecutor struct {
	repo   *repository.RenameRepo
	logger *zap.SugaredLogger
}

func newRenameExecutor(repo *repository.RenameRepo, logger *zap.SugaredLogger) *renameExecutor {
	return &renameExecutor{repo: repo, logger: logger}
}

// executeItem 落盘一个条目（主视频 + 关联资源）
func (e *renameExecutor) executeItem(planID string, item *model.RenamePlanItem, related []SmartRenameRelatedFile) error {
	if item.SourcePath == "" || item.TargetPath == "" {
		return errors.New("源/目标路径为空")
	}
	if item.SourcePath == item.TargetPath {
		return nil
	}

	// 记录该条目本次写过的 journal，便于失败回滚
	var localJournalIDs []uint64

	// 1) 主视频：先确保目标目录
	if err := e.ensureDir(planID, item.ID, filepath.Dir(item.TargetPath), &localJournalIDs); err != nil {
		return err
	}
	// 2) 主视频 rename
	if err := e.renameWithJournal(planID, item.ID, item.SourcePath, item.TargetPath, &localJournalIDs); err != nil {
		// 已成功的操作回滚（理论上仅 mkdir）
		e.rollbackLocal(localJournalIDs)
		return err
	}

	// 3) 关联资源逐个
	for _, rel := range related {
		// 跳过自身（理论上不会出现）
		if rel.Source == item.SourcePath {
			continue
		}
		if err := e.ensureDir(planID, item.ID, filepath.Dir(rel.Target), &localJournalIDs); err != nil {
			e.rollbackLocal(localJournalIDs)
			return fmt.Errorf("关联资源目录失败 %s: %w", rel.Source, err)
		}
		if _, err := os.Stat(rel.Source); errors.Is(err, os.ErrNotExist) {
			// 源不存在则忽略（用户可能已自行清理）
			continue
		}
		if err := e.renameWithJournal(planID, item.ID, rel.Source, rel.Target, &localJournalIDs); err != nil {
			e.rollbackLocal(localJournalIDs)
			return fmt.Errorf("关联资源迁移失败 %s -> %s: %w", rel.Source, rel.Target, err)
		}
	}
	return nil
}

// ensureDir 若目标目录不存在则创建（并记录 journal）
func (e *renameExecutor) ensureDir(planID, itemID, dir string, journalIDs *[]uint64) error {
	if dir == "" || dir == "." {
		return nil
	}
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		// 记录失败 journal（便于审计）
		j := &model.RenameJournal{
			PlanID:  planID,
			ItemID:  itemID,
			Op:      model.RenameJournalOpMkdir,
			ToPath:  dir,
			Success: false,
			Error:   err.Error(),
		}
		_ = e.repo.AppendJournal(j)
		return err
	}
	j := &model.RenameJournal{
		PlanID:  planID,
		ItemID:  itemID,
		Op:      model.RenameJournalOpMkdir,
		ToPath:  dir,
		Success: true,
	}
	if err := e.repo.AppendJournal(j); err == nil {
		*journalIDs = append(*journalIDs, j.ID)
	}
	return nil
}

// renameWithJournal 重命名并记录 journal
func (e *renameExecutor) renameWithJournal(planID, itemID, src, tgt string, journalIDs *[]uint64) error {
	if src == tgt {
		return nil
	}
	// 防御：Windows 下 os.Rename 当 tgt 已存在时会失败（POSIX 会原子覆盖），
	// 行为差异可能让用户在不同平台看到不同结果，统一为「目标已存在 -> 拒绝执行」，
	// 由上层 safety/checkSafety 在规划阶段保证不会出现。这里是最后一道防线。
	//
	// 注意：通过 filepath.Clean + 平台大小写策略判定是否「同一路径不同写法」，
	// 同一路径直接 return nil；不同路径但目标已存在则报错。
	if pathEqual(src, tgt) {
		return nil
	}
	if _, statErr := os.Stat(tgt); statErr == nil {
		err := fmt.Errorf("rename target already exists: %s", tgt)
		_ = e.repo.AppendJournal(&model.RenameJournal{
			PlanID:   planID,
			ItemID:   itemID,
			Op:       model.RenameJournalOpMove,
			FromPath: src,
			ToPath:   tgt,
			Success:  false,
			Error:    err.Error(),
		})
		return err
	}
	// 优先 os.Rename；若失败（跨卷）回退到 copy+delete
	err := os.Rename(src, tgt)
	if err != nil && isCrossDeviceError(err) {
		err = copyThenDelete(src, tgt)
	}
	if err != nil {
		j := &model.RenameJournal{
			PlanID:   planID,
			ItemID:   itemID,
			Op:       model.RenameJournalOpMove,
			FromPath: src,
			ToPath:   tgt,
			Success:  false,
			Error:    err.Error(),
		}
		_ = e.repo.AppendJournal(j)
		return err
	}
	j := &model.RenameJournal{
		PlanID:   planID,
		ItemID:   itemID,
		Op:       model.RenameJournalOpMove,
		FromPath: src,
		ToPath:   tgt,
		Success:  true,
	}
	if err := e.repo.AppendJournal(j); err == nil {
		*journalIDs = append(*journalIDs, j.ID)
	}
	return nil
}

// rollbackLocal 回滚本条目已成功的 journal（含真正的物理逆操作）。
//
// 之前实现仅做 DB 标记，导致条目内主视频已 rename 但关联文件失败时，磁盘留下半成品状态。
// 现按 ID 集合从 DB 取回明细并执行：
//   - move：ToPath -> FromPath（同卷 Rename，跨卷 copyThenDelete）
//   - mkdir：尝试删除空目录（非空忽略）
func (e *renameExecutor) rollbackLocal(ids []uint64) {
	if len(ids) == 0 {
		return
	}
	journals, err := e.repo.ListJournalByIDs(ids)
	if err != nil {
		e.logger.Warnf("[SmartRename] 本条回滚查询 journal 失败 ids=%v: %v", ids, err)
		// 退化为仅做 DB 标记，避免遗留数据混乱
		for _, id := range ids {
			_ = e.repo.MarkJournalReverted(id, "local rollback after item failure (no journal record)")
		}
		return
	}
	// 倒序回滚
	for i := len(journals) - 1; i >= 0; i-- {
		j := journals[i]
		if !j.Success || j.Reverted {
			continue
		}
		var revErr error
		switch j.Op {
		case model.RenameJournalOpMove:
			if _, statErr := os.Stat(j.ToPath); statErr == nil {
				revErr = os.Rename(j.ToPath, j.FromPath)
				if revErr != nil && isCrossDeviceError(revErr) {
					revErr = copyThenDelete(j.ToPath, j.FromPath)
				}
			}
		case model.RenameJournalOpMkdir:
			_ = os.Remove(j.ToPath)
		}
		if revErr != nil {
			e.logger.Warnf("[SmartRename] 本条回滚失败 journal=%d: %v", j.ID, revErr)
		}
		_ = e.repo.MarkJournalReverted(j.ID, errMsg(revErr))
	}
}

// rollback 按 plan 全量逆序回滚（公开给 SmartRenameService.Rollback 调用）
func (e *renameExecutor) rollback(journals []model.RenameJournal) error {
	if len(journals) == 0 {
		return nil
	}
	var firstErr error
	// 倒序回滚
	for i := len(journals) - 1; i >= 0; i-- {
		j := journals[i]
		if !j.Success || j.Reverted {
			continue
		}
		var err error
		switch j.Op {
		case model.RenameJournalOpMove:
			// 反向移动 ToPath -> FromPath
			if _, statErr := os.Stat(j.ToPath); statErr == nil {
				err = os.Rename(j.ToPath, j.FromPath)
				if err != nil && isCrossDeviceError(err) {
					err = copyThenDelete(j.ToPath, j.FromPath)
				}
			}
		case model.RenameJournalOpMkdir:
			// 仅尝试删除空目录（非空忽略）
			_ = os.Remove(j.ToPath)
		}
		if err != nil {
			e.logger.Warnf("[SmartRename] 回滚失败 journal=%d: %v", j.ID, err)
			if firstErr == nil {
				firstErr = err
			}
		}
		_ = e.repo.MarkJournalReverted(j.ID, errMsg(err))
	}
	return firstErr
}

func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ============================== 跨平台辅助 ===================================

// isCrossDeviceError 是否为跨卷错误（EXDEV）
func isCrossDeviceError(err error) bool {
	if err == nil {
		return false
	}
	// POSIX: errno EXDEV
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		if errno, ok := linkErr.Err.(syscall.Errno); ok {
			// Windows: ERROR_NOT_SAME_DEVICE (0x11=17)；POSIX EXDEV=18
			if int(errno) == 17 || int(errno) == 18 {
				return true
			}
		}
	}
	// 字符串兜底（不同平台错误信息不同）
	low := strings.ToLower(err.Error())
	if strings.Contains(low, "cross-device") || strings.Contains(low, "different drive") ||
		strings.Contains(low, "different device") || strings.Contains(low, "not the same device") {
		return true
	}
	return false
}

// copyThenDelete 跨卷的退化方案：先 copy 再删原文件
func copyThenDelete(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := copyAll(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	if err := os.Remove(src); err != nil {
		return err
	}
	return nil
}

// copyAll 复制内容；直接使用 io.Copy 以正确处理 io.EOF（标准库行为正确）。
//
// 之前的手撸实现用字符串后缀 "EOF" 判断，存在两类风险：
//  1. 任何错误信息以 "EOF" 结尾都会被误判为 EOF，提前 return nil 导致拷贝不完整；
//  2. 与 errors.Is(err, io.EOF) 的标准语义不一致，在不同平台/包装错误下行为不可控。
//
// 跨卷迁移随后会执行 os.Remove(src) —— 如果上面误判为成功，将造成数据丢失。
func copyAll(dst *os.File, src *os.File) (int64, error) {
	return io.Copy(dst, src)
}

// isCrossVolume 仅做静态判定：Windows 看盘符；POSIX 用 syscall.Stat
func isCrossVolume(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	// Windows
	va := filepath.VolumeName(a)
	vb := filepath.VolumeName(b)
	if va != "" || vb != "" {
		return !strings.EqualFold(va, vb)
	}
	// POSIX：比较所在目录的 device id
	return statDevID(filepath.Dir(a)) != statDevID(filepath.Dir(b))
}

// getHardlinkCount 返回硬链接计数（POSIX）；Windows 上仅返回 1
func getHardlinkCount(p string) uint64 {
	return hardlinkCountPlatform(p)
}

// getFileSize 文件大小（错误返回 0）
func getFileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

// hasEnoughSpace 目标目录所在卷可用空间是否足以容纳 size 字节
//
// Windows 上使用 GetDiskFreeSpaceExW（在 _windows 文件中实现）；
// POSIX 使用 Statfs（在 _unix 文件中实现）。
func hasEnoughSpace(dir string, size int64) bool {
	if size <= 0 {
		return true
	}
	free := getFreeBytesPlatform(dir)
	if free == 0 {
		// 探测失败时默认放行（不阻断用户）
		return true
	}
	// 留 5% 余量
	return int64(free) > size+size/20
}
