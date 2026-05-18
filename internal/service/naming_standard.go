// Package service - 共享标准命名渲染器
//
// 设计目标：把"一键入库（LazyIngest）"沉淀的命名规则抽成单一来源，
// 让智能重命名（SmartRename）/ 扫描归类（ScanPostProcess）/ 一键入库
// 三处对齐，避免规则飘移。
//
// 命名标准（Jellyfin/Emby 兼容）：
//   - 电影文件：Title (Year) [tmdbid-X].ext
//   - 电影目录：Title (Year) [tmdbid-X]
//   - 剧集文件：Title (Year) S01E02 [tmdbid-X].ext
//   - 剧集目录：Title (Year) [tmdbid-X]/Season 01
//
// 关键修正点（一键入库特有）：
//  1. 季号兜底：episode 但 SeasonNum<=0 时强制兜为 1
//  2. 季尾缀剥离：剧集标题中的"第二季 / Season 2 / S2"等会被 NormalizeSeriesTitle 剥掉
//  3. 路径 ID 兜底：当源路径目录名带 [tmdbid-X] 时，自动回收 ID（即使文件名上没有）
package service

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// StandardNameInput 渲染器入参
type StandardNameInput struct {
	// SourcePath 源文件绝对路径（用于路径 ID 兜底；可为空）
	SourcePath string
	// SourceName 源文件名（带扩展名；用于扩展名提取）
	SourceName string
	// MediaType "movie" / "episode" / ""（空 → 视为 movie）
	MediaType string
	// Title 解析后的标题（必须）
	Title string
	// Year 年份；0 = 未知
	Year int
	// TMDbID 0 = 未知
	TMDbID int
	// IMDbID "" = 未知
	IMDbID string
	// SeasonNum 剧集季号；<=0 时本渲染器会兜底为 1
	SeasonNum int
	// EpisodeNum 剧集集号；<=0 表示未识别（仅剧集场景需要）
	EpisodeNum int
	// Style "jellyfin" / "plex"，默认 jellyfin
	Style string
	// CustomTpl 自定义文件名模板（仅电影；为空则用默认）
	CustomTpl string
}

// StandardNames 渲染器输出
type StandardNames struct {
	// FileName 目标文件名（带扩展名，不含目录）
	FileName string
	// MovieFolder 电影专用：影片文件夹名（不含父级 Movies/）
	MovieFolder string
	// ShowFolder 剧集专用：剧集文件夹名（不含父级 TV Shows/，已剥离季号尾缀）
	ShowFolder string
	// SeasonDir 剧集专用：季目录名（如 "Season 01"）
	SeasonDir string
	// EffectiveSeasonNum 实际使用的季号（兜底后）
	EffectiveSeasonNum int
	// EffectiveTitle 实际使用的标题（剧集场景下已剥离季尾缀）
	EffectiveTitle string
}

// pathTMDbIDPattern 匹配路径段中的 [tmdbid-12345] / [tmdbid=12345] / {tmdb-12345}
var pathTMDbIDPattern = regexp.MustCompile(`(?i)[\[{]tmdb(?:id)?[-=](\d+)[\]}]`)

// pathIMDbIDPattern 匹配路径段中的 [imdbid-tt12345] / {imdb-tt12345}
var pathIMDbIDPattern = regexp.MustCompile(`(?i)[\[{]imdb(?:id)?[-=](tt\d+)[\]}]`)

// ExtractIDsFromPath 沿源文件路径自下而上检查每一段目录名 + 文件名，
// 回收最近的 [tmdbid-X] / [imdbid-X] 标签。
//
// 用例：D:\video\_organized\Movies\逃学威龙2\逃学威龙2.mkv
//
//	→ 文件名/父目录都没有 tmdbid，但若再上一层"逃学威龙 (1991) [tmdbid-10258]"有
//	  也能被回收（避免之前批量重命名后 tmdbid 丢失的 BUG）。
//
// 仅在调用方原 ID 为空时使用：调用方自行判断。
func ExtractIDsFromPath(srcPath string) (tmdbID int, imdbID string) {
	if srcPath == "" {
		return 0, ""
	}
	// 自下而上，最近的优先
	cur := filepath.Clean(srcPath)
	for i := 0; i < 6; i++ { // 最多向上看 6 层，避免无限循环
		base := filepath.Base(cur)
		if base == "" || base == "." || base == string(filepath.Separator) {
			break
		}
		if tmdbID == 0 {
			if m := pathTMDbIDPattern.FindStringSubmatch(base); len(m) >= 2 {
				if n, _ := strconv.Atoi(m[1]); n > 0 {
					tmdbID = n
				}
			}
		}
		if imdbID == "" {
			if m := pathIMDbIDPattern.FindStringSubmatch(base); len(m) >= 2 {
				imdbID = m[1]
			}
		}
		if tmdbID > 0 && imdbID != "" {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return
}

// BuildStandardNames 一处渲染，三处共用。规则以一键入库为标准。
func BuildStandardNames(in StandardNameInput) StandardNames {
	out := StandardNames{}

	// ext 统一小写
	ext := strings.ToLower(filepath.Ext(in.SourceName))
	if ext == "" && in.SourcePath != "" {
		ext = strings.ToLower(filepath.Ext(in.SourcePath))
	}

	// 标题清洗
	title := sanitizeTitle(in.Title)
	if title == "" {
		// 兜底：去扩展名后的源文件名
		stem := strings.TrimSuffix(in.SourceName, filepath.Ext(in.SourceName))
		title = sanitizeTitle(stem)
	}

	// 命名风格归一
	style := strings.ToLower(strings.TrimSpace(in.Style))
	if style != NamingStyleJellyfin && style != NamingStylePlex {
		style = NamingStyleJellyfin
	}

	// 是否剧集（mediaType=episode 且 季 / 集 任一已知）
	isEp := strings.EqualFold(in.MediaType, "episode")

	if isEp {
		// === 剧集分支 ===
		// 1) 季尾缀剥离（"一拳超人 第二季" → "一拳超人"），并回收季号
		seasonNum := in.SeasonNum
		if cleaned, recovered := reclaimSeasonFromTitle(title, seasonNum); cleaned != "" {
			title = cleaned
			if seasonNum <= 0 && recovered > 0 {
				seasonNum = recovered
			}
		}
		// 2) 季号兜底（一键入库标准）
		if seasonNum <= 0 {
			seasonNum = 1
		}
		out.EffectiveSeasonNum = seasonNum
		out.EffectiveTitle = title

		// 3) 文件名（集号未知时不渲染文件名 → 调用方应将其归入 _unsorted）
		if in.EpisodeNum > 0 {
			base := fmt.Sprintf("%s S%02dE%02d", title, seasonNum, in.EpisodeNum)
			if in.Year > 0 {
				base = fmt.Sprintf("%s (%d) S%02dE%02d", title, in.Year, seasonNum, in.EpisodeNum)
			}
			base += renderIDTag(style, in.TMDbID, in.IMDbID)
			out.FileName = collapseWhitespace(base) + ext
		}

		// 4) 剧集文件夹名（带 idtag，统一一键入库标准）
		showFolder := title
		if in.Year > 0 {
			showFolder = fmt.Sprintf("%s (%d)", title, in.Year)
		}
		if in.TMDbID > 0 {
			showFolder += fmt.Sprintf(" [tmdbid-%d]", in.TMDbID)
		} else if in.IMDbID != "" {
			showFolder += fmt.Sprintf(" [imdbid-%s]", in.IMDbID)
		}
		out.ShowFolder = collapseWhitespace(showFolder)
		out.SeasonDir = fmt.Sprintf("Season %02d", seasonNum)
		return out
	}

	// === 电影分支 ===
	out.EffectiveTitle = title

	// 优先用户自定义模板
	if strings.TrimSpace(in.CustomTpl) != "" {
		o := in.CustomTpl
		o = strings.ReplaceAll(o, "{title}", title)
		if in.Year > 0 {
			o = strings.ReplaceAll(o, "{year}", strconv.Itoa(in.Year))
			o = strings.ReplaceAll(o, "({year})", fmt.Sprintf("(%d)", in.Year))
		} else {
			o = strings.ReplaceAll(o, "{year}", "")
			o = strings.ReplaceAll(o, "({year})", "")
		}
		if in.TMDbID > 0 {
			o = strings.ReplaceAll(o, "{tmdb}", strconv.Itoa(in.TMDbID))
		} else {
			o = strings.ReplaceAll(o, "{tmdb}", "")
		}
		o = strings.ReplaceAll(o, "{imdb}", in.IMDbID)
		o = strings.ReplaceAll(o, "{ext}", strings.TrimPrefix(ext, "."))
		o = collapseWhitespace(o)
		if !strings.HasSuffix(strings.ToLower(o), ext) {
			o += ext
		}
		out.FileName = o
	} else {
		// 默认电影文件名：Title (Year) [tmdbid-X].ext
		yearTag := ""
		if in.Year > 0 {
			yearTag = fmt.Sprintf(" (%d)", in.Year)
		}
		base := fmt.Sprintf("%s%s%s", title, yearTag, renderIDTag(style, in.TMDbID, in.IMDbID))
		out.FileName = collapseWhitespace(base) + ext
	}

	// 电影文件夹名：Title (Year) [tmdbid-X]
	movieFolder := title
	if in.Year > 0 {
		movieFolder = fmt.Sprintf("%s (%d)", title, in.Year)
	}
	if in.TMDbID > 0 {
		movieFolder += fmt.Sprintf(" [tmdbid-%d]", in.TMDbID)
	} else if in.IMDbID != "" {
		movieFolder += fmt.Sprintf(" [imdbid-%s]", in.IMDbID)
	}
	out.MovieFolder = collapseWhitespace(movieFolder)
	return out
}
