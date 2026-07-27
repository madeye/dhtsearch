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
	// A generic release-code shape without any other adult signal must not
	// flag. (Codes of known JAV studios, e.g. ABP-123, are a separate strong
	// signal — see TestJavStudioCodeBlocked.)
	r := Check("GTX-4090 benchmark suite", mkFiles("video.mp4"), 1<<30)
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

func TestSizeFloor(t *testing.T) {
	const mb = 1 << 20
	cases := []struct {
		size int64
		want bool
	}{
		{50 * mb, true},   // below the 100 MiB floor
		{99 * mb, true},   //
		{100 * mb, false}, // exactly at the floor is kept
		{700 * mb, false},
	}
	for _, tc := range cases {
		r := Check("Big Buck Bunny 2008 1080p", mkFiles("bbb.mkv"), tc.size)
		if r.TooSmall != tc.want {
			t.Errorf("size %d: TooSmall=%v, want %v (%v)", tc.size, r.TooSmall, tc.want, r.Reasons)
		}
		// The size floor must not be conflated with spam.
		if r.Spam {
			t.Errorf("size %d: wrongly flagged as spam: %v", tc.size, r.Reasons)
		}
	}
}

func TestSizeFloorIsConfigurable(t *testing.T) {
	old := MinTotalSize
	defer func() { MinTotalSize = old }()
	MinTotalSize = 1 << 30
	if r := Check("Movie 1080p", mkFiles("m.mkv"), 700<<20); !r.TooSmall {
		t.Errorf("700MB should be too small when floor is 1GB: %+v", r)
	}
}

func TestRejectedReportsAnySignal(t *testing.T) {
	if (Result{}).Rejected() {
		t.Error("empty result must not be rejected")
	}
	for _, r := range []Result{{Adult: true}, {Spam: true}, {TooSmall: true}} {
		if !r.Rejected() {
			t.Errorf("%+v should be rejected", r)
		}
	}
}

func TestMinedAdultKeywordsBlocked(t *testing.T) {
	cases := []string{
		// The listing that motivated the mined batch: suggestive prose with
		// no wordlist term until 屁股 was added.
		"可爱学妹小狗〖软萌兔兔酱〗小草神女仆，QQ弹弹的小屁股，bb又很紧温润。做起来很舒服",
		"第一會所新片@SIS001@最新合集",
		"高颜值探花小姐姐酒店私拍",
		"麻豆传媒 MDX-0021",
	}
	for _, name := range cases {
		if r := Check(name, mkFiles("video.mp4"), 1<<30); !r.Adult {
			t.Errorf("adult name %q not flagged: %+v", name, r)
		}
	}
}

func TestJavStudioCodeBlocked(t *testing.T) {
	for _, name := range []string{"IPZZ-586 1080p", "SSNI987", "midv-192 中文字幕"} {
		if r := Check(name, mkFiles("video.mp4"), 1<<30); !r.Adult {
			t.Errorf("jav studio code %q not flagged: %+v", name, r)
		}
	}
	// The generic code shape alone must stay a weak signal.
	if r := Check("MTLS-001 conference recordings", mkFiles("talk.mp4"), 1<<30); r.Adult {
		t.Errorf("generic code wrongly flagged: %+v", r)
	}
}

func TestBannerInNameOnlyNotFiles(t *testing.T) {
	// A forum banner in the display name marks the release as adult.
	if r := Check("1024草榴社區 t66y 合集", mkFiles("video.mp4"), 1<<30); !r.Adult {
		t.Errorf("banner in name not flagged: %+v", r)
	}
	// The same banner inside a bundled ad file must not reject the torrent:
	// these ads ride along with perfectly legitimate uploads.
	r := Check("流浪地球2 2023 1080p WEB-DL",
		mkFiles("movie.mkv", "1024草榴社區 t66y.com.txt", "久度空间~BT分享平台~免註冊下載.url"), 4<<30)
	if r.Adult || r.Spam {
		t.Errorf("legit movie with bundled ad files flagged: %+v", r)
	}
}

func TestMainstreamProfanityAndExceptionsPass(t *testing.T) {
	for _, name := range []string{
		"FUCK.2006.720p.x264-DON",
		"The.End.Of.The.Fucking.World.Season.01.1080p",
		"[ANi] 幼女戰記 2 - 01 [1080P][Baha][WEB-DL].mp4",
		"幼女战记 剧场版 2019 BDRip",
	} {
		if r := Check(name, mkFiles("video.mkv"), 2<<30); r.Adult {
			t.Errorf("mainstream release %q wrongly flagged: %+v", name, r)
		}
	}
	// Standalone 幼女 outside the anime title must still be flagged.
	if r := Check("幼女 合集", mkFiles("v.mp4"), 1<<30); !r.Adult {
		t.Errorf("standalone 幼女 not flagged: %+v", r)
	}
}
