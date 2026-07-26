package filter

import (
	"strings"
	"testing"
)

func mkFiles(paths ...string) []File {
	fs := make([]File, len(paths))
	for i, p := range paths {
		fs[i] = File{Path: p, Size: 1024}
	}
	return fs
}

func TestNormalTorrentPasses(t *testing.T) {
	r := Check("Big Buck Bunny 2008 1080p BluRay x264",
		mkFiles("Big.Buck.Bunny.2008.1080p.BluRay.x264.mkv"), 700<<20)
	if r.Adult || r.Spam {
		t.Fatalf("normal torrent flagged: %+v", r)
	}
}

func TestChineseMovieNamePasses(t *testing.T) {
	for _, name := range []string{
		"流浪地球2 2023 1080p WEB-DL",
		"霸王别姬 1993 修复版",
		"让子弹飞 2010 BluRay 国粤双语",
		"纪录片：舌尖上的中国 第一季 全7集",
	} {
		if r := Check(name, mkFiles("movie.mkv"), 2<<30); r.Adult || r.Spam {
			t.Fatalf("chinese movie %q flagged: %+v", name, r)
		}
	}
}

func TestAdultKeywordBlocked(t *testing.T) {
	cases := []string{
		"Hot Porn Collection 2024",
		"XXX Hardcore Videos Pack",
		"JAV Uncensored 4K Collection",
		"AV女優 無修正 中出し",
		"成人视频 国产自拍合集",
		"hentai collection ep01-12",
	}
	for _, name := range cases {
		r := Check(name, mkFiles("video.mp4"), 1<<30)
		if !r.Adult {
			t.Errorf("adult name %q not flagged: %+v", name, r)
		}
		if len(r.Reasons) == 0 {
			t.Errorf("adult name %q flagged without reasons", name)
		}
	}
}

func TestAdultKeywordInFileNameBlocked(t *testing.T) {
	r := Check("some innocuous pack",
		mkFiles("readme.txt", "hidden/brazzers-scene-01.mp4"), 1<<30)
	if !r.Adult {
		t.Fatalf("adult file name not flagged: %+v", r)
	}
}

func TestAdultDomainBlocked(t *testing.T) {
	r := Check("www.leaksite.xxx full siterip", mkFiles("a.zip"), 1<<30)
	if !r.Adult {
		t.Fatalf("adult domain not flagged: %+v", r)
	}
}

func TestJavCodeAloneDoesNotFlag(t *testing.T) {
	// JAV-style code without any other adult signal must not flag.
	r := Check("ABP-123 mystery release", mkFiles("video.mp4"), 1<<30)
	if r.Adult {
		t.Fatalf("jav code alone should not flag: %+v", r)
	}
}

func TestShortTokenWordBoundary(t *testing.T) {
	// "av"/"jav" substrings inside normal words must not flag.
	for _, name := range []string{
		"Avatar 2009 Extended Collectors Edition 1080p",
		"Avengers Endgame 2019 2160p UHD BluRay",
		"Java Programming Masterclass 2024",
		"Deep Purple Live in Concert 1972",
	} {
		if r := Check(name, mkFiles("movie.mkv"), 2<<30); r.Adult || r.Spam {
			t.Fatalf("name %q wrongly flagged: %+v", name, r)
		}
	}
}

func TestXXXMovieTitleIsBlocked(t *testing.T) {
	// Documented behavior: "XXX: Return of Xander Cage" contains the token
	// "xxx" as a standalone word and is therefore (acceptably) blocked.
	r := Check("XXX: Return of Xander Cage 2017 1080p", mkFiles("xxx.mkv"), 3<<30)
	if !r.Adult {
		t.Fatalf("expected XXX title to be flagged as adult: %+v", r)
	}
}

func TestSEOStuffingBlocked(t *testing.T) {
	r := Check("movie movie movie movie download free", mkFiles("movie.mkv"), 1<<30)
	if !r.Spam {
		t.Fatalf("keyword stuffing not flagged: %+v", r)
	}
}

func TestZeroSizeBlocked(t *testing.T) {
	r := Check("Perfectly Normal Documentary 2024", mkFiles("doc.mkv"), 0)
	if !r.Spam {
		t.Fatalf("zero size not flagged: %+v", r)
	}
}

func TestOversizeBlocked(t *testing.T) {
	r := Check("Some Huge Pack", mkFiles("a.bin"), 30<<40)
	if !r.Spam {
		t.Fatalf("oversize not flagged: %+v", r)
	}
}

func TestEmptyNameBlocked(t *testing.T) {
	r := Check("   ", nil, 1<<30)
	if !r.Spam {
		t.Fatalf("empty name not flagged: %+v", r)
	}
}

func TestSymbolOnlyNameBlocked(t *testing.T) {
	r := Check("!@#$%^&*()_+-=[]{}", nil, 1<<30)
	if !r.Spam {
		t.Fatalf("symbol-only name not flagged: %+v", r)
	}
}

func TestTooManyFilesBlocked(t *testing.T) {
	files := make([]File, 20001)
	for i := range files {
		files[i] = File{Path: "f.txt", Size: 1}
	}
	r := Check("Big Archive", files, 1<<30)
	if !r.Spam {
		t.Fatalf("huge file count not flagged: %+v", r)
	}
}

func TestRandomFileNamesBlocked(t *testing.T) {
	var paths []string
	for i := 0; i < 10; i++ {
		paths = append(paths, "qwrtpsdfghjklzxcvbnm"+strings.Repeat("x", i))
	}
	r := Check("data pack", mkFiles(paths...), 1<<30)
	if !r.Spam {
		t.Fatalf("random file names not flagged: %+v", r)
	}
}

func TestLongNameBlocked(t *testing.T) {
	r := Check(strings.Repeat("a", 301), mkFiles("a.mkv"), 1<<30)
	if !r.Spam {
		t.Fatalf("overlong name not flagged: %+v", r)
	}
}

func TestReasonsRecorded(t *testing.T) {
	r := Check("porn movie movie movie movie", nil, 0)
	if !r.Adult || !r.Spam {
		t.Fatalf("expected both flags: %+v", r)
	}
	if len(r.Reasons) < 3 {
		t.Fatalf("expected multiple reasons, got %v", r.Reasons)
	}
}
