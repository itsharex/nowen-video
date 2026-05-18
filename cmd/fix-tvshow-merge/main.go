// Command fix-tvshow-merge 修复历史误分裂的 TV Shows 目录。
//
// 用法：
//
//	go run ./cmd/fix-tvshow-merge -root "D:\\video\\_organized\\TV Shows" -dry-run
//	go run ./cmd/fix-tvshow-merge -root "D:\\video\\_organized\\TV Shows" -apply
//
// 背景：早期的 LazyIngest 在写盘时未剥离 Title 中的"第二季 / Season 2"等尾缀，
// 导致同一部剧产生多个独立目录（女神咖啡厅 / 女神咖啡厅 第一季 / 女神咖啡厅 第二季），
// 同时季号子目录被错误地合并到 Season 01 之下。
//
// 本工具按以下规则归并：
//  1. 把目录名做归一化（剥离 [tmdbid-xxx] / (2018) / 第二季 / Season 2 / S02 等尾缀）
//     得到"剧集名"，把所有归一化后相同的目录合并到目标目录。
//  2. 目标目录优先选用：带 [tmdbid-xxx] 的版本 > 带 (年份) 的版本 > 名字最短的版本。
//  3. 把每个源目录中的 Season XX 子目录（或独立剧集文件）按"目录名中识别到的季号"
//     重新计算正确的季号；若目录名能识别到 N（如"第二季"），则源目录的 Season 01
//     会被搬到目标 Season N 下；目录名识别不到季号时保持原样。
//  4. 同一目标 Season 下若发生命名冲突，跳过冲突文件并打日志（不覆盖）。
//  5. dry-run 仅打印计划，不写盘。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// =====================================================================
// 归一化（与 internal/service/scanner.go 中 normalizeSeriesName 行为一致）
// =====================================================================

var (
	idtagPattern = regexp.MustCompile(`(?i)\s*\[(tmdbid|imdbid|tvdbid)-[^\]]+\]\s*`)
	yearPattern  = regexp.MustCompile(`\s*[\(\[]\s*(19|20)\d{2}\s*[\)\]]\s*`)
	seasonStrips = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\s*S\d{1,2}\s*$`),
		regexp.MustCompile(`(?i)\s*Season\s*\d{1,2}\s*$`),
		regexp.MustCompile(`\s*第\s*[一二三四五六七八九十\d]+\s*季\s*$`),
		regexp.MustCompile(`\s*第\s*[一二三四五六七八九十\d]+\s*部\s*$`),
	}
	cnSeasonNum = map[string]int{
		"一": 1, "二": 2, "三": 3, "四": 4, "五": 5,
		"六": 6, "七": 7, "八": 8, "九": 9, "十": 10,
	}
)

// normalize 把一个剧集目录名归一化为可比对的剧集名
func normalize(dirName string) string {
	t := dirName
	t = idtagPattern.ReplaceAllString(t, " ")
	t = yearPattern.ReplaceAllString(t, " ")
	for _, p := range seasonStrips {
		t = p.ReplaceAllString(t, "")
	}
	t = regexp.MustCompile(`\s+`).ReplaceAllString(t, " ")
	t = strings.TrimSpace(t)
	t = strings.Trim(t, " -·・【】()（）[]")
	return t
}

// extractSeasonFromName 从目录名中尝试提取季号；提取不到返回 0
func extractSeasonFromName(dirName string) int {
	if m := regexp.MustCompile(`(?i)\bS(\d{1,2})\b`).FindStringSubmatch(dirName); len(m) >= 2 {
		if n, _ := strconv.Atoi(m[1]); n > 0 && n <= 50 {
			return n
		}
	}
	if m := regexp.MustCompile(`(?i)\bSeason\s*(\d{1,2})\b`).FindStringSubmatch(dirName); len(m) >= 2 {
		if n, _ := strconv.Atoi(m[1]); n > 0 && n <= 50 {
			return n
		}
	}
	if m := regexp.MustCompile(`第\s*(\d{1,2})\s*季`).FindStringSubmatch(dirName); len(m) >= 2 {
		if n, _ := strconv.Atoi(m[1]); n > 0 && n <= 50 {
			return n
		}
	}
	if m := regexp.MustCompile(`第\s*([一二三四五六七八九十]+)\s*季`).FindStringSubmatch(dirName); len(m) >= 2 {
		if n, ok := cnSeasonNum[m[1]]; ok {
			return n
		}
	}
	return 0
}

// extractSeasonFromSubDir 从 "Season 02" / "S02" 子目录名提取季号
func extractSeasonFromSubDir(subDir string) int {
	if m := regexp.MustCompile(`(?i)^Season\s*(\d{1,2})$`).FindStringSubmatch(subDir); len(m) >= 2 {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	if m := regexp.MustCompile(`(?i)^S(\d{1,2})$`).FindStringSubmatch(subDir); len(m) >= 2 {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return -1 // -1 表示不是季目录
}

// =====================================================================
// 主流程
// =====================================================================

type srcDir struct {
	path      string
	name      string
	dirSeason int  // 从目录名识别到的季号（0 = 未识别）
	hasTMDb   bool // 是否带 [tmdbid-xxx]
	hasYear   bool // 是否带 (年份)
}

type plan struct {
	target  string          // 目标目录绝对路径
	sources []srcDir        // 待合并的源目录（可能含 target 本身）
	moves   []moveOp        // 实际文件搬运计划
	mkdirs  map[string]bool // 需要创建的目录
}

type moveOp struct {
	src  string
	dst  string
	skip string // 非空 = 跳过原因
}

func main() {
	var (
		root      = flag.String("root", "", "TV Shows 根目录，例如 D:\\video\\_organized\\TV Shows")
		dryRun    = flag.Bool("dry-run", true, "仅预览，不写盘（默认）")
		apply     = flag.Bool("apply", false, "实际执行（覆盖 -dry-run）")
		removeOld = flag.Bool("remove-empty", true, "搬运后删除空的源目录")
	)
	flag.Parse()

	if strings.TrimSpace(*root) == "" {
		fmt.Fprintln(os.Stderr, "必须指定 -root（TV Shows 根目录）")
		os.Exit(2)
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "root 不可访问:", err)
		os.Exit(2)
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		fmt.Fprintln(os.Stderr, "root 不是有效目录:", abs)
		os.Exit(2)
	}

	doApply := *apply // 默认 dry-run
	if doApply {
		*dryRun = false
	}

	fmt.Printf("=== fix-tvshow-merge ===\n")
	fmt.Printf("root      : %s\n", abs)
	fmt.Printf("mode      : %s\n", modeStr(doApply))
	fmt.Printf("removeOld : %v\n", *removeOld)
	fmt.Println()

	// === 阶段1：扫描所有一级子目录，按归一化名分组 ===
	entries, err := os.ReadDir(abs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取 root 失败:", err)
		os.Exit(1)
	}

	groups := map[string][]srcDir{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		full := filepath.Join(abs, name)
		key := normalize(name)
		if key == "" {
			continue
		}
		groups[key] = append(groups[key], srcDir{
			path:      full,
			name:      name,
			dirSeason: extractSeasonFromName(name),
			hasTMDb:   regexp.MustCompile(`(?i)\[tmdbid-`).MatchString(name),
			hasYear:   regexp.MustCompile(`[\(\[]\s*(19|20)\d{2}\s*[\)\]]`).MatchString(name),
		})
	}

	// === 阶段2：每组制定 plan ===
	plans := []*plan{}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		group := groups[key]
		if len(group) == 1 && group[0].dirSeason == 0 {
			// 单个目录且没有"第二季"标签 -> 无需动
			continue
		}
		// 选目标目录：优先 hasTMDb，其次 hasYear，再次最短名
		sort.Slice(group, func(i, j int) bool {
			a, b := group[i], group[j]
			if a.hasTMDb != b.hasTMDb {
				return a.hasTMDb
			}
			if a.hasYear != b.hasYear {
				return a.hasYear
			}
			return len(a.name) < len(b.name)
		})
		// 目标目录名：
		//   - 若已经有 hasTMDb 或 hasYear 的版本，直接用其名
		//   - 否则用 key（归一化后的纯名）
		targetName := group[0].name
		if !group[0].hasTMDb && !group[0].hasYear {
			targetName = key
		} else {
			// 同时把目标目录名也"剥掉"季号尾缀，确保稳定
			// 例如 "一拳超人 第二季 (2018) [tmdbid-74956]" → "一拳超人 (2018) [tmdbid-74956]"
			targetName = stripSeasonFromTargetName(group[0].name)
		}
		targetPath := filepath.Join(abs, targetName)

		p := &plan{
			target:  targetPath,
			sources: group,
			mkdirs:  map[string]bool{},
		}
		// 计划文件搬运
		buildMoves(p)
		plans = append(plans, p)
	}

	if len(plans) == 0 {
		fmt.Println("✅ 没有需要修复的目录")
		return
	}

	// === 阶段3：打印 plan ===
	totalMoves := 0
	totalSkips := 0
	for _, p := range plans {
		fmt.Printf("──────────────────────────────────────────────\n")
		fmt.Printf("[合并组] %s\n", filepath.Base(p.target))
		for _, s := range p.sources {
			marker := "  ├─"
			if s.path == p.target {
				marker = "  ★ (目标)"
			}
			seasonHint := ""
			if s.dirSeason > 0 {
				seasonHint = fmt.Sprintf("  → 强制季号=%d", s.dirSeason)
			}
			fmt.Printf("%s %s%s\n", marker, s.name, seasonHint)
		}
		for _, m := range p.moves {
			if m.skip != "" {
				fmt.Printf("    ✗ skip %s\n        %s\n", m.skip, m.src)
				totalSkips++
				continue
			}
			fmt.Printf("    → %s\n        %s\n", rel(m.dst, abs), rel(m.src, abs))
			totalMoves++
		}
	}
	fmt.Printf("──────────────────────────────────────────────\n")
	fmt.Printf("汇总：%d 个合并组, %d 个搬运操作, %d 个跳过\n", len(plans), totalMoves, totalSkips)
	fmt.Println()

	if !doApply {
		fmt.Println("ⓘ 当前是 dry-run 模式，不会真正写盘。如确认无误，重跑加 -apply 执行。")
		return
	}

	// === 阶段4：执行 ===
	fmt.Println(">>> 开始执行...")
	okMoves, failMoves := 0, 0
	for _, p := range plans {
		// 先建好目标 + season 子目录
		if err := os.MkdirAll(p.target, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "创建目标目录失败: %s: %v\n", p.target, err)
			continue
		}
		for d := range p.mkdirs {
			_ = os.MkdirAll(d, 0o755)
		}
		for _, m := range p.moves {
			if m.skip != "" {
				continue
			}
			if err := os.Rename(m.src, m.dst); err != nil {
				fmt.Fprintf(os.Stderr, "搬运失败 %s -> %s: %v\n", m.src, m.dst, err)
				failMoves++
				continue
			}
			okMoves++
		}
		// 清理空的源目录
		if *removeOld {
			for _, s := range p.sources {
				if s.path == p.target {
					continue
				}
				removeEmptyTree(s.path)
			}
		}
	}
	fmt.Printf("\n✅ 完成：成功 %d 个，失败 %d 个\n", okMoves, failMoves)
}

// stripSeasonFromTargetName 把"一拳超人 第二季 (2018) [tmdbid-74956]"
// 中的季号尾缀去掉，保留 (2018) [tmdbid-74956] 这类有用元数据。
func stripSeasonFromTargetName(name string) string {
	// 临时把 (年份)/[tmdbid-...] 抽出来
	yearMatch := yearPattern.FindString(name)
	tagMatch := idtagPattern.FindString(name)

	stripped := name
	stripped = idtagPattern.ReplaceAllString(stripped, " ")
	stripped = yearPattern.ReplaceAllString(stripped, " ")
	for _, p := range seasonStrips {
		stripped = p.ReplaceAllString(stripped, "")
	}
	stripped = regexp.MustCompile(`\s+`).ReplaceAllString(stripped, " ")
	stripped = strings.TrimSpace(stripped)
	stripped = strings.Trim(stripped, " -·・【】()（）[]")

	// 再拼回去
	out := stripped
	if y := strings.TrimSpace(yearMatch); y != "" {
		out += " " + y
	}
	if t := strings.TrimSpace(tagMatch); t != "" {
		out += " " + t
	}
	return regexp.MustCompile(`\s+`).ReplaceAllString(out, " ")
}

// buildMoves 计算每个 source 中的所有文件最终该搬到哪里
func buildMoves(p *plan) {
	for _, src := range p.sources {
		// 列出 src 下的子目录与散落文件
		entries, err := os.ReadDir(src.path)
		if err != nil {
			continue
		}
		for _, e := range entries {
			full := filepath.Join(src.path, e.Name())
			if e.IsDir() {
				// season 子目录
				subSeason := extractSeasonFromSubDir(e.Name())
				finalSeason := decideFinalSeason(subSeason, src.dirSeason)
				if finalSeason <= 0 {
					// 不是 season 目录，保留为目标目录下同名子目录
					dst := filepath.Join(p.target, e.Name())
					mergeDir(p, full, dst)
					continue
				}
				dstSeasonDir := filepath.Join(p.target, fmt.Sprintf("Season %02d", finalSeason))
				p.mkdirs[dstSeasonDir] = true
				mergeDir(p, full, dstSeasonDir)
			} else {
				// 散落在剧集根的文件（海报/封面/nfo）
				dst := filepath.Join(p.target, e.Name())
				if pathSame(full, dst) {
					continue // 已经在目标
				}
				if _, err := os.Stat(dst); err == nil {
					p.moves = append(p.moves, moveOp{src: full, dst: dst, skip: "目标已存在"})
					continue
				}
				p.moves = append(p.moves, moveOp{src: full, dst: dst})
			}
		}
	}
}

// decideFinalSeason 决定最终落到哪个 Season：
//   - 若 srcDir 名识别到了"第N季"（N>0），强制使用 N
//   - 否则用子目录的 subSeason
func decideFinalSeason(subSeason, dirSeason int) int {
	if dirSeason > 0 {
		return dirSeason
	}
	return subSeason
}

// mergeDir 把 src 目录下的所有文件平铺地搬到 dst 目录下
func mergeDir(p *plan, src, dst string) {
	if pathSame(src, dst) {
		return // 已经是目标
	}
	p.mkdirs[dst] = true
	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}
	for _, e := range entries {
		full := filepath.Join(src, e.Name())
		if e.IsDir() {
			// 嵌套子目录递归（罕见，比如 BDMV）
			mergeDir(p, full, filepath.Join(dst, e.Name()))
			continue
		}
		target := filepath.Join(dst, e.Name())
		if pathSame(full, target) {
			continue
		}
		if _, err := os.Stat(target); err == nil {
			p.moves = append(p.moves, moveOp{src: full, dst: target, skip: "目标已存在"})
			continue
		}
		p.moves = append(p.moves, moveOp{src: full, dst: target})
	}
}

// pathSame 大小写不敏感的同路径判断（Windows 友好）
func pathSame(a, b string) bool {
	pa, _ := filepath.Abs(a)
	pb, _ := filepath.Abs(b)
	return strings.EqualFold(filepath.Clean(pa), filepath.Clean(pb))
}

// rel 把 abs 转为相对 root 的展示用路径
func rel(abs, root string) string {
	r, err := filepath.Rel(root, abs)
	if err != nil {
		return abs
	}
	return r
}

// removeEmptyTree 递归删除空目录（仅当目录树下没有任何文件）
func removeEmptyTree(path string) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			removeEmptyTree(filepath.Join(path, e.Name()))
		}
	}
	// 重新读取，看是否真的为空
	entries2, _ := os.ReadDir(path)
	if len(entries2) == 0 {
		_ = os.Remove(path)
	}
}

func modeStr(apply bool) string {
	if apply {
		return "APPLY (实际执行)"
	}
	return "DRY-RUN (仅预览)"
}
