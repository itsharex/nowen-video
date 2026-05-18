// Command dedupe-movies 同片多版本折叠回填工具
//
// 用途：把数据库里"标题相同/TMDb ID 相同的多版本电影"标记为副本，
// 让前端列表只显示主版本一张卡，避免同一部电影占据多张卡片。
//
// 用法：
//
//	# 1. 预览（不写库）
//	go run ./cmd/dedupe-movies -dry-run
//
//	# 2. 仅处理指定媒体库
//	go run ./cmd/dedupe-movies -library "test" -dry-run
//
//	# 3. 实际写库
//	go run ./cmd/dedupe-movies -apply
//
// 安全性：
//   - 仅修改 duplicate_of / duplicate_group 两个标记字段，不删除任何 Media 记录
//   - 物理文件完全不动
//   - 主版本选取规则：4K > 2K > 1080p > 720p > 480p，同分辨率比文件大小，再比是否有海报，再比是否有 TMDb ID
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/nowen-video/nowen-video/internal/config"
	"github.com/nowen-video/nowen-video/internal/model"
)

var resolutionPriority = map[string]int{
	"4K": 5, "2K": 4, "1080p": 3, "720p": 2, "480p": 1,
}

func main() {
	var (
		dryRun     bool
		apply      bool
		libraryArg string
	)
	flag.BoolVar(&dryRun, "dry-run", true, "仅预览，不写库（默认）")
	flag.BoolVar(&apply, "apply", false, "实际写入数据库（覆盖 -dry-run）")
	flag.StringVar(&libraryArg, "library", "", "仅处理指定媒体库（按 ID 或名字模糊匹配）")
	flag.Parse()

	if apply {
		dryRun = false
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败：%v\n", err)
		os.Exit(1)
	}

	db, err := gorm.Open(sqlite.Open(cfg.GetDBDSN()), &gorm.Config{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开数据库失败：%v\n", err)
		os.Exit(1)
	}

	// 1. 拉取目标范围内的所有电影 Media
	var movies []model.Media
	q := db.Where("media_type = ? AND deleted_at IS NULL", "movie")
	if libraryArg != "" {
		var libs []model.Library
		if err := db.Where("id = ? OR name LIKE ?", libraryArg, "%"+libraryArg+"%").Find(&libs).Error; err != nil || len(libs) == 0 {
			fmt.Fprintf(os.Stderr, "找不到匹配的媒体库：%s\n", libraryArg)
			os.Exit(1)
		}
		var ids []string
		for _, l := range libs {
			ids = append(ids, l.ID)
		}
		q = q.Where("library_id IN ?", ids)
		fmt.Printf("📚 限定媒体库：%d 个（ID: %v）\n", len(libs), ids)
	}
	if err := q.Find(&movies).Error; err != nil {
		fmt.Fprintf(os.Stderr, "查询电影列表失败：%v\n", err)
		os.Exit(1)
	}
	fmt.Printf("🎬 共扫描到 %d 部电影记录（含副本）\n", len(movies))

	// 2. 分组：优先按 TMDb ID，回退到 标题+年份
	type groupKey struct {
		kind  string // tmdb / titleYear
		value string
	}
	groups := map[groupKey][]model.Media{}
	for _, m := range movies {
		var k groupKey
		if m.TMDbID > 0 {
			k = groupKey{kind: "tmdb", value: fmt.Sprintf("%d", m.TMDbID)}
		} else {
			normTitle := strings.ToLower(strings.TrimSpace(m.Title))
			if normTitle == "" {
				continue
			}
			k = groupKey{kind: "titleYear", value: fmt.Sprintf("%s|%d", normTitle, m.Year)}
		}
		groups[k] = append(groups[k], m)
	}

	// 3. 对每组挑主版本，其余打副本标记
	type pendingMark struct {
		ID           string
		DuplicateOf  string // 空表示主版本
		DuplicateGrp string
		Title        string
		Resolution   string
		FileSize     int64
		IsPrimary    bool
		LibraryID    string
	}
	var pending []pendingMark
	groupCount, dupCount := 0, 0

	// 稳定排序：先按 key 排，便于输出可读
	var keys []groupKey
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].kind+keys[i].value < keys[j].kind+keys[j].value })

	for _, k := range keys {
		list := groups[k]
		if len(list) < 2 {
			continue
		}
		groupCount++

		// 选主版本
		bestIdx := 0
		bestScore := -1
		for i, m := range list {
			score := resolutionPriority[m.Resolution] * 1_000_000_000
			score += int(m.FileSize / (1024 * 1024)) // MB
			if m.PosterPath != "" {
				score += 100
			}
			if m.TMDbID > 0 {
				score += 200
			}
			if score > bestScore {
				bestScore = score
				bestIdx = i
			}
		}
		primaryID := list[bestIdx].ID
		groupKeyStr := fmt.Sprintf("%s:%s", k.kind, k.value)

		fmt.Printf("\n━━ [%s] 共 %d 个版本 ━━\n", groupKeyStr, len(list))
		for i, m := range list {
			marker := "  "
			dupOf := primaryID
			if i == bestIdx {
				marker = "★ "
				dupOf = ""
			} else {
				dupCount++
			}
			fmt.Printf("%s %-10s %-10d MB  %-40s  %s\n",
				marker, m.Resolution, m.FileSize/(1024*1024), truncate(m.Title, 40), m.FilePath)
			pending = append(pending, pendingMark{
				ID:           m.ID,
				DuplicateOf:  dupOf,
				DuplicateGrp: groupKeyStr,
				Title:        m.Title,
				Resolution:   m.Resolution,
				FileSize:     m.FileSize,
				IsPrimary:    i == bestIdx,
				LibraryID:    m.LibraryID,
			})
		}
	}

	fmt.Printf("\n📊 汇总：\n")
	fmt.Printf("   - 重复组数: %d\n", groupCount)
	fmt.Printf("   - 将被标记为副本的记录: %d\n", dupCount)
	fmt.Printf("   - 主版本: %d\n", groupCount)

	if dryRun {
		fmt.Println("\nℹ️  当前为 dry-run 模式，未写库。如需实际生效，请加 -apply。")
		return
	}

	if dupCount == 0 {
		fmt.Println("\n✅ 无副本需要标记，数据库未变更。")
		return
	}

	// 4. 实际写库（事务）
	err = db.Transaction(func(tx *gorm.DB) error {
		updated := 0
		for _, p := range pending {
			if err := tx.Model(&model.Media{}).Where("id = ?", p.ID).Updates(map[string]interface{}{
				"duplicate_of":    p.DuplicateOf,
				"duplicate_group": p.DuplicateGrp,
			}).Error; err != nil {
				return fmt.Errorf("更新 media %s 失败: %w", p.ID, err)
			}
			updated++
		}
		fmt.Printf("\n✅ 已写入 %d 条 duplicate 标记（主版本 %d，副本 %d）\n",
			updated, groupCount, dupCount)
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ 事务失败：%v\n", err)
		os.Exit(1)
	}

	fmt.Println("🎉 完成！前端刷新即可看到合并后的电影列表。")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
