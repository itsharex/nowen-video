package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nowen-video/nowen-video/internal/model"
	"github.com/nowen-video/nowen-video/internal/repository"
	"go.uber.org/zap"
)

// ==================== SmartRename 智能扫描重命名服务 ====================
//
// 模块职责：
//   P0 识别评分：基于规则的命名解析（复用 ParseMovieFilename）+ 文件落库匹配 -> 置信度
//   P1 AI Fallback：低置信度时调用 AIService.ChatCompletion 补全识别
//   P2 命名模板：Jellyfin/Emby `[tmdbid-xxx]` 默认；可切 Plex `{tmdb-xxx}`
//   P3 关联资源：同名 .nfo/.srt/.ass/.sub/-poster.jpg/-fanart.jpg/-thumb.jpg/.idx 等随主迁移
//   P4 安全检测：跨卷、目标已存在、磁盘空间、硬链接计数、相对路径越界
//   P5 事务执行：plan + journal，按条目串行原子，遇错回滚已成功部分
//   P6 默认 dry-run：仅 confirm=true 才真正动盘
//
// 该服务不修改 FileManagerService 现有 API，独立运转。

// ================================ Constants ================================

// 常见视频扩展名
var smartRenameVideoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".wmv": true,
	".flv": true, ".webm": true, ".m4v": true, ".ts": true, ".m2ts": true,
	".mpg": true, ".mpeg": true, ".rmvb": true, ".rm": true, ".3gp": true,
	".vob": true, ".iso": true,
}

// 关联资源扩展名（不带前缀的尾缀） -> kind
var smartRenameRelatedExts = map[string]string{
	".nfo":  "nfo",
	".srt":  "subtitle",
	".ass":  "subtitle",
	".ssa":  "subtitle",
	".sub":  "subtitle",
	".idx":  "subtitle",
	".vtt":  "subtitle",
	".sup":  "subtitle",
	".lrc":  "subtitle",
	".chs":  "subtitle",
	".cht":  "subtitle",
	".chi":  "subtitle",
	".eng":  "subtitle",
	".jpg":  "image",
	".jpeg": "image",
	".png":  "image",
	".webp": "image",
	".tbn":  "image",
}

// 媒体伴生图片的命名后缀（去扩展名后的最末段）
var smartRenameImageSuffix = map[string]string{
	"poster":    "poster",
	"fanart":    "fanart",
	"thumb":     "thumb",
	"banner":    "banner",
	"clearlogo": "clearlogo",
	"landscape": "landscape",
	"disc":      "disc",
	"backdrop":  "fanart",
}

// 命名模板风格
const (
	NamingStyleJellyfin = "jellyfin" // Title (Year) [tmdbid-12345].ext
	NamingStylePlex     = "plex"     // Title (Year) {tmdb-12345}.ext
)

// 安全 / 标题字符清洗：去除 NTFS / ext4 禁用字符
var smartRenameUnsafeCharPattern = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

// ================================ Types ====================================

// SmartRenameConfig 服务级配置（来自全局 config 注入）
type SmartRenameConfig struct {
	DefaultStyle          string   // jellyfin / plex
	AIConfidenceThreshold float64  // 触发 AI 阈值（默认 0.7）
	EnableAIFallback      bool     // 是否启用 AI Fallback
	MaxScanFiles          int      // 单次扫描最大文件数（防爆，默认 5000）
	SafeRoots             []string // 安全根目录白名单：若非空，所有改名必须在白名单内
	RequireConfirm        bool     // 是否强制 confirm（即使前端传 false）
}

// DefaultSmartRenameConfig 默认配置
func DefaultSmartRenameConfig() SmartRenameConfig {
	return SmartRenameConfig{
		DefaultStyle:          NamingStyleJellyfin,
		AIConfidenceThreshold: 0.7,
		EnableAIFallback:      true,
		MaxScanFiles:          5000,
		SafeRoots:             nil,
		RequireConfirm:        true,
	}
}

// SmartRenameRelatedFile 单个关联资源
type SmartRenameRelatedFile struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"` // nfo / subtitle / poster / fanart / thumb / other
}

// SmartRenameSafetyReport 安全检测结果
type SmartRenameSafetyReport struct {
	OK              bool     `json:"ok"`
	CrossVolume     bool     `json:"cross_volume"`   // 跨卷
	TargetExists    bool     `json:"target_exists"`  // 目标已存在
	HardlinkCount   uint64   `json:"hardlink_count"` // 硬链接数（>1 警告）
	OutsideSafeRoot bool     `json:"outside_safe_root"`
	NotEnoughSpace  bool     `json:"not_enough_space"`
	Issues          []string `json:"issues"` // 人类可读问题列表
}

// SmartRenameAIResult AI Fallback 输出结构（强制 JSON Schema）
type SmartRenameAIResult struct {
	Title      string  `json:"title"`
	TitleAlt   string  `json:"title_alt"`
	Year       int     `json:"year"`
	TMDbID     int     `json:"tmdb_id"`
	IMDbID     string  `json:"imdb_id"`
	MediaType  string  `json:"media_type"` // movie / episode / unknown
	Season     int     `json:"season"`
	Episode    int     `json:"episode"`
	Confidence float64 `json:"confidence"` // AI 自评 0~1
}

// ScanInput 扫描入参
type ScanInput struct {
	RootPath              string   // 待扫描根目录（绝对路径）
	LibraryID             string   // 可选：限定到媒体库
	NamingStyle           string   // 可选：jellyfin / plex
	Template              string   // 可选：自定义模板（空则按 style 取默认）
	EnableAIFallback      *bool    // 可选：覆盖默认
	AIConfidenceThreshold *float64 // 可选：覆盖默认
	SafeRoots             []string // 可选：本次扫描覆盖的安全根
	CreatedBy             string   // 当前用户 ID
}

// ExecuteInput 执行入参
type ExecuteInput struct {
	PlanID       string   // 必填
	Confirm      bool     // 必须 true 才真正落盘
	ItemIDs      []string // 可选：仅执行指定条目（空表示全部 pending+safety_ok 条目）
	IgnoreSafety bool     // 可选：用户显式忽略安全警告（默认 false）
}

// SmartRenameService 智能扫描重命名服务
type SmartRenameService struct {
	repo       *repository.RenameRepo
	mediaRepo  *repository.MediaRepo
	seriesRepo *repository.SeriesRepo
	ai         *AIService
	cfg        SmartRenameConfig
	logger     *zap.SugaredLogger

	// preloadedMedia 在单次 Scan 期间临时缓存的"路径→Media"映射；
	// buildItem 内部仅读，外部由 Scan 在每次调用前/后重新设置/清理。
	// 因 Scan 内部使用 errgroup 串行启动+等待，所以无需读写锁。
	preloadedMedia map[string]*model.Media

	// seriesAICache 番剧主干级 AI 结果缓存（生命周期随 Scan）。
	// key 由 seriesFingerprint(srcName, parentDir) 决定，保证同一番剧的不同集
	// 共用同一份 AI 调用结果，避免一集 1 次请求导致的 429 / 配额浪费。
	//
	// AI 命中时调用方仅根据当前文件再算一次集号即可，剧名/年份/TMDb 全部复用。
	seriesAICache   map[string]*SmartRenameAIResult
	seriesAICacheMu sync.Mutex
}

// NewSmartRenameService 构造服务
func NewSmartRenameService(
	repo *repository.RenameRepo,
	mediaRepo *repository.MediaRepo,
	seriesRepo *repository.SeriesRepo,
	ai *AIService,
	cfg SmartRenameConfig,
	logger *zap.SugaredLogger,
) *SmartRenameService {
	if cfg.AIConfidenceThreshold <= 0 {
		cfg.AIConfidenceThreshold = 0.7
	}
	if cfg.MaxScanFiles <= 0 {
		cfg.MaxScanFiles = 5000
	}
	if cfg.DefaultStyle == "" {
		cfg.DefaultStyle = NamingStyleJellyfin
	}
	return &SmartRenameService{
		repo:       repo,
		mediaRepo:  mediaRepo,
		seriesRepo: seriesRepo,
		ai:         ai,
		cfg:        cfg,
		logger:     logger,
	}
}

// ================================ P0+P1: 扫描 + 规划 ==========================

// Scan 扫描目录、识别每个视频文件、生成规划任务（draft 状态）。
//
// 不会动磁盘，仅在 DB 中创建 RenamePlan + 一组 RenamePlanItem。
func (s *SmartRenameService) Scan(in ScanInput) (*model.RenamePlan, error) {
	if in.RootPath == "" {
		return nil, errors.New("root_path 必填")
	}
	absRoot, err := filepath.Abs(in.RootPath)
	if err != nil {
		return nil, fmt.Errorf("根目录非法: %w", err)
	}
	st, err := os.Stat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("根目录不可访问: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("根目录不是目录: %s", absRoot)
	}

	// 合并配置
	style := strings.ToLower(strings.TrimSpace(in.NamingStyle))
	if style != NamingStyleJellyfin && style != NamingStylePlex {
		style = s.cfg.DefaultStyle
	}
	enableAI := s.cfg.EnableAIFallback
	if in.EnableAIFallback != nil {
		enableAI = *in.EnableAIFallback
	}
	threshold := s.cfg.AIConfidenceThreshold
	if in.AIConfidenceThreshold != nil && *in.AIConfidenceThreshold > 0 {
		threshold = *in.AIConfidenceThreshold
	}
	safeRoots := in.SafeRoots
	if len(safeRoots) == 0 {
		safeRoots = s.cfg.SafeRoots
	}

	// 1) 扫描视频文件
	videoFiles, err := s.collectVideoFiles(absRoot)
	if err != nil {
		return nil, fmt.Errorf("扫描失败: %w", err)
	}
	s.logger.Infof("[SmartRename] 扫描完成：发现 %d 个视频文件 root=%s", len(videoFiles), absRoot)

	// 2) 持久化 Plan
	planID := uuid.New().String()
	plan := &model.RenamePlan{
		ID:                    planID,
		LibraryID:             in.LibraryID,
		RootPath:              absRoot,
		NamingStyle:           style,
		Template:              in.Template,
		EnableAIFallback:      enableAI,
		AIConfidenceThreshold: threshold,
		Status:                model.RenamePlanStatusDraft,
		DryRun:                true,
		TotalItems:            len(videoFiles),
		CreatedBy:             in.CreatedBy,
	}
	if err := s.repo.CreatePlan(plan); err != nil {
		return nil, fmt.Errorf("持久化规划失败: %w", err)
	}

	// 3) 预加载：一次 SQL 拉全部 file_path→Media 映射，避免循环内 N+1。
	mediaMap := s.preloadMediaMap(videoFiles)

	// 3.5) 初始化"番剧主干 AI 缓存"，本次 Scan 期间共享，结束清理。
	//      作用：同一番剧的多集只调用一次 AI，节省配额并规避 429 限流。
	s.seriesAICacheMu.Lock()
	s.seriesAICache = make(map[string]*SmartRenameAIResult)
	s.seriesAICacheMu.Unlock()
	defer func() {
		s.seriesAICacheMu.Lock()
		s.seriesAICache = nil
		s.seriesAICacheMu.Unlock()
	}()

	// 3.6) 主动批量预热番剧主干 AI 缓存（C 阶段增强）：
	//      在 buildItemsParallel 启动之前，按番剧主干分组，对"每组 ≥ 2 个文件"
	//      的目录主动调用一次 AI 批量识别，结果回填到 seriesAICache。
	//      这样后续逐文件 build 时就直接走缓存，单组 N 集只产生 1 次 AI 调用。
	if enableAI && s.ai != nil && s.ai.IsEnabled() {
		s.prewarmSeriesAICache(videoFiles, mediaMap, threshold)
	}

	// 4) 并发识别 + 生成条目
	items := s.buildItemsParallel(planID, videoFiles, style, in.Template, enableAI, threshold, safeRoots, mediaMap)

	// 5) 统计汇总
	stats := struct {
		need, skipped, unsafe, ai int
	}{}
	for i := range items {
		it := &items[i]
		if it.AIInvoked {
			stats.ai++
		}
		switch it.Status {
		case model.RenameItemStatusPending:
			stats.need++
		case model.RenameItemStatusSkipped:
			stats.skipped++
		case model.RenameItemStatusUnsafe:
			stats.unsafe++
		}
	}

	if err := s.repo.CreateItems(items); err != nil {
		return nil, fmt.Errorf("持久化条目失败: %w", err)
	}

	// 4) 更新统计
	plan.NeedRename = stats.need
	plan.SkippedItems = stats.skipped
	plan.UnsafeItems = stats.unsafe
	plan.AIInvocations = stats.ai
	if err := s.repo.UpdatePlanFields(planID, map[string]interface{}{
		"need_rename":    stats.need,
		"skipped_items":  stats.skipped,
		"unsafe_items":   stats.unsafe,
		"ai_invocations": stats.ai,
	}); err != nil {
		s.logger.Warnf("[SmartRename] 更新规划统计失败: %v", err)
	}

	// 重新加载（带 items 返回）
	return s.repo.GetPlanWithItems(planID)
}

// collectVideoFiles 递归扫描目录，仅收集视频文件
//
// 加速优化：
//   - 跳过小于 10MB 的文件：片头/广告/样片/损坏文件，避免浪费 AI 调用 + 后续磁盘 IO；
//   - .strm 远程流文件 大小不代表内容，豁免大小过滤。
func (s *SmartRenameService) collectVideoFiles(root string) ([]string, error) {
	var files []string
	maxFiles := s.cfg.MaxScanFiles
	const minVideoBytes = 10 * 1024 * 1024 // 10MB
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			s.logger.Warnf("[SmartRename] 跳过不可访问路径 %s: %v", p, err)
			return nil
		}
		if d.IsDir() {
			// 忽略以 . / @eaDir 开头的目录
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "@eaDir" || name == "$RECYCLE.BIN" || name == "System Volume Information" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if !smartRenameVideoExts[ext] {
			return nil
		}
		// 小文件过滤（.strm 远程流豁免）
		if ext != ".strm" {
			if info, e := d.Info(); e == nil && info.Size() < minVideoBytes {
				return nil
			}
		}
		files = append(files, p)
		if maxFiles > 0 && len(files) >= maxFiles {
			return errStopWalk
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return nil, err
	}
	return files, nil
}

var errStopWalk = errors.New("stop walk")

// buildItem 针对单个视频文件生成 RenamePlanItem
func (s *SmartRenameService) buildItem(
	planID, src, style, customTpl string,
	enableAI bool, aiThreshold float64,
	safeRoots []string,
	usedTargets map[string]bool,
) (*model.RenamePlanItem, error) {
	srcName := filepath.Base(src)
	item := &model.RenamePlanItem{
		ID:         uuid.New().String(),
		PlanID:     planID,
		SourcePath: src,
		SourceName: srcName,
		Status:     model.RenameItemStatusPending,
	}

	// === P0: 规则解析 + 置信度评分 ===
	parsed := ParseMovieFilename(srcName)
	conf := s.scoreConfidence(parsed)

	// === 关联落库的 Media (若有)：优先用 DB 信息覆盖 ===
	// 优先从预加载 map 取；未传则兑底 SQL。
	var mediaInfo *model.Media
	if s.preloadedMedia != nil {
		mediaInfo = s.preloadedMedia[src]
	} else {
		mediaInfo, _ = s.lookupMediaByPath(src)
	}
	if mediaInfo != nil {
		item.MediaID = mediaInfo.ID
		// 用 DB 中的精确字段强化识别
		if mediaInfo.Title != "" {
			parsed.Title = mediaInfo.Title
		}
		if mediaInfo.OrigTitle != "" && parsed.TitleAlt == "" {
			parsed.TitleAlt = mediaInfo.OrigTitle
		}
		if mediaInfo.Year > 0 {
			parsed.Year = mediaInfo.Year
		}
		if mediaInfo.TMDbID > 0 {
			parsed.TMDbID = mediaInfo.TMDbID
		}
		if mediaInfo.IMDbID != "" {
			parsed.IMDbID = mediaInfo.IMDbID
		}
		// DB 已有准确的 TMDb 识别 -> 置信度拉满
		if mediaInfo.TMDbID > 0 {
			conf = 0.99
		} // 否则保持规则评分，让 AI 有机会纠正文件名不含真实标题的情况
		item.MediaType = mediaInfo.MediaType
		item.SeasonNum = mediaInfo.SeasonNum
		item.EpisodeNum = mediaInfo.EpisodeNum
	}

	// === P1: AI Fallback ===
	// 规则置信度不足且 AI 可用时触发。当 AI 返回结果的置信度高于规则时，
	// AI 结果完全覆盖规则结果（解决文件名不含真实标题、标题在目录名中的场景）。
	if enableAI && conf < aiThreshold && s.ai != nil && s.ai.IsEnabled() {
		// 把父目录名也传给 AI，增加识别上下文（如文件名不含标题但目录名包含剧名）
		parentDir := filepath.Base(filepath.Dir(src))

		// === 番剧主干缓存：相同剧的不同集复用 ===
		// 例如 `Ladies versus Butlers - 02/03/04...` 共用一次 AI 结果，
		// 仅替换集号；显著降低 AI 调用量，规避 429 限流。
		fp := s.seriesFingerprint(srcName, parentDir)
		var aiRes *SmartRenameAIResult
		var aiRaw string
		var aiErr error
		var fromSeriesCache bool
		if fp != "" {
			s.seriesAICacheMu.Lock()
			if cached, ok := s.seriesAICache[fp]; ok && cached != nil {
				// 深拷贝缓存条目，避免被本条目的集号修改污染其他文件
				cp := *cached
				aiRes = &cp
				aiRaw = "(reused from series cache)"
				fromSeriesCache = true
			}
			s.seriesAICacheMu.Unlock()
		}

		if !fromSeriesCache {
			aiRes, aiRaw, aiErr = s.callAIFallback(srcName, parentDir, parsed)
			// 命中成功后写回番剧主干缓存
			if aiErr == nil && aiRes != nil && fp != "" {
				cp := *aiRes
				s.seriesAICacheMu.Lock()
				s.seriesAICache[fp] = &cp
				s.seriesAICacheMu.Unlock()
			}
		}
		item.AIInvoked = true
		item.AIRawResponse = aiRaw

		// 共用缓存场景下，集号必须按当前文件名重新解析（缓存里的是别的文件的集号）
		if fromSeriesCache && aiRes != nil {
			if se, ep := extractSxxExx(srcName); se > 0 && ep > 0 {
				aiRes.Season = se
				aiRes.Episode = ep
			} else if ep := extractEpisodeNumber(srcName); ep > 0 {
				if aiRes.Season <= 0 {
					aiRes.Season = 1
				}
				aiRes.Episode = ep
			}
		}
		if aiErr == nil && aiRes != nil {
			// AI 置信度高于规则时，完全采纳 AI 结果
			if aiRes.Confidence >= conf {
				if aiRes.Title != "" {
					parsed.Title = aiRes.Title
				}
				if aiRes.TitleAlt != "" {
					parsed.TitleAlt = aiRes.TitleAlt
				}
				if aiRes.Year > 0 {
					parsed.Year = aiRes.Year
				}
				if aiRes.TMDbID > 0 {
					parsed.TMDbID = aiRes.TMDbID
				}
				if aiRes.IMDbID != "" {
					parsed.IMDbID = aiRes.IMDbID
				}
				if aiRes.MediaType != "" {
					item.MediaType = aiRes.MediaType
				}
				if aiRes.Season > 0 {
					item.SeasonNum = aiRes.Season
				}
				if aiRes.Episode > 0 {
					item.EpisodeNum = aiRes.Episode
				}
				conf = aiRes.Confidence
			} else {
				// AI 置信度低于规则：仅在原字段为空时补充
				if parsed.Title == "" && aiRes.Title != "" {
					parsed.Title = aiRes.Title
				}
				if parsed.TitleAlt == "" && aiRes.TitleAlt != "" {
					parsed.TitleAlt = aiRes.TitleAlt
				}
				if parsed.Year == 0 && aiRes.Year > 0 {
					parsed.Year = aiRes.Year
				}
				if parsed.TMDbID == 0 && aiRes.TMDbID > 0 {
					parsed.TMDbID = aiRes.TMDbID
				}
				if parsed.IMDbID == "" && aiRes.IMDbID != "" {
					parsed.IMDbID = aiRes.IMDbID
				}
				if item.MediaType == "" && aiRes.MediaType != "" {
					item.MediaType = aiRes.MediaType
				}
				if item.SeasonNum == 0 && aiRes.Season > 0 {
					item.SeasonNum = aiRes.Season
				}
				if item.EpisodeNum == 0 && aiRes.Episode > 0 {
					item.EpisodeNum = aiRes.Episode
				}
				if aiRes.Confidence > conf {
					conf = aiRes.Confidence
				}
			}
		} else if aiErr != nil {
			s.logger.Warnf("[SmartRename] AI Fallback 失败 file=%s: %v", srcName, aiErr)
		}
	}

	// 兜底：未识别 Title 时使用文件名主体
	if parsed.Title == "" {
		parsed.Title = strings.TrimSuffix(srcName, filepath.Ext(srcName))
	}
	if item.MediaType == "" {
		// 默认按电影；若文件名中检测出 SxxExx 则改为 episode（粗略再扫一次）
		if s, e := extractSxxExx(srcName); s > 0 && e > 0 {
			item.MediaType = "episode"
			item.SeasonNum = s
			item.EpisodeNum = e
		} else {
			item.MediaType = "movie"
		}
	}

	item.ParsedTitle = parsed.Title
	item.ParsedTitleAlt = parsed.TitleAlt
	item.ParsedYear = parsed.Year
	item.ParsedTMDbID = parsed.TMDbID
	item.ParsedIMDbID = parsed.IMDbID
	item.Confidence = conf

	// === P2: 渲染目标命名 ===
	targetName, err := s.renderTargetName(style, customTpl, parsed, item)
	if err != nil {
		return nil, err
	}
	targetPath := filepath.Join(filepath.Dir(src), targetName)
	item.TargetName = targetName
	item.TargetPath = targetPath

	// 如果目标名等于源名 -> 跳过（已是目标格式）
	if filepath.Base(src) == targetName {
		item.Status = model.RenameItemStatusSkipped
		item.SafetyOK = true
		item.SafetyNote = "已是目标命名"
		return item, nil
	}

	// === P3: 关联资源 ===
	relatedRaw, relatedTargets := s.collectRelatedFiles(src, targetPath)
	if buf, err := json.Marshal(relatedRaw); err == nil {
		item.RelatedFilesJSON = string(buf)
	}

	// === P4: 安全检测 ===
	allTargets := append([]string{targetPath}, relatedTargets...)
	safety := s.checkSafety(src, targetPath, allTargets, safeRoots, usedTargets)
	if buf, err := json.Marshal(safety); err == nil {
		item.SafetyJSON = string(buf)
	}
	item.SafetyOK = safety.OK
	if !safety.OK {
		item.SafetyNote = strings.Join(safety.Issues, "; ")
		item.Status = model.RenameItemStatusUnsafe
	} else {
		// 标记目标已占用（跨平台大小写策略由 pathKey 决定）
		for _, t := range allTargets {
			usedTargets[pathKey(t)] = true
		}
	}

	return item, nil
}

// scoreConfidence 基于解析结果计算 0~1 的置信度
//
// 评分模型（最高 1.0）：
//   - 有 TMDbID：+0.5（强证据）
//   - 有 IMDbID：+0.4
//   - Title 非空且非全 ASCII 噪声：+0.25
//   - Year > 0：+0.2
//   - TitleAlt 非空：+0.05
func (s *SmartRenameService) scoreConfidence(p ParsedFilename) float64 {
	score := 0.0
	if p.TMDbID > 0 {
		score += 0.5
	}
	if p.IMDbID != "" {
		score += 0.4
	}
	if p.Title != "" && len([]rune(strings.TrimSpace(p.Title))) >= 2 {
		score += 0.25
	}
	if p.Year > 0 {
		score += 0.2
	}
	if p.TitleAlt != "" {
		score += 0.05
	}
	if score > 1.0 {
		score = 1.0
	}
	return score
}

// preloadMediaMap 一次 SQL 把所有候选源路径对应的 Media 拉出来，回写到 s.preloadedMedia。
//
// 任何失败都不会阻断流程：返回 nil/空 map 时，buildItem 会自动回退到逐条 SQL 查询。
func (s *SmartRenameService) preloadMediaMap(paths []string) map[string]*model.Media {
	if s.mediaRepo == nil || len(paths) == 0 {
		return nil
	}
	m, err := s.mediaRepo.ListByFilePaths(paths)
	if err != nil {
		s.logger.Warnf("[SmartRename] 预加载 Media 失败（回退到逐条查询）: %v", err)
		return nil
	}
	return m
}

// buildItemsParallel 并发执行 buildItem。
//
// 关键点：
//   - 并发数 = min(8, NumCPU*2)，AI 可用时再被 AIService.semaphore 收紧到全局并发上限；
//   - usedTargets 是"目标路径占用表"（防止两个源解析到相同目标），并发下用 mu 保护；
//   - 结果 items 与 paths 顺序一一对应（用 index 写回，避免 channel 乱序）。
func (s *SmartRenameService) buildItemsParallel(
	planID string,
	paths []string,
	style, customTpl string,
	enableAI bool, aiThreshold float64,
	safeRoots []string,
	preloaded map[string]*model.Media,
) []model.RenamePlanItem {
	// 临时把预加载 map 注入服务，buildItem 直接读
	s.preloadedMedia = preloaded
	defer func() { s.preloadedMedia = nil }()

	items := make([]model.RenamePlanItem, len(paths))

	// 并发参数
	workers := runtime.NumCPU() * 2
	if workers < 4 {
		workers = 4
	}
	if workers > 8 {
		workers = 8
	}

	var (
		mu          sync.Mutex
		usedTargets = map[string]bool{}
		wg          sync.WaitGroup
	)

	jobs := make(chan int, len(paths))

	worker := func() {
		defer wg.Done()
		// 每个 goroutine 用独立的 localUsed，避免每条都抢全局锁；
		// 完成后一次性合并到 usedTargets。
		// 但 SafetyCheck 里的 usedTargets 检测必须看到全局视图，
		// 所以这里仍要传共享 map + 加锁。简化：直接共享 + 锁。
		for i := range jobs {
			src := paths[i]
			item, err := s.buildItemSafe(planID, src, style, customTpl, enableAI, aiThreshold, safeRoots, &mu, usedTargets)
			if err != nil {
				items[i] = model.RenamePlanItem{
					ID:         uuid.New().String(),
					PlanID:     planID,
					SourcePath: src,
					SourceName: filepath.Base(src),
					Status:     model.RenameItemStatusFailed,
					ErrorMsg:   err.Error(),
				}
				continue
			}
			items[i] = *item
		}
	}

	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go worker()
	}
	for i := range paths {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return items
}

// buildItemSafe 是 buildItem 的并发安全包装：替换原先直接传 usedTargets map 的方式，
// 由调用方持有锁；内部 buildItem 仍假设 usedTargets 是它独占的临时副本视图。
//
// 实现：在持锁状态下做"读快照 + 调用 buildItem + 合并写回"，保证目标占用判定一致。
func (s *SmartRenameService) buildItemSafe(
	planID, src, style, customTpl string,
	enableAI bool, aiThreshold float64,
	safeRoots []string,
	mu *sync.Mutex,
	usedTargets map[string]bool,
) (*model.RenamePlanItem, error) {
	// 复制一份 used 快照供 buildItem 检测（buildItem 只在内部 markUsed 时往里塞）
	mu.Lock()
	snapshot := make(map[string]bool, len(usedTargets))
	for k, v := range usedTargets {
		snapshot[k] = v
	}
	mu.Unlock()

	item, err := s.buildItem(planID, src, style, customTpl, enableAI, aiThreshold, safeRoots, snapshot)
	if err != nil {
		return nil, err
	}

	// 把 snapshot 中新增的目标占用合并回全局
	mu.Lock()
	for k := range snapshot {
		usedTargets[k] = true
	}
	mu.Unlock()

	return item, nil
}

// lookupMediaByPath 用源路径反查 Media（不强求一定能查到）。
//
// 之前使用 ListFilesAdvanced(keyword=src) 进行 LIKE %src% 模糊查询：
//   - 路径中的 `_` / `%` 会被 LIKE 当作通配符，造成误匹配；
//   - LIMIT 1 + ORDER BY created_at DESC 不一定是精确匹配者，造成漏命中。
//
// 现改用 MediaRepo.FindByFilePath 进行 file_path = ? 精确查询。
func (s *SmartRenameService) lookupMediaByPath(src string) (*model.Media, error) {
	if s.mediaRepo == nil {
		return nil, nil
	}
	m, err := s.mediaRepo.FindByFilePath(src)
	if err != nil {
		if repository.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return m, nil
}

// extractSxxExx 从字符串中找出 SxxExx 集数。
//
// 之前使用 \b（word boundary）作为边界，但 Go 正则中 `_` 是 word char，
// 造成 `something_S01E01_other` 这类常见命名不能匹配。
// 现改用"非字母数字/起始结束"作为显式边界，同时接受 _/-/. /空格 作为可选分隔。
func extractSxxExx(s string) (int, int) {
	re := regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])S(\d{1,3})[._\s\-]?E(\d{1,3})(?:[^A-Za-z0-9]|$)`)
	m := re.FindStringSubmatch(s)
	if len(m) < 3 {
		return 0, 0
	}
	se, _ := strconv.Atoi(m[1])
	ep, _ := strconv.Atoi(m[2])
	return se, ep
}

// extractEpisodeNumber 仅识别"独立集号"（无 SxxExx 时的备选）。
//
// 覆盖国内动漫资源常见命名：
//   - "[ANi] XXX - 02 [1080P]..."
//   - "XXX 第08话 / EP08 / [08]"
//   - "Some Show 12.mkv"
//
// 仅返回正整数集号；返回 0 表示未识别。
func extractEpisodeNumber(s string) int {
	// 去扩展名
	stem := strings.TrimSuffix(s, filepath.Ext(s))
	patterns := []*regexp.Regexp{
		// 显式中/英集号关键词
		regexp.MustCompile(`(?i)(?:^|[\s\-_\[【])(?:EP|E|第)\s*0*(\d{1,4})\s*(?:话|集|话\b|集\b|话|集|\b|[\]\s\-_）】])`),
		// "title - 02 [..." 这种空格-数字-空格段（动漫主流命名）
		regexp.MustCompile(`(?:^|\s|\-)\s*\-\s*0*(\d{1,4})\s*(?:[\[\(\s]|$)`),
		// "[12]" 单独括号集号
		regexp.MustCompile(`[\[【]\s*0*(\d{1,4})\s*[\]】]`),
	}
	for _, re := range patterns {
		m := re.FindStringSubmatch(stem)
		if len(m) >= 2 {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && n < 2000 {
				return n
			}
		}
	}
	return 0
}

// seriesFingerprint 计算"番剧主干"指纹，用于在一次 Scan 期间复用 AI 结果。
//
// 核心思路：剥离一切随集变化的部分（集号 / 季号 / 单独数字段），
// 保留发布组、剧名主体、年份、来源标签等"对所有集都相同"的内容作为指纹。
//
// 同主干 + 同父目录视为同一番剧；仅命中其中一集需要 AI 时，其余集全部走缓存。
//
// 返回 "" 表示无法稳定提取（保守起见此时不复用缓存，仍逐文件调 AI）。
func (s *SmartRenameService) seriesFingerprint(srcName, parentDir string) string {
	stem := strings.TrimSuffix(srcName, filepath.Ext(srcName))
	work := stem

	// 1) 去 SxxExx
	work = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])S\d{1,3}[._\s\-]?E\d{1,3}`).ReplaceAllString(work, " ")
	// 2) 去显式集号关键词（EP08 / 第08话 / 第08集）
	work = regexp.MustCompile(`(?i)(?:EP|E|第)\s*0*\d{1,4}\s*(?:话|集)?`).ReplaceAllString(work, " ")
	// 3) 去 " - 02 " / " - 003 " 这种动漫常见集号段
	work = regexp.MustCompile(`(?:\s|\-|_)\-?\s*0*\d{1,4}\s*(?:[\[\(]|$)`).ReplaceAllString(work, " ")
	// 4) 去 [12] / 【08】 单独括号集号
	work = regexp.MustCompile(`[\[【]\s*0*\d{1,4}\s*[\]】]`).ReplaceAllString(work, " ")
	// 5) 折叠空白
	work = collapseWhitespace(work)

	// 至少要剩下若干字符才有"主干"意义；过短则放弃复用，回退到逐条 AI
	if len([]rune(work)) < 4 {
		return ""
	}
	// 用父目录名 + 处理后主干 一起作为缓存 key（避免不同剧凑巧主干相同）
	return strings.ToLower(parentDir + "||" + work)
}

// ================================ P1: AI Fallback ============================

// callAIFallback 调用 LLM 还原元数据
func (s *SmartRenameService) callAIFallback(srcName, parentDir string, hint ParsedFilename) (*SmartRenameAIResult, string, error) {
	if s.ai == nil || !s.ai.IsEnabled() {
		return nil, "", errors.New("AI 服务未启用")
	}

	sysPrompt := `你是影视命名识别专家。根据用户给出的文件名，识别影视作品的元数据。

严格按以下 JSON Schema 返回，不要任何额外解释、不要 Markdown 代码块：
{
  "title": "中文主标题（无则填英文/原始）",
  "title_alt": "英文别名（可空）",
  "year": 1999,
  "tmdb_id": 0,
  "imdb_id": "tt1234567（可空）",
  "media_type": "movie|episode|unknown",
  "season": 0,
  "episode": 0,
  "confidence": 0.85
}

约束：
- 仅识别已知影视作品，不要编造；不确定的字段留空 / 0 / unknown。
- confidence 取 0.0~1.0；若文件名信息严重不足，给 < 0.5。
- 不要输出文件名中明显的 PT 发布组、编码标签等噪声。`

	userPrompt := fmt.Sprintf(`文件名：%s
所在目录：%s
当前规则解析（可能不准）：title=%q title_alt=%q year=%d tmdb=%d imdb=%q
请按 JSON Schema 返回最终识别结果。`,
		srcName, parentDir, hint.Title, hint.TitleAlt, hint.Year, hint.TMDbID, hint.IMDbID)

	// === 二级缓存：相同 (sysPrompt, userPrompt) 直接命中 ===
	// 同一来源目录下大量同名／模式高度相似的文件名是重灾区，
	// 命中后直接节省一次 LLM 往返（≈1~3s + token 费）。
	cacheKey := "smart_rename_v1:" + srcName + "|" + parentDir + "|" +
		fmt.Sprintf("%s|%s|%d|%d|%s", hint.Title, hint.TitleAlt, hint.Year, hint.TMDbID, hint.IMDbID)
	if cached, ok := s.ai.GetCache(cacheKey); ok && cached != "" {
		cleaned := stripJSONFence(cached)
		var out SmartRenameAIResult
		if err := json.Unmarshal([]byte(cleaned), &out); err == nil {
			if out.Confidence <= 0 || out.Confidence > 1 {
				out.Confidence = 0.5
			}
			return &out, cached, nil
		}
		// JSON 损坏的缓存就当未命中
	}

	raw, err := s.ai.ChatCompletion(sysPrompt, userPrompt, 0.2, 512)
	if err != nil {
		return nil, "", err
	}

	// 写缓存（无论解析是否成功都写一份原始 raw，下次至少省掉网络）
	s.ai.SetCache(cacheKey, raw)

	// 清洗：模型可能仍然带 ```json fence
	cleaned := stripJSONFence(raw)
	var out SmartRenameAIResult
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
		return nil, raw, fmt.Errorf("AI 返回 JSON 解析失败: %w", err)
	}
	if out.Confidence <= 0 || out.Confidence > 1 {
		out.Confidence = 0.5
	}
	return &out, raw, nil
}

// prewarmSeriesAICache 按番剧主干分组，对组内 ≥ 2 个文件的目录主动调用一次 AI 批量识别，
// 结果填入 s.seriesAICache，让后续 buildItem 走缓存。
//
// 跳过条件：
//   - 已在 mediaMap 中（DB 已识别，置信度 0.99 不会触发 AI）
//   - 规则解析已经达到 aiThreshold（无需 AI）
//   - 单文件分组（没有"批量节省"价值，留给 buildItem 单独处理）
//
// 失败回退：批量 AI 调用失败/解析失败时，缓存留空 → 后续每文件仍会单独走 AI Fallback
// （那时 B 阶段的"首次成功后回填缓存"机制会接管，最终也只浪费 1 次额外调用）。
func (s *SmartRenameService) prewarmSeriesAICache(
	paths []string,
	mediaMap map[string]*model.Media,
	aiThreshold float64,
) {
	if len(paths) == 0 || s.ai == nil || !s.ai.IsEnabled() {
		return
	}

	groups := map[string]*seriesPrewarmGroup{}

	for _, src := range paths {
		// DB 已识别 → 跳过
		if mediaMap != nil {
			if m, ok := mediaMap[src]; ok && m != nil && m.TMDbID > 0 {
				continue
			}
		}
		name := filepath.Base(src)
		parentDir := filepath.Base(filepath.Dir(src))
		fp := s.seriesFingerprint(name, parentDir)
		if fp == "" {
			continue
		}
		// 规则置信度已达标的不参与（不会触发 AI Fallback）
		parsed := ParseMovieFilename(name)
		if s.scoreConfidence(parsed) >= aiThreshold {
			continue
		}

		if g, ok := groups[fp]; ok {
			if len(g.samples) < 6 { // 最多采 6 个样本，控制 prompt 长度
				g.samples = append(g.samples, name)
			}
		} else {
			groups[fp] = &seriesPrewarmGroup{
				fp:        fp,
				parentDir: parentDir,
				samples:   []string{name},
				hint:      parsed,
			}
		}
	}

	// 仅对 ≥ 2 个文件的组做批量预热，单文件组留给 buildItem
	work := make([]*seriesPrewarmGroup, 0, len(groups))
	totalSamples := 0
	for _, g := range groups {
		if len(g.samples) >= 2 {
			work = append(work, g)
			totalSamples += len(g.samples)
		}
	}
	if len(work) == 0 {
		return
	}

	s.logger.Infof("[SmartRename] 番剧批量预热：%d 组 / 总样本 %d 个", len(work), totalSamples)

	// 串行执行（AI 内部已有并发控制 + 限流；这里再加并发反而易触发 429）
	hits := 0
	for _, g := range work {
		res, err := s.callAIBatchSeries(g.parentDir, g.samples, g.hint)
		if err != nil {
			s.logger.Debugf("[SmartRename] 批量预热失败 fp=%s: %v", g.fp, err)
			continue
		}
		if res == nil {
			continue
		}
		s.seriesAICacheMu.Lock()
		s.seriesAICache[g.fp] = res
		s.seriesAICacheMu.Unlock()
		hits++
	}
	// 节省次数估算：每命中 1 组 = 节省 (samples-1) 次后续 AI 调用
	saved := 0
	if hits > 0 {
		for _, g := range work {
			saved += len(g.samples) - 1
		}
	}
	s.logger.Infof("[SmartRename] 番剧批量预热完成：成功 %d / %d 组（预计节省约 %d 次 AI 调用）",
		hits, len(work), saved)
}

// seriesPrewarmGroup 表示批量预热时一组同番剧的待识别集合
type seriesPrewarmGroup struct {
	fp        string
	parentDir string
	samples   []string
	hint      ParsedFilename
}

// callAIBatchSeries 批量识别一组同番剧文件名。
//
// Prompt 中传入多份文件名 + 父目录上下文，让 AI 识别"这一组属于同一部什么剧"，
// 返回该剧的 Title / TitleAlt / Year / TMDb / IMDb / MediaType；
// 集号字段（season/episode）由调用方在使用时按各自文件名重新解析。
//
// 缓存：同一 (parentDir + 排序后样本指纹) 命中 AIService 双层缓存，避免重复扫描时重新调用。
func (s *SmartRenameService) callAIBatchSeries(parentDir string, samples []string, hint ParsedFilename) (*SmartRenameAIResult, error) {
	if len(samples) == 0 {
		return nil, errors.New("samples 为空")
	}
	// 排序使 cache key 稳定
	sorted := make([]string, len(samples))
	copy(sorted, samples)
	sort.Strings(sorted)

	cacheKey := "smart_rename_batch_v1:" + parentDir + "||" + strings.Join(sorted, "##")
	if cached, ok := s.ai.GetCache(cacheKey); ok && cached != "" {
		var out SmartRenameAIResult
		if err := json.Unmarshal([]byte(stripJSONFence(cached)), &out); err == nil {
			if out.Confidence <= 0 || out.Confidence > 1 {
				out.Confidence = 0.5
			}
			return &out, nil
		}
	}

	sysPrompt := `你是影视命名识别专家。下面给出"同一部影视作品的多个文件名"（通常是同一番剧的不同集、或同一电影的多个版本/光盘）。
请综合多份样本识别出"作品本身"的元数据。

严格按以下 JSON Schema 返回，不要任何额外解释、不要 Markdown 代码块：
{
  "title": "中文主标题（无则填英文/原始）",
  "title_alt": "英文别名（可空）",
  "year": 1999,
  "tmdb_id": 0,
  "imdb_id": "tt1234567（可空）",
  "media_type": "movie|episode|unknown",
  "season": 0,
  "episode": 0,
  "confidence": 0.85
}

约束：
- 仅识别已知影视作品，不要编造；不确定的字段留空 / 0 / unknown。
- 输入是同一作品的多份样本，请输出该"作品"的元数据，而不是某一集；
- 集号 season/episode 在批量场景下意义不大，可填 0；调用方会单独解析每个文件的集号。
- confidence 取 0.0~1.0；样本之间高度一致 + 命名清晰可给 0.85+。`

	var sb strings.Builder
	sb.WriteString("所在目录：")
	sb.WriteString(parentDir)
	sb.WriteString("\n样本文件名（同一部作品）：\n")
	for i, name := range sorted {
		sb.WriteString(fmt.Sprintf("  [%d] %s\n", i+1, name))
	}
	sb.WriteString(fmt.Sprintf("当前规则解析（参考，可能不准）：title=%q title_alt=%q year=%d tmdb=%d imdb=%q\n",
		hint.Title, hint.TitleAlt, hint.Year, hint.TMDbID, hint.IMDbID))
	sb.WriteString("请按 JSON Schema 返回最终识别结果。")

	raw, err := s.ai.ChatCompletion(sysPrompt, sb.String(), 0.2, 512)
	if err != nil {
		return nil, err
	}
	s.ai.SetCache(cacheKey, raw)

	var out SmartRenameAIResult
	if err := json.Unmarshal([]byte(stripJSONFence(raw)), &out); err != nil {
		return nil, fmt.Errorf("AI 批量响应 JSON 解析失败: %w", err)
	}
	if out.Confidence <= 0 || out.Confidence > 1 {
		out.Confidence = 0.5
	}
	return &out, nil
}

// stripJSONFence 剥离 Markdown 代码围栏。
//
// 返回“首个 `{` 到最后一个 `}` 之间”的 JSON 主体，
// 避免代码围栏 / 引导说明多余输出干扰后续 Unmarshal。
func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end >= start {
		return s[start : end+1]
	}
	return s
}

// ================================ P2: 命名模板 ===============================

// renderTargetName 渲染目标文件名（不带目录）
func (s *SmartRenameService) renderTargetName(style, customTpl string, p ParsedFilename, item *model.RenamePlanItem) (string, error) {
	ext := filepath.Ext(item.SourceName)
	// 标题清洗：去掉 NTFS 禁止字符 + 折叠空白
	title := sanitizeTitle(p.Title)
	if title == "" {
		title = sanitizeTitle(strings.TrimSuffix(item.SourceName, ext))
	}

	// 剧集格式：Title S01E02.ext（Jellyfin/Plex 等都识别）
	if item.MediaType == "episode" && item.SeasonNum > 0 && item.EpisodeNum > 0 {
		base := fmt.Sprintf("%s S%02dE%02d", title, item.SeasonNum, item.EpisodeNum)
		if p.Year > 0 {
			base = fmt.Sprintf("%s (%d) S%02dE%02d", title, p.Year, item.SeasonNum, item.EpisodeNum)
		}
		// ID 标签
		base += renderIDTag(style, p.TMDbID, p.IMDbID)
		return base + strings.ToLower(ext), nil
	}

	// 电影格式
	year := ""
	if p.Year > 0 {
		year = fmt.Sprintf(" (%d)", p.Year)
	}

	// 优先采用用户自定义模板（支持占位符 {title}/{year}/{tmdb}/{imdb}/{ext}）
	if strings.TrimSpace(customTpl) != "" {
		out := customTpl
		out = strings.ReplaceAll(out, "{title}", title)
		if p.Year > 0 {
			out = strings.ReplaceAll(out, "{year}", strconv.Itoa(p.Year))
			out = strings.ReplaceAll(out, "({year})", fmt.Sprintf("(%d)", p.Year))
		} else {
			out = strings.ReplaceAll(out, "{year}", "")
			out = strings.ReplaceAll(out, "({year})", "")
		}
		if p.TMDbID > 0 {
			out = strings.ReplaceAll(out, "{tmdb}", strconv.Itoa(p.TMDbID))
		} else {
			out = strings.ReplaceAll(out, "{tmdb}", "")
		}
		out = strings.ReplaceAll(out, "{imdb}", p.IMDbID)
		out = strings.ReplaceAll(out, "{ext}", strings.TrimPrefix(strings.ToLower(ext), "."))
		// 折叠多余空白
		out = collapseWhitespace(out)
		// 如果模板没指定扩展名，自动补
		if !strings.HasSuffix(strings.ToLower(out), strings.ToLower(ext)) {
			out += strings.ToLower(ext)
		}
		return out, nil
	}

	base := fmt.Sprintf("%s%s%s", title, year, renderIDTag(style, p.TMDbID, p.IMDbID))
	base = collapseWhitespace(base)
	return base + strings.ToLower(ext), nil
}

// renderIDTag 按风格生成 ID 标签
func renderIDTag(style string, tmdbID int, imdbID string) string {
	if tmdbID == 0 && imdbID == "" {
		return ""
	}
	switch style {
	case NamingStylePlex:
		// Plex: {tmdb-12345} / {imdb-tt123}
		if tmdbID > 0 {
			return fmt.Sprintf(" {tmdb-%d}", tmdbID)
		}
		return fmt.Sprintf(" {imdb-%s}", imdbID)
	default:
		// Jellyfin/Emby: [tmdbid-12345] / [imdbid-tt123]
		if tmdbID > 0 {
			return fmt.Sprintf(" [tmdbid-%d]", tmdbID)
		}
		return fmt.Sprintf(" [imdbid-%s]", imdbID)
	}
}

// pathKey 返回一个能用于"同一规划内目标占用集合"的路径键。
//
// Windows / macOS 默认不区分大小写，这里走 ToLower；Linux 区分大小写，保留原值。
// 统一走 filepath.Clean，避免例如 `a//b` vs `a/b` 不一致。
func pathKey(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "linux" {
		return p
	}
	return strings.ToLower(p)
}

// pathEqual 判定两个路径是否指向同一位置（考虑平台大小写 + Clean）。
func pathEqual(a, b string) bool {
	return pathKey(a) == pathKey(b)
}

// sanitizeTitle 标题中的 NTFS/ext4 禁用字符替换为空格
func sanitizeTitle(s string) string {
	s = smartRenameUnsafeCharPattern.ReplaceAllString(s, " ")
	return collapseWhitespace(s)
}

// collapseWhitespace 把多空白合一并 trim
func collapseWhitespace(s string) string {
	return strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(s, " "))
}

// ================================ P3: 关联资源 ===============================

// collectRelatedFiles 收集同名/同前缀的关联资源；返回明细 + 目标路径列表
func (s *SmartRenameService) collectRelatedFiles(srcVideo, targetVideo string) ([]SmartRenameRelatedFile, []string) {
	dir := filepath.Dir(srcVideo)
	srcBase := strings.TrimSuffix(filepath.Base(srcVideo), filepath.Ext(srcVideo))
	tgtBase := strings.TrimSuffix(filepath.Base(targetVideo), filepath.Ext(targetVideo))
	tgtDir := filepath.Dir(targetVideo)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}

	var related []SmartRenameRelatedFile
	var targets []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// 跳过自身
		if name == filepath.Base(srcVideo) {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		stem := strings.TrimSuffix(name, filepath.Ext(name))

		// 1) 完全同名前缀（srcBase.ext / srcBase.zh.srt / srcBase-poster.jpg ...）
		if !strings.HasPrefix(stem, srcBase) {
			continue
		}
		suffix := stem[len(srcBase):] // 比如 "-poster" 或 ".zh"
		// 决定 kind
		kind, ok := smartRenameRelatedExts[ext]
		if !ok {
			continue
		}
		// 对于 image 类型，区分海报 / 背景 / 缩略
		if kind == "image" {
			// 去掉前导分隔符
			suffixCore := strings.TrimLeft(suffix, "-._")
			suffixCore = strings.ToLower(suffixCore)
			if k, ok2 := smartRenameImageSuffix[suffixCore]; ok2 {
				kind = k
			} else if suffixCore == "" {
				kind = "thumb" // 同名图（无后缀）
			} else {
				kind = "image"
			}
		}

		newName := tgtBase + suffix + ext
		newPath := filepath.Join(tgtDir, newName)
		related = append(related, SmartRenameRelatedFile{
			Source: filepath.Join(dir, name),
			Target: newPath,
			Kind:   kind,
		})
		targets = append(targets, newPath)
	}
	// 排序，便于前端展示稳定
	sort.SliceStable(related, func(i, j int) bool {
		return related[i].Source < related[j].Source
	})
	return related, targets
}

// ================================ P4: 安全检测 ===============================

// checkSafety 对源/目标进行安全审查
func (s *SmartRenameService) checkSafety(src, tgt string, allTargets, safeRoots []string, usedTargets map[string]bool) SmartRenameSafetyReport {
	report := SmartRenameSafetyReport{OK: true}

	// 1) 跨卷检测（Windows 看盘符；POSIX 看 device id）
	if isCrossVolume(src, tgt) {
		report.CrossVolume = true
		report.Issues = append(report.Issues, "源与目标位于不同卷/盘符")
	}

	// 2) 目标已存在
	for _, t := range allTargets {
		if pathEqual(t, src) {
			continue
		}
		if _, err := os.Stat(t); err == nil {
			report.TargetExists = true
			report.Issues = append(report.Issues, "目标已存在: "+filepath.Base(t))
			break
		}
		// 同一规划中目标已被占用
		if usedTargets[pathKey(t)] {
			report.TargetExists = true
			report.Issues = append(report.Issues, "目标与同一规划内其他条目冲突: "+filepath.Base(t))
			break
		}
	}

	// 3) 硬链接计数（POSIX）
	if hlc := getHardlinkCount(src); hlc > 1 {
		report.HardlinkCount = hlc
		report.Issues = append(report.Issues, fmt.Sprintf("源文件硬链接数=%d，重命名可能影响其他位置", hlc))
	}

	// 4) 安全根白名单
	if len(safeRoots) > 0 {
		ok := false
		for _, root := range safeRoots {
			absRoot, _ := filepath.Abs(root)
			absTgt, _ := filepath.Abs(tgt)
			if absRoot != "" && (strings.HasPrefix(strings.ToLower(absTgt), strings.ToLower(absRoot+string(os.PathSeparator))) ||
				strings.EqualFold(absRoot, absTgt)) {
				ok = true
				break
			}
		}
		if !ok {
			report.OutsideSafeRoot = true
			report.Issues = append(report.Issues, "目标位于安全根白名单之外")
		}
	}

	// 5) 磁盘空间（粗略：只在跨卷时检查；同卷重命名不消耗空间）
	if report.CrossVolume {
		if !hasEnoughSpace(filepath.Dir(tgt), getFileSize(src)) {
			report.NotEnoughSpace = true
			report.Issues = append(report.Issues, "目标卷可用空间不足")
		}
	}

	report.OK = len(report.Issues) == 0
	return report
}

// ================================ P5+P6: 执行（plan -> journal） ===============

// Execute 落盘执行（confirm=false 仅做 dry-run 校验，不真正动盘）
func (s *SmartRenameService) Execute(in ExecuteInput) (*model.RenamePlan, error) {
	plan, err := s.repo.GetPlanWithItems(in.PlanID)
	if err != nil {
		return nil, fmt.Errorf("规划不存在: %w", err)
	}
	if plan.Status != model.RenamePlanStatusDraft && plan.Status != model.RenamePlanStatusFailed {
		return nil, fmt.Errorf("规划状态不允许执行: %s", plan.Status)
	}

	// 强制 confirm
	if s.cfg.RequireConfirm && !in.Confirm {
		// dry-run：把 plan 状态保留 draft，但更新一次校验时间
		_ = s.repo.UpdatePlanFields(plan.ID, map[string]interface{}{
			"dry_run": true,
		})
		return plan, nil
	}

	// 标记执行中
	now := time.Now()
	_ = s.repo.UpdatePlanFields(plan.ID, map[string]interface{}{
		"status":      model.RenamePlanStatusExecuting,
		"dry_run":     false,
		"executed_at": &now,
	})

	// 过滤需要执行的条目
	itemFilter := map[string]bool{}
	for _, id := range in.ItemIDs {
		itemFilter[id] = true
	}

	executed := 0
	failed := 0
	executor := newRenameExecutor(s.repo, s.logger)

	for i := range plan.Items {
		it := &plan.Items[i]
		if len(itemFilter) > 0 && !itemFilter[it.ID] {
			continue
		}
		if it.Excluded {
			continue
		}
		if it.Status != model.RenameItemStatusPending && it.Status != model.RenameItemStatusFailed {
			continue
		}
		if !it.SafetyOK && !in.IgnoreSafety {
			continue
		}
		// 取 OverrideName 覆盖
		if it.OverrideName != "" {
			it.TargetName = it.OverrideName
			it.TargetPath = filepath.Join(filepath.Dir(it.SourcePath), it.OverrideName)
		}

		var related []SmartRenameRelatedFile
		if it.RelatedFilesJSON != "" {
			_ = json.Unmarshal([]byte(it.RelatedFilesJSON), &related)
		}

		if err := executor.executeItem(plan.ID, it, related); err != nil {
			failed++
			it.Status = model.RenameItemStatusFailed
			it.ErrorMsg = err.Error()
			_ = s.repo.UpdateItem(it)
			s.logger.Errorf("[SmartRename] 执行条目失败 plan=%s item=%s: %v", plan.ID, it.ID, err)
			continue
		}
		executed++
		it.Status = model.RenameItemStatusExecuted
		_ = s.repo.UpdateItem(it)
	}

	completedAt := time.Now()
	finalStatus := model.RenamePlanStatusCompleted
	if failed > 0 && executed == 0 {
		finalStatus = model.RenamePlanStatusFailed
	}
	_ = s.repo.UpdatePlanFields(plan.ID, map[string]interface{}{
		"status":         finalStatus,
		"executed_items": executed,
		"failed_items":   failed,
		"completed_at":   &completedAt,
	})
	s.logger.Infof("[SmartRename] 规划执行完成 plan=%s executed=%d failed=%d", plan.ID, executed, failed)

	return s.repo.GetPlanWithItems(plan.ID)
}

// Rollback 回滚一次规划（按 journal 倒序逆操作）
func (s *SmartRenameService) Rollback(planID string) (*model.RenamePlan, error) {
	plan, err := s.repo.GetPlanWithItems(planID)
	if err != nil {
		return nil, err
	}
	if plan.Status != model.RenamePlanStatusCompleted &&
		plan.Status != model.RenamePlanStatusFailed {
		return nil, fmt.Errorf("规划状态不可回滚: %s", plan.Status)
	}

	journals, err := s.repo.ListJournalByPlan(planID)
	if err != nil {
		return nil, err
	}
	executor := newRenameExecutor(s.repo, s.logger)
	if err := executor.rollback(journals); err != nil {
		return nil, err
	}

	// 把对应条目标记为 reverted
	for i := range plan.Items {
		it := &plan.Items[i]
		if it.Status == model.RenameItemStatusExecuted {
			it.Status = model.RenameItemStatusReverted
			_ = s.repo.UpdateItem(it)
		}
	}

	_ = s.repo.UpdatePlanFields(planID, map[string]interface{}{
		"status": model.RenamePlanStatusRolledBack,
	})
	return s.repo.GetPlanWithItems(planID)
}

// Cancel 取消（仅 draft 状态可取消）
func (s *SmartRenameService) Cancel(planID string) error {
	plan, err := s.repo.GetPlan(planID)
	if err != nil {
		return err
	}
	if plan.Status != model.RenamePlanStatusDraft {
		return fmt.Errorf("仅 draft 状态可取消，当前: %s", plan.Status)
	}
	return s.repo.UpdatePlanFields(planID, map[string]interface{}{
		"status": model.RenamePlanStatusCanceled,
	})
}

// UpdateItemOverride 用户修改单条目标名 / 排除标记
func (s *SmartRenameService) UpdateItemOverride(itemID, overrideName string, excluded *bool) (*model.RenamePlanItem, error) {
	it, err := s.repo.GetItem(itemID)
	if err != nil {
		return nil, err
	}
	updates := map[string]interface{}{}
	if overrideName != "" {
		updates["override_name"] = overrideName
		updates["target_name"] = overrideName
		updates["target_path"] = filepath.Join(filepath.Dir(it.SourcePath), overrideName)
	}
	if excluded != nil {
		updates["excluded"] = *excluded
	}
	if len(updates) == 0 {
		return it, nil
	}
	if err := s.repo.UpdateItemFields(itemID, updates); err != nil {
		return nil, err
	}
	return s.repo.GetItem(itemID)
}

// ListPlans 列出规划（可按 LibraryID 过滤）
func (s *SmartRenameService) ListPlans(page, size int, libraryID string) ([]model.RenamePlan, int64, error) {
	return s.repo.ListPlansFiltered(libraryID, page, size)
}

// GetPlan 取详情
func (s *SmartRenameService) GetPlan(planID string) (*model.RenamePlan, error) {
	return s.repo.GetPlanWithItems(planID)
}

// DeletePlan 删除（仅非执行中）
func (s *SmartRenameService) DeletePlan(planID string) error {
	plan, err := s.repo.GetPlan(planID)
	if err != nil {
		return err
	}
	if plan.Status == model.RenamePlanStatusExecuting {
		return errors.New("执行中的规划不能删除")
	}
	return s.repo.DeletePlan(planID)
}
