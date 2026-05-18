package service

import (
	"path/filepath"
	"testing"

	"github.com/nowen-video/nowen-video/internal/model"
)

// TestBuildStandardNames_Movie 验证电影命名（默认 jellyfin 风格）
func TestBuildStandardNames_Movie(t *testing.T) {
	cases := []struct {
		name       string
		in         StandardNameInput
		wantFile   string
		wantFolder string
	}{
		{
			name: "电影 完整字段",
			in: StandardNameInput{
				SourceName: "Avatar.2009.mkv",
				MediaType:  "movie",
				Title:      "Avatar",
				Year:       2009,
				TMDbID:     19995,
				Style:      "jellyfin",
			},
			wantFile:   "Avatar (2009) [tmdbid-19995].mkv",
			wantFolder: "Avatar (2009) [tmdbid-19995]",
		},
		{
			name: "电影 plex 风格",
			in: StandardNameInput{
				SourceName: "黑客帝国.mp4",
				MediaType:  "movie",
				Title:      "黑客帝国",
				Year:       1999,
				TMDbID:     603,
				Style:      "plex",
			},
			wantFile:   "黑客帝国 (1999) {tmdb-603}.mp4",
			wantFolder: "黑客帝国 (1999) [tmdbid-603]", // 文件夹始终用 jellyfin 风格 idtag（更稳定）
		},
		{
			name: "电影 无 ID 无年份",
			in: StandardNameInput{
				SourceName: "未知影片.avi",
				MediaType:  "movie",
				Title:      "未知影片",
			},
			wantFile:   "未知影片.avi",
			wantFolder: "未知影片",
		},
		{
			name: "电影 IMDb ID",
			in: StandardNameInput{
				SourceName: "Casino Royale.mkv",
				MediaType:  "movie",
				Title:      "Casino Royale",
				Year:       2006,
				IMDbID:     "tt0381061",
			},
			wantFile:   "Casino Royale (2006) [imdbid-tt0381061].mkv",
			wantFolder: "Casino Royale (2006) [imdbid-tt0381061]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildStandardNames(tc.in)
			if tc.in.Style == "plex" {
				// plex 文件名校验（文件夹依然按 jellyfin idtag 渲染——这是有意设计，保证目录扫描兼容）
				if got.FileName != tc.wantFile {
					t.Errorf("FileName: 期望 %q 得到 %q", tc.wantFile, got.FileName)
				}
				return
			}
			if got.FileName != tc.wantFile {
				t.Errorf("FileName: 期望 %q 得到 %q", tc.wantFile, got.FileName)
			}
			if got.MovieFolder != tc.wantFolder {
				t.Errorf("MovieFolder: 期望 %q 得到 %q", tc.wantFolder, got.MovieFolder)
			}
		})
	}
}

// TestBuildStandardNames_Episode 剧集命名（含季号兜底、季尾缀剥离）
func TestBuildStandardNames_Episode(t *testing.T) {
	cases := []struct {
		name       string
		in         StandardNameInput
		wantFile   string
		wantFolder string
		wantSeason string
	}{
		{
			name: "标准剧集 完整字段",
			in: StandardNameInput{
				SourceName: "OnePunchMan.S01E01.mkv",
				MediaType:  "episode",
				Title:      "一拳超人",
				Year:       2015,
				TMDbID:     63926,
				SeasonNum:  1,
				EpisodeNum: 1,
				Style:      "jellyfin",
			},
			wantFile:   "一拳超人 (2015) S01E01 [tmdbid-63926].mkv",
			wantFolder: "一拳超人 (2015) [tmdbid-63926]",
			wantSeason: "Season 01",
		},
		{
			name: "季尾缀剥离 - 中文",
			in: StandardNameInput{
				SourceName: "一拳超人 第二季 S02E13.mp4",
				MediaType:  "episode",
				Title:      "一拳超人 第二季",
				TMDbID:     74956,
				SeasonNum:  2,
				EpisodeNum: 13,
				Style:      "jellyfin",
			},
			wantFile:   "一拳超人 S02E13 [tmdbid-74956].mp4",
			wantFolder: "一拳超人 [tmdbid-74956]",
			wantSeason: "Season 02",
		},
		{
			name: "季号兜底 - SeasonNum=0",
			in: StandardNameInput{
				SourceName: "管家后宫学园.mkv",
				MediaType:  "episode",
				Title:      "管家后宫学园",
				Year:       2010,
				SeasonNum:  0, // 兜底应该 → 1
				EpisodeNum: 5,
				Style:      "jellyfin",
			},
			wantFile:   "管家后宫学园 (2010) S01E05.mkv",
			wantFolder: "管家后宫学园 (2010)",
			wantSeason: "Season 01",
		},
		{
			name: "季尾缀剥离 - 英文 Season 2",
			in: StandardNameInput{
				SourceName: "Breaking Bad Season 2 E01.mkv",
				MediaType:  "episode",
				Title:      "Breaking Bad Season 2",
				TMDbID:     1396,
				SeasonNum:  0, // 应从尾缀回收为 2
				EpisodeNum: 1,
				Style:      "jellyfin",
			},
			wantFile:   "Breaking Bad S02E01 [tmdbid-1396].mkv",
			wantFolder: "Breaking Bad [tmdbid-1396]",
			wantSeason: "Season 02",
		},
		{
			name: "集号未知 - FileName 应为空",
			in: StandardNameInput{
				SourceName: "怪物娘的医生 NCED.mkv",
				MediaType:  "episode",
				Title:      "怪物娘的医生",
				SeasonNum:  1,
				EpisodeNum: 0, // 未知
				Style:      "jellyfin",
			},
			wantFile:   "", // FileName 为空，让上层归入 _unsorted
			wantFolder: "怪物娘的医生",
			wantSeason: "Season 01",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildStandardNames(tc.in)
			if got.FileName != tc.wantFile {
				t.Errorf("FileName: 期望 %q 得到 %q", tc.wantFile, got.FileName)
			}
			if got.ShowFolder != tc.wantFolder {
				t.Errorf("ShowFolder: 期望 %q 得到 %q", tc.wantFolder, got.ShowFolder)
			}
			if got.SeasonDir != tc.wantSeason {
				t.Errorf("SeasonDir: 期望 %q 得到 %q", tc.wantSeason, got.SeasonDir)
			}
		})
	}
}

// TestExtractIDsFromPath 路径上 [tmdbid-X] / [imdbid-tt] 兜底回收
func TestExtractIDsFromPath(t *testing.T) {
	cases := []struct {
		path     string
		wantTMDb int
		wantIMDb string
	}{
		{
			path:     filepath.Join("D:", "video", "_organized", "Movies", "逃学威龙 (1991) [tmdbid-10258]", "逃学威龙 (1991) [tmdbid-10258].mkv"),
			wantTMDb: 10258,
		},
		{
			path:     filepath.Join("D:", "TV", "Breaking Bad [imdbid-tt0903747]", "Season 01", "E01.mkv"),
			wantIMDb: "tt0903747",
		},
		{
			path:     filepath.Join("D:", "video", "Movies", "Avatar (2009) {tmdb-19995}", "Avatar.mkv"),
			wantTMDb: 19995,
		},
		{
			path:     filepath.Join("D:", "video", "Movies", "无标签", "Movie.mkv"),
			wantTMDb: 0,
			wantIMDb: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			gotT, gotI := ExtractIDsFromPath(tc.path)
			if gotT != tc.wantTMDb {
				t.Errorf("TMDbID: 期望 %d 得到 %d", tc.wantTMDb, gotT)
			}
			if gotI != tc.wantIMDb {
				t.Errorf("IMDbID: 期望 %q 得到 %q", tc.wantIMDb, gotI)
			}
		})
	}
}

// TestSmartRename_RenderTargetName_Episode 验证 SmartRename 渲染剧集时季号兜底已生效
func TestSmartRename_RenderTargetName_Episode(t *testing.T) {
	s := &SmartRenameService{}
	item := &model.RenamePlanItem{
		SourceName: "管家后宫学园 EP05.mkv",
		MediaType:  "episode",
		SeasonNum:  0, // 兜底 → 1
		EpisodeNum: 5,
	}
	parsed := ParsedFilename{
		Title:  "管家后宫学园",
		Year:   2010,
		TMDbID: 88735,
	}
	got, err := s.renderTargetName("jellyfin", "", parsed, item)
	if err != nil {
		t.Fatalf("renderTargetName error: %v", err)
	}
	want := "管家后宫学园 (2010) S01E05 [tmdbid-88735].mkv"
	if got != want {
		t.Errorf("FileName: 期望 %q 得到 %q", want, got)
	}
	if item.SeasonNum != 1 {
		t.Errorf("SeasonNum 兜底未生效：期望 1 得到 %d", item.SeasonNum)
	}
}
