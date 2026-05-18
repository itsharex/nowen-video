// Command clean-dirty 媒体库脏数据扫描与清理工具
//
// 用法：
//
//	# 1. 全量扫描（dry-run，只生成报告，不写库）
//	go run ./cmd/clean-dirty -dry-run
//
//	# 2. 仅扫描指定媒体库（按库 ID 或名字）
//	go run ./cmd/clean-dirty -library "test" -dry-run
//
//	# 3. 仅检查特定问题类别
//	go run ./cmd/clean-dirty -checks=missing,orphan,duplicate -dry-run
//
//	# 4. 真正应用清理（删除/合并）
//	go run ./cmd/clean-dirty -apply
//
//	# 5. 输出 JSON 报告
//	go run ./cmd/clean-dirty -dry-run -json > dirty-report.json
//
// 检测项：
//
//	missing      - Media 文件不存在 / Series 目录不存在
//	orphan       - Episode 的 series_id 指向已删除/不存在的 Series
//	mismatch     - Episode 的 file_path 不在 Series.folder_path 子树下（错误归类）
//	duplicate    - 同一 library 下，归一化后剧集名相同的重复 Series 记录
//	emptyseries  - Series 下无任何 Episode（且 folder_path 也已不在磁盘）
//	stalelibrary - Library.path 指向的目录已不存在
//
// 安全性：
//   - 默认 dry-run，必须显式 -apply 才会写库
//   - apply 模式下交互式二次确认
//   - 删除采用 GORM 软删除（保留 deleted_at），可以从数据库恢复
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/nowen-video/nowen-video/internal/config"
	"github.com/nowen-video/nowen-video/internal/model"
)

// ==================== 数据结构 ====================

// DirtyKind 脏数据类型
type DirtyKind string

const (
	KindMissing      DirtyKind = "missing"      // 物理文件/目录不存在
	KindOrphan       DirtyKind = "orphan"       // 孤儿 episode
	KindMismatch     DirtyKind = "mismatch"     // file_path 不在 series 子树下
	KindDuplicate    DirtyKind = "duplicate"    // 重复 series
	KindEmptySeries  DirtyKind = "empty_series" // 空壳 series
	KindStaleLibrary DirtyKind = "stale_lib"    // 媒体库根路径已失效
)

// DirtyItem 单条脏数据记录
type DirtyItem struct {
	Kind        DirtyKind `json:"kind"`
	Entity      string    `json:"entity"` // library / series / media
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	LibraryID   string    `json:"library_id,omitempty"`
	LibraryName string    `json:"library_name,omitempty"`
	Path        string    `json:"path,omitempty"`
	Reason      string    `json:"reason"`
	// 修复建议 / 关联数据
	SuggestedAction string   `json:"suggested_action,omitempty"`
	RelatedIDs      []string `json:"related_ids,omitempty"`
}

// CleanReport 总报告
type CleanReport struct {
	GeneratedAt    time.Time           `json:"generated_at"`
	DryRun         bool                `json:"dry_run"`
	Stats          map[DirtyKind]int   `json:"stats"`
	TotalDirty     int                 `json:"total_dirty"`
	Items          []DirtyItem         `json:"items"`
	AppliedActions []string            `json:"applied_actions,omitempty"`
	Counts         map[string]int      `json:"counts"` // 总数：libraries/series/media
	GroupedByKind  map[DirtyKind][]int `json:"-"`      // kind -> indexes
}

// ==================== 归一化（与 fix-tvshow-merge 保持一致） ====================

var (
	idtagPattern = regexp.MustCompile(`(?i)\s*\[(tmdbid|imdbid|tvdbid)-[^\]]+\]\s*`)
	yearPattern  = regexp.MustCompile(`\s*[\(\[]\s*(19|20)\d{2}\s*[\)\]]\s*`)
	seasonStrips = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\s*S\d{1,2}\s*$`),
		regexp.MustCompile(`(?i)\s*Season\s*\d{1,2}\s*$`),
		regexp.MustCompile(`\s*第\s*[一二三四五六七八九十\d]+\s*季\s*$`),
		regexp.MustCompile(`\s*第\s*[一二三四五六七八九十\d]+\s*部\s*$`),
	}
)

// normalizeTitle 把剧集名归一化为可比对的剧集名
func normalizeTitle(name string) string {
	t := name
	t = idtagPattern.ReplaceAllString(t, " ")
	t = yearPattern.ReplaceAllString(t, " ")
	for _, p := range seasonStrips {
		t = p.ReplaceAllString(t, "")
	}
	t = regexp.MustCompile(`\s+`).ReplaceAllString(t, " ")
	t = strings.TrimSpace(t)
	t = strings.Trim(t, " -·・【】()（）[]")
	return strings.ToLower(t)
}

// pathExists 物理路径是否存在（兼容 Windows 反斜杠）
func pathExists(p string) bool {
	if strings.TrimSpace(p) == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// inSubtree 判断 child 是否位于 parent 子树下
func inSubtree(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == "" || child == "" {
		return false
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && rel != ""
}

// ==================== 检测器 ====================

type cleaner struct {
	db            *gorm.DB
	libraryFilter string // 库 ID 或名字
	checks        map[string]bool
	dryRun        bool
	verbose       bool
	report        *CleanReport
}

func newCleaner(db *gorm.DB, libraryFilter string, checks []string, dryRun, verbose bool) *cleaner {
	cm := make(map[string]bool)
	for _, c := range checks {
		cm[strings.TrimSpace(strings.ToLower(c))] = true
	}
	return &cleaner{
		db:            db,
		libraryFilter: strings.TrimSpace(libraryFilter),
		checks:        cm,
		dryRun:        dryRun,
		verbose:       verbose,
		report: &CleanReport{
			GeneratedAt: time.Now(),
			DryRun:      dryRun,
			Stats:       make(map[DirtyKind]int),
			Counts:      make(map[string]int),
		},
	}
}

func (c *cleaner) checkEnabled(name string) bool {
	if len(c.checks) == 0 {
		return true
	}
	return c.checks[strings.ToLower(name)]
}

// addItem 追加脏数据条目
func (c *cleaner) addItem(item DirtyItem) {
	c.report.Items = append(c.report.Items, item)
	c.report.Stats[item.Kind]++
	c.report.TotalDirty++
}

// loadLibraries 加载需要扫描的库列表
func (c *cleaner) loadLibraries() ([]model.Library, error) {
	q := c.db.Model(&model.Library{})
	if c.libraryFilter != "" {
		q = q.Where("id = ? OR name = ?", c.libraryFilter, c.libraryFilter)
	}
	var libs []model.Library
	if err := q.Find(&libs).Error; err != nil {
		return nil, err
	}
	return libs, nil
}

// run 主入口
func (c *cleaner) run() error {
	libs, err := c.loadLibraries()
	if err != nil {
		return fmt.Errorf("加载媒体库失败: %w", err)
	}
	c.report.Counts["libraries"] = len(libs)
	libIndex := make(map[string]model.Library)
	for _, l := range libs {
		libIndex[l.ID] = l
	}

	// 1. 检查 Library.path 是否存在
	if c.checkEnabled("stalelibrary") {
		c.checkStaleLibraries(libs)
	}

	// 2. 加载相关 Series / Media
	libIDs := make([]string, 0, len(libs))
	for _, l := range libs {
		libIDs = append(libIDs, l.ID)
	}

	var seriesList []model.Series
	if len(libIDs) > 0 {
		if err := c.db.Where("library_id IN ?", libIDs).Find(&seriesList).Error; err != nil {
			return fmt.Errorf("加载 Series 失败: %w", err)
		}
	}
	c.report.Counts["series"] = len(seriesList)
	seriesIndex := make(map[string]model.Series)
	for _, s := range seriesList {
		seriesIndex[s.ID] = s
	}

	var mediaList []model.Media
	if len(libIDs) > 0 {
		if err := c.db.Where("library_id IN ?", libIDs).Find(&mediaList).Error; err != nil {
			return fmt.Errorf("加载 Media 失败: %w", err)
		}
	}
	c.report.Counts["media"] = len(mediaList)

	// 3. 检测各类脏数据
	if c.checkEnabled("missing") {
		c.checkMissingSeries(seriesList, libIndex)
		c.checkMissingMedia(mediaList, libIndex)
	}
	if c.checkEnabled("orphan") {
		c.checkOrphanEpisodes(mediaList, seriesIndex, libIndex)
	}
	if c.checkEnabled("mismatch") {
		c.checkMismatchEpisodes(mediaList, seriesIndex, libIndex)
	}
	if c.checkEnabled("duplicate") {
		c.checkDuplicateSeries(seriesList, libIndex)
	}
	if c.checkEnabled("emptyseries") {
		c.checkEmptySeries(seriesList, mediaList, libIndex)
	}

	// 4. 应用清理（仅 apply 模式）
	if !c.dryRun {
		if err := c.applyClean(); err != nil {
			return fmt.Errorf("执行清理失败: %w", err)
		}
	}

	return nil
}

// ==================== 各类检测 ====================

// checkStaleLibraries Library.path 失效
func (c *cleaner) checkStaleLibraries(libs []model.Library) {
	for _, l := range libs {
		for _, p := range l.AllPaths() {
			if !pathExists(p) {
				c.addItem(DirtyItem{
					Kind:            KindStaleLibrary,
					Entity:          "library",
					ID:              l.ID,
					Title:           l.Name,
					LibraryID:       l.ID,
					LibraryName:     l.Name,
					Path:            p,
					Reason:          fmt.Sprintf("库根路径不存在: %s", p),
					SuggestedAction: "manual",
				})
			}
		}
	}
}

// checkMissingSeries Series.folder_path 不存在
// 注意：多季合并/散落剧集会使用 "__multi__:" / "__loose__:" 虚拟路径前缀，磁盘上不存在但属于合法状态，必须跳过
func (c *cleaner) checkMissingSeries(seriesList []model.Series, libIndex map[string]model.Library) {
	for _, s := range seriesList {
		if isVirtualSeriesPath(s.FolderPath) {
			continue // 虚拟路径（多季合并/散落剧集），不应作为 missing 处理
		}
		if pathExists(s.FolderPath) {
			continue
		}
		lib := libIndex[s.LibraryID]
		c.addItem(DirtyItem{
			Kind:            KindMissing,
			Entity:          "series",
			ID:              s.ID,
			Title:           s.Title,
			LibraryID:       s.LibraryID,
			LibraryName:     lib.Name,
			Path:            s.FolderPath,
			Reason:          "剧集目录已不存在",
			SuggestedAction: "delete",
		})
	}
}

// isVirtualSeriesPath 判断 series.folder_path 是否为虚拟标记路径
// 这种路径形如 "D:\\xxx\\__multi__:女神咖啡厅" 或 "D:\\xxx\\__loose__:xxx"，
// 是 scanner.go / series.go 在多季合并/散落剧集场景下生成的占位符，磁盘上肯定不存在但属于合法状态。
func isVirtualSeriesPath(p string) bool {
	if p == "" {
		return false
	}
	return strings.Contains(p, "__multi__:") || strings.Contains(p, "__loose__:")
}

// checkMissingMedia Media.file_path 不存在（且非 STRM 远程流）
func (c *cleaner) checkMissingMedia(mediaList []model.Media, libIndex map[string]model.Library) {
	for _, m := range mediaList {
		// STRM 远程流：跳过物理文件检查
		if strings.TrimSpace(m.StreamURL) != "" {
			continue
		}
		if pathExists(m.FilePath) {
			continue
		}
		lib := libIndex[m.LibraryID]
		c.addItem(DirtyItem{
			Kind:            KindMissing,
			Entity:          "media",
			ID:              m.ID,
			Title:           m.Title,
			LibraryID:       m.LibraryID,
			LibraryName:     lib.Name,
			Path:            m.FilePath,
			Reason:          "视频文件已不存在",
			SuggestedAction: "delete",
		})
	}
}

// checkOrphanEpisodes Episode.series_id 指向不存在的 Series
func (c *cleaner) checkOrphanEpisodes(mediaList []model.Media, seriesIndex map[string]model.Series, libIndex map[string]model.Library) {
	for _, m := range mediaList {
		if m.MediaType != "episode" {
			continue
		}
		if strings.TrimSpace(m.SeriesID) == "" {
			// 没有 series_id 的 episode 也是孤儿
			lib := libIndex[m.LibraryID]
			c.addItem(DirtyItem{
				Kind:            KindOrphan,
				Entity:          "media",
				ID:              m.ID,
				Title:           m.Title,
				LibraryID:       m.LibraryID,
				LibraryName:     lib.Name,
				Path:            m.FilePath,
				Reason:          "episode 缺少 series_id",
				SuggestedAction: "delete",
			})
			continue
		}
		if _, ok := seriesIndex[m.SeriesID]; !ok {
			lib := libIndex[m.LibraryID]
			c.addItem(DirtyItem{
				Kind:            KindOrphan,
				Entity:          "media",
				ID:              m.ID,
				Title:           m.Title,
				LibraryID:       m.LibraryID,
				LibraryName:     lib.Name,
				Path:            m.FilePath,
				Reason:          fmt.Sprintf("series_id=%s 已不存在", m.SeriesID),
				SuggestedAction: "delete",
				RelatedIDs:      []string{m.SeriesID},
			})
		}
	}
}

// checkMismatchEpisodes Episode.file_path 不在所属 Series.folder_path 子树下
func (c *cleaner) checkMismatchEpisodes(mediaList []model.Media, seriesIndex map[string]model.Series, libIndex map[string]model.Library) {
	for _, m := range mediaList {
		if m.MediaType != "episode" {
			continue
		}
		if strings.TrimSpace(m.SeriesID) == "" {
			continue // orphan 检测会处理
		}
		s, ok := seriesIndex[m.SeriesID]
		if !ok {
			continue // orphan 检测会处理
		}
		if strings.TrimSpace(s.FolderPath) == "" || strings.TrimSpace(m.FilePath) == "" {
			continue
		}
		// 虚拟路径（多季合并 __multi__: / 散落剧集 __loose__:）下的剧集天然不会在虚拟目录子树下，属合法状态
		if isVirtualSeriesPath(s.FolderPath) {
			continue
		}
		if inSubtree(s.FolderPath, m.FilePath) {
			continue
		}
		// 跨目录：file_path 不在 series 目录下，属于错误归类
		lib := libIndex[m.LibraryID]
		c.addItem(DirtyItem{
			Kind:            KindMismatch,
			Entity:          "media",
			ID:              m.ID,
			Title:           m.Title,
			LibraryID:       m.LibraryID,
			LibraryName:     lib.Name,
			Path:            m.FilePath,
			Reason:          fmt.Sprintf("file_path 不在所属 Series 目录下\n  Series: %s\n  Folder: %s", s.Title, s.FolderPath),
			SuggestedAction: "rebind_or_delete",
			RelatedIDs:      []string{s.ID},
		})
	}
}

// checkDuplicateSeries 同一 library 下归一化后剧集名相同的重复记录
func (c *cleaner) checkDuplicateSeries(seriesList []model.Series, libIndex map[string]model.Library) {
	type key struct {
		libID  string
		normTi string
	}
	groups := make(map[key][]model.Series)
	for _, s := range seriesList {
		k := key{libID: s.LibraryID, normTi: normalizeTitle(s.Title)}
		if k.normTi == "" {
			continue
		}
		groups[k] = append(groups[k], s)
	}
	for k, gs := range groups {
		if len(gs) < 2 {
			continue
		}
		lib := libIndex[k.libID]
		// 选取保留者：优先 TMDbID > 集数最多 > FolderPath 最短
		sort.Slice(gs, func(i, j int) bool {
			a, b := gs[i], gs[j]
			if (a.TMDbID > 0) != (b.TMDbID > 0) {
				return a.TMDbID > 0
			}
			if a.EpisodeCount != b.EpisodeCount {
				return a.EpisodeCount > b.EpisodeCount
			}
			return len(a.FolderPath) < len(b.FolderPath)
		})
		keeper := gs[0]
		ids := make([]string, 0, len(gs))
		titles := make([]string, 0, len(gs))
		for _, s := range gs {
			ids = append(ids, s.ID)
			titles = append(titles, fmt.Sprintf("%q@%s(eps=%d)", s.Title, s.FolderPath, s.EpisodeCount))
		}
		for i, s := range gs {
			if i == 0 {
				continue // keeper 不报为脏数据
			}
			c.addItem(DirtyItem{
				Kind:        KindDuplicate,
				Entity:      "series",
				ID:          s.ID,
				Title:       s.Title,
				LibraryID:   s.LibraryID,
				LibraryName: lib.Name,
				Path:        s.FolderPath,
				Reason: fmt.Sprintf("与 keeper(id=%s, title=%q) 归一化相同 (key=%q)\n  全部候选: %s",
					keeper.ID, keeper.Title, k.normTi, strings.Join(titles, " | ")),
				SuggestedAction: "merge_into:" + keeper.ID,
				RelatedIDs:      ids,
			})
		}
	}
}

// checkEmptySeries Series 下无任何 Episode
func (c *cleaner) checkEmptySeries(seriesList []model.Series, mediaList []model.Media, libIndex map[string]model.Library) {
	hasEp := make(map[string]bool)
	for _, m := range mediaList {
		if m.MediaType == "episode" && strings.TrimSpace(m.SeriesID) != "" {
			hasEp[m.SeriesID] = true
		}
	}
	for _, s := range seriesList {
		if hasEp[s.ID] {
			continue
		}
		lib := libIndex[s.LibraryID]
		// 目录还在的话，可能是新建未扫描，不一定是脏；但既无 episode 又无目录，肯定是脏
		reason := "Series 下无任何 episode"
		action := "delete"
		if pathExists(s.FolderPath) {
			reason += "（但磁盘目录还存在，可能尚未扫描）"
			action = "rescan_or_delete"
		}
		c.addItem(DirtyItem{
			Kind:            KindEmptySeries,
			Entity:          "series",
			ID:              s.ID,
			Title:           s.Title,
			LibraryID:       s.LibraryID,
			LibraryName:     lib.Name,
			Path:            s.FolderPath,
			Reason:          reason,
			SuggestedAction: action,
		})
	}
}

// ==================== 应用清理 ====================

func (c *cleaner) applyClean() error {
	if len(c.report.Items) == 0 {
		fmt.Println("✅ 无需清理：未发现脏数据")
		return nil
	}
	fmt.Println()
	fmt.Println("⚠️  即将执行清理操作（GORM 软删除，可从数据库恢复）：")
	for kind, n := range c.report.Stats {
		fmt.Printf("    - %s: %d 条\n", kind, n)
	}
	fmt.Print("\n输入 YES 以确认执行（大小写不敏感，其他任意输入将取消）: ")
	var confirm string
	fmt.Scanln(&confirm)
	if !strings.EqualFold(strings.TrimSpace(confirm), "YES") {
		fmt.Println("已取消清理。")
		return nil
	}

	tx := c.db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			panic(r)
		}
	}()

	for _, item := range c.report.Items {
		var act string
		var err error
		switch item.Kind {
		case KindMissing, KindOrphan, KindEmptySeries:
			if item.Entity == "media" {
				err = tx.Where("id = ?", item.ID).Delete(&model.Media{}).Error
				act = fmt.Sprintf("DELETE media id=%s title=%q", item.ID, item.Title)
			} else if item.Entity == "series" {
				// 删 Series 同时把它的 episode 也删（避免后续变孤儿）
				err = tx.Where("series_id = ?", item.ID).Delete(&model.Media{}).Error
				if err == nil {
					err = tx.Where("id = ?", item.ID).Delete(&model.Series{}).Error
				}
				act = fmt.Sprintf("DELETE series id=%s title=%q (含其全部 episode)", item.ID, item.Title)
			}
		case KindMismatch:
			// 错误归类的 episode：直接删除（重新扫描会正确归类）
			err = tx.Where("id = ?", item.ID).Delete(&model.Media{}).Error
			act = fmt.Sprintf("DELETE mismatch media id=%s title=%q", item.ID, item.Title)
		case KindDuplicate:
			// 解析 keeper id
			keeperID := strings.TrimPrefix(item.SuggestedAction, "merge_into:")
			if keeperID == "" || keeperID == item.SuggestedAction {
				act = fmt.Sprintf("SKIP duplicate id=%s（无 keeper id）", item.ID)
				break
			}
			// 把重复 series 下的 episode 重定向到 keeper
			err = tx.Model(&model.Media{}).
				Where("series_id = ?", item.ID).
				Update("series_id", keeperID).Error
			if err == nil {
				err = tx.Where("id = ?", item.ID).Delete(&model.Series{}).Error
			}
			act = fmt.Sprintf("MERGE series id=%s into keeper=%s, title=%q", item.ID, keeperID, item.Title)
		case KindStaleLibrary:
			// 仅提示，不自动删库
			act = fmt.Sprintf("WARN stale library id=%s name=%q path=%q（请手动确认后再删）", item.ID, item.Title, item.Path)
		default:
			act = fmt.Sprintf("SKIP unknown kind=%s id=%s", item.Kind, item.ID)
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("应用 %s 失败: %w", act, err)
		}
		c.report.AppliedActions = append(c.report.AppliedActions, act)
		if c.verbose {
			fmt.Println("  ", act)
		}
	}

	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}
	fmt.Printf("\n✅ 已应用 %d 项清理动作\n", len(c.report.AppliedActions))
	return nil
}

// ==================== 报告输出 ====================

func printTextReport(r *CleanReport) {
	fmt.Println("================ 媒体库脏数据扫描报告 ================")
	fmt.Printf("生成时间: %s\n", r.GeneratedAt.Format("2006-01-02 15:04:05"))
	fmt.Printf("模式: %s\n", map[bool]string{true: "DRY-RUN（仅扫描）", false: "APPLY（已执行清理）"}[r.DryRun])
	fmt.Println()
	fmt.Println("数据规模：")
	fmt.Printf("  - 媒体库: %d 个\n", r.Counts["libraries"])
	fmt.Printf("  - 剧集: %d 个\n", r.Counts["series"])
	fmt.Printf("  - 媒体项: %d 个\n", r.Counts["media"])
	fmt.Println()
	fmt.Println("脏数据统计：")
	if r.TotalDirty == 0 {
		fmt.Println("  ✅ 未发现脏数据，库非常干净！")
		return
	}
	// 排序输出统计
	kinds := make([]string, 0, len(r.Stats))
	for k := range r.Stats {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Printf("  - %-12s : %d 条\n", k, r.Stats[DirtyKind(k)])
	}
	fmt.Printf("  合计: %d 条脏数据\n", r.TotalDirty)
	fmt.Println()

	// 分组明细
	groups := make(map[DirtyKind][]DirtyItem)
	for _, it := range r.Items {
		groups[it.Kind] = append(groups[it.Kind], it)
	}
	kindOrder := []DirtyKind{KindStaleLibrary, KindMissing, KindOrphan, KindMismatch, KindDuplicate, KindEmptySeries}
	for _, k := range kindOrder {
		items, ok := groups[k]
		if !ok || len(items) == 0 {
			continue
		}
		fmt.Printf("──── [%s] 共 %d 条 ────\n", k, len(items))
		// 限制每类最多打印 30 条详情（更多请看 JSON 报告）
		max := 30
		for i, it := range items {
			if i >= max {
				fmt.Printf("  ...（其余 %d 条已省略，请使用 -json 查看完整列表）\n", len(items)-max)
				break
			}
			fmt.Printf("  [%s/%s] %s\n", it.Entity, it.ID, it.Title)
			if it.LibraryName != "" {
				fmt.Printf("     库:   %s\n", it.LibraryName)
			}
			if it.Path != "" {
				fmt.Printf("     路径: %s\n", it.Path)
			}
			fmt.Printf("     原因: %s\n", it.Reason)
			if it.SuggestedAction != "" {
				fmt.Printf("     建议: %s\n", it.SuggestedAction)
			}
		}
		fmt.Println()
	}

	if r.DryRun {
		fmt.Println("提示：本次为 dry-run，未修改数据库。如需应用清理，请加 -apply 参数。")
	} else {
		fmt.Printf("已执行清理动作 %d 项。\n", len(r.AppliedActions))
	}
}

// ==================== main ====================

func main() {
	var (
		libraryFilter = flag.String("library", "", "仅扫描指定媒体库（库 ID 或名字），留空扫描全部")
		checksFlag    = flag.String("checks", "", "仅运行指定检测项（逗号分隔），留空运行全部。可选: missing,orphan,mismatch,duplicate,emptyseries,stalelibrary")
		dryRun        = flag.Bool("dry-run", true, "仅扫描生成报告（默认）")
		apply         = flag.Bool("apply", false, "真正应用清理（会与 dry-run 互斥）")
		jsonOut       = flag.Bool("json", false, "输出 JSON 报告（用于程序化处理）")
		verbose       = flag.Bool("v", false, "应用清理时打印每条动作")
		dbPath        = flag.String("db", "", "覆盖数据库路径（默认从 config.Load 读取，未配置则用 ./data/nowen.db）")
	)
	flag.Parse()

	if *apply {
		*dryRun = false
	}

	// 加载配置
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[warn] 加载配置失败: %v；将使用默认数据库路径\n", err)
		cfg = &config.Config{}
		cfg.Database.DBPath = "./data/nowen.db"
	}
	if *dbPath != "" {
		cfg.Database.DBPath = *dbPath
	}

	db, err := gorm.Open(sqlite.Open(cfg.GetDBDSN()), &gorm.Config{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "连接数据库失败: %v\n", err)
		os.Exit(1)
	}
	if sqlDB, errDB := db.DB(); errDB == nil {
		defer func() { _ = sqlDB.Close() }()
	}

	checks := []string{}
	if strings.TrimSpace(*checksFlag) != "" {
		for _, c := range strings.Split(*checksFlag, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				checks = append(checks, c)
			}
		}
	}

	cl := newCleaner(db, *libraryFilter, checks, *dryRun, *verbose)
	if err := cl.run(); err != nil {
		fmt.Fprintf(os.Stderr, "执行失败: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(cl.report)
		return
	}
	printTextReport(cl.report)
}
