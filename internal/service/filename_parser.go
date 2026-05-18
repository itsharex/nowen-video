package service

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ParsedFilename 统一的文件名解析结果。
//
// 面向电影/剧集通用命名场景，尤其处理国内资源站常见的脏命名：
//   - 01届.《翼》-《Wings》-1927-1929。【十万度Q裙 319940383】.mkv
//   - [yyh3d.com]采花和尚.Satyr Monks.1994.LD_D9.x264.AAC.480P.YYH3D.xt.mkv
//   - Movie.Name.2020.1080p.BluRay.x264.mkv（仍兼容）
//   - Title (2020) [tmdbid=12345].mkv（仍兼容）
type ParsedFilename struct {
	// Title 清洗后的主标题（通常为中文名；若无中文则是英文名）
	Title string
	// TitleAlt 英文别名（当存在《英文名》或中英文并列时填充），便于作为备选搜索词
	TitleAlt string
	// Year 上映年份；0 表示未识别到
	Year int
	// TMDbID / IMDbID 从 [tmdbid=xxx]/{imdb-ttxxx} 标签中识别到的 ID
	TMDbID int
	IMDbID string
}

// ---- 通用正则（文件作用域复用） ----

var (
	// siteTagPattern 匹配文件名开头 / 结尾的站点域名前缀标签，例如 [yyh3d.com]、[nyaa.si]
	siteTagPattern = regexp.MustCompile(`(?i)\[[a-z0-9][a-z0-9\-]*\.[a-z]{2,}(?:\.[a-z]{2,})?\]`)

	// leadingAwardPrefixPattern 匹配开头的"XX届"之类中文前缀（一到三位数字 + 届/集/期），后面可能跟 . / - / 空格
	leadingAwardPrefixPattern = regexp.MustCompile(`^\s*\d{1,3}\s*[届集期]\s*[\.\-_\s]*`)

	// chineseAdPattern 匹配【xxx】或（xxx）中包含推广/联系方式关键词的广告段
	chineseAdPattern = regexp.MustCompile(`[【（\[(][^【】()\[\]（）]*(?:Q裙|Q群|V信|微信|微 信|QQ|薇信|订阅|公众号|关注|联系|淘宝|加入|群号|扫码|小\s*红\s*书|十万度|推广)[^【】()\[\]（）]*[】）\])]`)

	// chineseNameInBookPattern 匹配《中文名》
	chineseNameInBookPattern = regexp.MustCompile(`《([^《》]+)》`)

	// trailingJunkPattern 匹配尾部垃圾后缀，例如 .115chrome_5_17、.cfg 已被 filepath.Ext 处理
	//   115chrome/115/chrome 之类是 115 网盘标记，非文件名内容
	trailingJunkPattern = regexp.MustCompile(`(?i)\.(?:115chrome|115)[_\-\.\w]*$`)

	// embeddedTrailingJunkPattern 文件名中间的 115chrome 标记（刮削前再兜底清理一次）
	embeddedTrailingJunkPattern = regexp.MustCompile(`(?i)[\-\._\s]*(?:115chrome|115)[_\-\.\w]*`)

	// embeddedMediaExtPattern 文件名内嵌的媒体扩展名残留（常见于错误的双扩展名场景，如 ...mkv.115chrome_4_28）
	embeddedMediaExtPattern = regexp.MustCompile(`(?i)\b(?:mkv|mp4|avi|ts|m2ts|mov|wmv|flv|webm|rmvb|mpg|mpeg)\b`)

	// yyh3dTagPattern 匹配常见的私有发布站技术标签组合，如 LD_D9.x264.AAC.480P.YYH3D.xt
	yyh3dTagPattern = regexp.MustCompile(`(?i)\b(?:YYH3D|xt|LD_D\d|LD_D9|D9|D5|D1)\b`)

	// codecNoisePattern 一次性吃掉常见编码/来源/分辨率/音频/HDR/版本/流媒体源等噪声标签
	// 覆盖 PT 资源命名习惯（如：DDP5.1 / TrueHD.7.1 / HDR10+ / Atmos / DV / AMZN.WEB-DL / Criterion 等）
	//
	// 注意：Go 正则的 alternation 是 leftmost-first 而非 longest-match，
	// 因此长 token 必须排在短 token 前（如 HDR10\+ / HDR10Plus 必须出现在 HDR10 之前）。
	codecNoisePattern = regexp.MustCompile(`(?i)\b(` +
		// 介质 / 来源
		`UHD\.?BluRay|Blu-Ray|BluRay|BDRemux|BDRip|HDRip|WEB-?DL|WEB-?Rip|WEBRip|DVDRip|DVDScr|DVD5|DVD9|HDTV|PDTV|HDCam|TS|TC|R5|REMUX|` +
		// 流媒体平台标识
		`AMZN|NFLX|NF|HMAX|MAX|DSNP|ATVP|iTunes|iT|HULU|PCOK|CR|CRAV|FUNI|STAN|VUDU|GPLAY|RED|MA|UPNATOM|` +
		// 版本/特别版
		`PROPER|REPACK|EXTENDED|UNCUT|UNRATED|DIRECTORS\.?CUT|REMASTERED|CRITERION|IMAX|OPEN\.?MATTE|THEATRICAL|FINAL\.?CUT|HYBRID|MULTi|INTERNAL|LIMITED|RERIP|` +
		// 视频编码
		`x264|x265|h\.?264|h\.?265|HEVC|AVC|VC-?1|XViD|DivX|VP9|AV1|MPEG-?[24]|` +
		// 音频编码（长 token 优先）+ 通道
		`DTS-?HD-?MA|DTSHD-?MA|DTS-?HD-?HRA|DTS-?HD|DTS-?MA|DTS-?ES|DTS-?X|TrueHD\.?Atmos|TrueHD|HDMA|Atmos|` +
		`AAC2\.0|HE-AAC|AAC|EAC3|E-?AC3|AC3|` +
		`DDP\d?\.?\d?|DDP|DD\+|DD\d?\.?\d?|` +
		`FLAC2\.0|FLAC|OPUS|MP3|MP2|LPCM|PCM|2Audio|3Audio|2Audios|Dual\.?Audio|` +
		// 分辨率 / 帧率
		`2160[pi]|4320[pi]|1080[pi]|720[pi]|480[pi]|4K|UHD|8K|24fps|25fps|30fps|50fps|60fps|` +
		// 色彩 / HDR 标记（长 token 优先）
		`HDR10Plus|HDR10\+|HDR10|HLG|HDR|SDR|DoVi|Dolby\.?Vision|DV|10bit|8bit|12bit|REC\.?709|REC\.?2020|` +
		// 立体声/3D
		`3D|HSBS|HOU|TAB|SBS|` +
		// 帧标记
		`HQ|LQ|RAW` +
		`)\b\+?`)

	// audioChannelPattern 单独吃掉残留的音频通道数（5.1 / 7.1 / 2.0 / 2.1）
	// 限定为前后均为分隔符或边界，避免误伤剧集 S05E01 / 7.1 之类（剧集是 SxxExx 格式不会单独出现 5.1）
	audioChannelPattern = regexp.MustCompile(`(?:^|[\s\.\-_])([257]\.[01])(?:[\s\.\-_]|$)`)

	// ptSiteTagPattern PT 主站标签：@CHDBits / @MTeam / @HDHome / @HDFans / @OurBits 等
	// 文件名里 @ 几乎只用于 PT 主站标记，长度 2~20 的字母数字串一律视为站点标签
	ptSiteTagPattern = regexp.MustCompile(`(?i)\s*@[A-Za-z][A-Za-z0-9_\-]{1,20}\b`)

	// ptReleaseGroupSuffixPattern 已知 PT/影视资源制作组后缀 -XXX，仅匹配末尾位置
	// 维护高频词表，避免误伤英文片名（例如不能把 The.Matrix 的 Matrix 当成制作组）
	// 词表覆盖中文区 PT 站常见组 + 海外高频组。位置必须紧贴字符串末尾
	ptReleaseGroupSuffixPattern = regexp.MustCompile(`(?i)[\s\-_\.]+-?(` +
		// 中文 PT 站常见组
		`FRDS|CHD|CHDBits|CHDPAD|CHDTV|CHDWEB|WiKi|HDC|HDChina|HDS|HDSky|HDH|HDHome|HDArea|HDWinG|HDFans|MTeam|MTeamPT|MTeamTV|NTb|TLF|MySiLU|OurBits|OurTV|CMCT|CMCTV|MNHD|BeyondHD|KRaLiMaRKo|FraMeSToR|EPSiLON|FoRM|AREY|HHWEB|PTHome|AGSVPT|ZmWeb|NSBC|Pter|PuTao|TTG|TGx|ADWeb|NowYS|HQC|HDU|PTer|Audies|Bambumi|HDSWEB|SiNNERS|playWEB|playHD|playSD|playWeb|GalaxyRG|GalaxyTV|` +
		// 海外高频组
		`RARBG|YTS|YIFY|PSA|EVO|DON|CtrlHD|GECKOS|SPARKS|AMIABLE|MAJESTiC|KamiKaze|KOGi|NTG|decibeL|BMF|HONE|FLUX|NOSiViD|monkee|GLHF|TRiToN|PHOENiX|CMRG|TEPES|RUSTED|playWEB|TURG|ION10|ION265|ION10|ETHEL|TOMMY|SMURF|THUGLiNE` +
		`)$`)

	// spaceSquashPattern 多空格合一
	spaceSquashPattern = regexp.MustCompile(`\s+`)

	// chineseYearRangePattern 匹配 1927-1929 / 1971-1972 这样的年份区间（颁奖届使用跨年），第一个年份即为电影年份
	chineseYearRangePattern = regexp.MustCompile(`((?:19|20)\d{2})\s*[\-–—~～]\s*((?:19|20)\d{2})`)

	// latinTitleRunPattern 连续 ASCII 片段：用于识别并抽取英文别名
	latinTitleRunPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9 '&:,\.\-]*[A-Za-z0-9]`)
)

// ParseMovieFilename 统一的电影文件名解析入口。
//
// 兼容以下脏命名模式：
//  1. "01届.《翼》-《Wings》-1927-1929。【十万度Q裙 319940383】.mkv"
//  2. "[yyh3d.com]采花和尚.Satyr Monks.1994.LD_D9.x264.AAC.480P.YYH3D.xt.mkv"
//  3. "The.Matrix.1999.BluRay.1080p.x264.mkv"
//  4. "Avatar (2009) [tmdbid=19995].mkv"
//  5. 尾部 .115chrome_5_17 垃圾后缀
//
// 返回值字段含义见 ParsedFilename。对于无法识别年份的，Year 为 0；上层可再调用
// extractYearFromName 对整条路径做兜底。
func ParseMovieFilename(filename string) ParsedFilename {
	out := ParsedFilename{}
	if filename == "" {
		return out
	}

	// 1) 剥离扩展名，并清理 .115chrome_xxx 这类紧贴扩展名前的垃圾后缀
	name := strings.TrimSuffix(filename, filepath.Ext(filename))
	name = trailingJunkPattern.ReplaceAllString(name, "")

	// 2) 提取并移除 tmdb/imdb 标签
	if idType, idValue := parseIDFromName(name); idValue != "" {
		switch idType {
		case "tmdbid", "tmdb":
			out.TMDbID, _ = strconv.Atoi(idValue)
		case "imdbid", "imdb":
			out.IMDbID = idValue
		}
	}
	for _, p := range idTagPatterns {
		name = p.ReplaceAllString(name, "")
	}

	// 3) 去掉站点标签 [xxx.com]（无论在头在尾都去）
	name = siteTagPattern.ReplaceAllString(name, "")

	// 4) 去掉开头的"XX届"前缀
	name = leadingAwardPrefixPattern.ReplaceAllString(name, "")

	// 5) 去掉【十万度Q裙 xxx】这种中文广告段
	name = chineseAdPattern.ReplaceAllString(name, "")

	// 6) 把"中文句号。"统一成半角点，便于后续分隔符统一
	name = strings.ReplaceAll(name, "。", ".")
	name = strings.ReplaceAll(name, "　", " ") // 全角空格

	// 7) 年份区间（1927-1929）优先取前者作为电影年份
	if m := chineseYearRangePattern.FindStringSubmatch(name); len(m) >= 2 {
		if y, _ := strconv.Atoi(m[1]); y >= 1900 && y <= 2099 {
			out.Year = y
		}
		name = chineseYearRangePattern.ReplaceAllString(name, " ")
	}

	// 8) 尝试提取《中文名》和《英文名》，优先作为 Title / TitleAlt
	if ms := chineseNameInBookPattern.FindAllStringSubmatch(name, -1); len(ms) > 0 {
		// 第一个书名号视为中文主标题，后续若包含英文字母视为英文别名
		for _, m := range ms {
			inner := strings.TrimSpace(m[1])
			if inner == "" {
				continue
			}
			if out.Title == "" {
				out.Title = inner
				continue
			}
			if out.TitleAlt == "" && containsLatin(inner) {
				out.TitleAlt = inner
			}
		}
		name = chineseNameInBookPattern.ReplaceAllString(name, " ")
	}

	// 9) 去掉内嵌 115chrome 垃圾
	name = embeddedTrailingJunkPattern.ReplaceAllString(name, " ")

	// 9.5) 去掉内嵌媒体扩展名（双扩展名场景，如 xxx.mkv.115chrome_4_28 在第1步被截掉 .115chrome_4_28 后仍残留 .mkv）
	name = embeddedMediaExtPattern.ReplaceAllString(name, " ")

	// 10) 去掉 YYH3D / LD_D9 / xt / D9 等私有站点技术标签
	name = yyh3dTagPattern.ReplaceAllString(name, " ")

	// 11) 编码/来源/分辨率噪声
	name = codecNoisePattern.ReplaceAllString(name, " ")

	// 11.1) 剥离 PT 主站标签 @CHDBits / @MTeam …
	name = ptSiteTagPattern.ReplaceAllString(name, " ")

	// 11.2) 剥离残留的音频通道（5.1/7.1/2.0 等）
	name = audioChannelPattern.ReplaceAllString(name, " ")

	// 11.3) 剥离已知 PT/影视资源制作组后缀 -FRDS / -WiKi …
	//      注意：仅吃尾部位置，且只匹配白名单组名，避免误伤英文标题
	//      可能存在 -MNHD-FRDS 这种叠加，循环最多三次
	for i := 0; i < 3; i++ {
		next := ptReleaseGroupSuffixPattern.ReplaceAllString(strings.TrimRight(name, " ."), "")
		if next == strings.TrimRight(name, " .") {
			break
		}
		name = next
	}

	// 12) 如果之前没拿到年份，再尝试常规括号年份 / 普通年份
	if out.Year == 0 {
		if y := extractYearFromName(name); y > 0 {
			out.Year = y
		}
	}
	if out.Year == 0 {
		if m := regexp.MustCompile(`(?:^|[^0-9])((?:19|20)\d{2})(?:[^0-9]|$)`).FindStringSubmatch(name); len(m) >= 2 {
			if y, _ := strconv.Atoi(m[1]); y >= 1900 && y <= 2099 {
				out.Year = y
			}
		}
	}
	// 无论什么括号，移除年份
	name = yearInNamePattern.ReplaceAllString(name, " ")
	name = yearInNameAnyBracketPattern.ReplaceAllString(name, " ")
	name = regexp.MustCompile(`\b((?:19|20)\d{2})\b`).ReplaceAllString(name, " ")

	// 13) 使用通用括号清洗器去掉【】《》「」『』〈〉等装饰
	name = normalizeXiaoyaTitle(name)

	// 14) 统一分隔符，规范空格
	replacer := strings.NewReplacer(".", " ", "_", " ")
	clean := replacer.Replace(name)
	clean = spaceSquashPattern.ReplaceAllString(clean, " ")
	clean = strings.Trim(clean, " -·・")

	// 15) 如果还没有 Title，就按"中文段优先、否则整串"决定；同时提取英文别名
	if out.Title == "" {
		if cn := pickFirstChineseSegment(clean); cn != "" {
			out.Title = cn
			if out.TitleAlt == "" {
				if en := pickLongestLatinSegment(clean); en != "" && en != cn {
					out.TitleAlt = en
				}
			}
		} else {
			out.Title = clean
		}
	} else if out.TitleAlt == "" {
		// 已有中文 Title，但尾部可能还残留英文名（例如"采花和尚 Satyr Monks"）
		if en := pickLongestLatinSegment(clean); en != "" {
			out.TitleAlt = en
		}
	}

	// 保险：再次去掉首尾分隔
	out.Title = strings.Trim(out.Title, " -·・")
	out.TitleAlt = strings.Trim(out.TitleAlt, " -·・")
	return out
}

// containsLatin 判断字符串是否包含 ASCII 拉丁字母
func containsLatin(s string) bool {
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			return true
		}
	}
	return false
}

// containsHan 判断字符串是否包含 CJK 汉字
func containsHan(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
		if r >= 0x3400 && r <= 0x4DBF {
			return true
		}
	}
	return false
}

// pickFirstChineseSegment 提取连续的中文标题段（包含子标题）。
//
// 关键修复：旧版本遇到"：" / 空格 / 数字 / ASCII 等就截断，导致
//
//	"蜡笔小新：灼热的春日部舞者" 被截成 "蜡笔小新"，
//
// 进而所有蜡笔小新电影刮到同一个 TMDb 条目。
//
// 新策略：从第一个汉字开始扫描，遇到下列字符仍视为"标题内部"继续累积：
//   - 中日文字符（CJK 主面 + 扩展 A）
//   - 中文标点：："、 、 ：、·、・、-、—、—、・
//   - ASCII 标点：':', '-', ' ', '·'
//   - 数字与字母（如 "宝可梦XY"、"Q版三国"）
//
// 仅遇到明确的"段间分隔" - 多个连续空格 / 中文句点 / 制表符等，且后续不再出现汉字时，
// 才停止累积。这样能完整保留"蜡笔小新：灼热的春日部舞者"这种带子标题的命名。
func pickFirstChineseSegment(s string) string {
	runes := []rune(s)
	n := len(runes)

	// 找到第一个汉字位置
	start := -1
	for i, r := range runes {
		if isHanRune(r) {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}

	// 找到最后一个汉字位置
	end := start
	for i := n - 1; i >= start; i-- {
		if isHanRune(runes[i]) {
			end = i
			break
		}
	}

	// 在 [start, end] 之间连续累积，但跳过明显是"段间分隔"的部分
	//   - 连续 2 个及以上空格视为分隔（保守策略）
	//   - 半角句点 '.' / 制表符 '\t' 视为分隔
	// 但单个全角空格、中点 '·'、'：' 等保留为子标题分隔符
	var buf strings.Builder
	for i := start; i <= end; i++ {
		r := runes[i]
		// 连续多空格视为分隔，跳过空白本身但保留之前累积
		if r == ' ' && i+1 <= end && runes[i+1] == ' ' {
			// 多个空格压缩为一个空格
			buf.WriteRune(' ')
			for i+1 <= end && runes[i+1] == ' ' {
				i++
			}
			continue
		}
		// 半角句点和制表符当作空格分隔
		if r == '.' || r == '\t' {
			buf.WriteRune(' ')
			continue
		}
		buf.WriteRune(r)
	}

	result := strings.TrimSpace(buf.String())
	// 把全角冒号 ":" 标准化为半角冒号，便于元数据搜索（TMDb/豆瓣偏好半角）
	result = strings.ReplaceAll(result, "：", ": ")
	// 压缩多空格
	result = spaceSquashPattern.ReplaceAllString(result, " ")
	result = strings.Trim(result, " -·・:")
	return result
}

// isHanRune 是否为 CJK 汉字（含扩展 A）
func isHanRune(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF)
}

// pickLongestLatinSegment 从字符串中挑出最长的拉丁（英文）片段，常见于"中文名 English Title"并列
func pickLongestLatinSegment(s string) string {
	matches := latinTitleRunPattern.FindAllString(s, -1)
	best := ""
	for _, m := range matches {
		t := strings.TrimSpace(m)
		// 过滤纯 1~2 字母噪声（例如 "D9" "xt"）
		if len([]rune(t)) < 3 {
			continue
		}
		if len(t) > len(best) {
			best = t
		}
	}
	return best
}
