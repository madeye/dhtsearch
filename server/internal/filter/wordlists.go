package filter

// adultKeywords is the built-in adult keyword list (Chinese and English).
// Matching is case-insensitive. Short pure-ASCII tokens (<= 4 chars, e.g.
// "av", "xxx") are matched on word boundaries to avoid false positives such
// as "Avatar" or "Java". Longer tokens and CJK tokens use substring match.
var adultKeywords = []string{
	// --- English: explicit terms ---
	"porn", "porno", "pornography", "pornhub", "xvideos", "xvideo", "xnxx",
	"xhamster", "redtube", "youporn", "youjizz", "tube8", "spankbang",
	"brazzers", "naughtyamerica", "realitykings", "bangbros", "mofos",
	"onlyfans", "chaturbate", "camgirl", "cam4", "livejasmin", "stripchat",
	"bongacams", "myfreecams", "hentai", "ecchi", "futanari", "shotacon",
	"lolicon", "rule34", "r34", "doujin18", "nhentai", "hanime",
	"xxx", "sex", "sexy", "sexual", "sextape", "cumshot", "creampie",
	"blowjob", "handjob", "footjob", "titjob", "bukkake", "gangbang",
	"orgy", "threesome", "foursome", "anal", "dp", "bdsm", "bondage",
	"femdom", "dominatrix", "milf", "gilf", "dilf", "cougar", "bbw",
	"nude", "nudes", "naked", "topless", "upskirt", "downblouse",
	"nipple", "boobs", "tits", "pussy", "vagina", "clitoris", "dildo",
	"vibrator", "fleshlight", "erotic", "erotica", "sensual", "lust",
	"horny", "slut", "whore", "hooker", "escort", "brothel", "stripper",
	"striptease", "lapdance", "peep", "voyeur", "exhibitionist",
	"fetish", "kinky", "hardcore", "softcore", "xxxvideo", "adultvideo",
	"18plus", "18only", "nsfw", "fap", "fapping", "jerk off", "hand job",
	"blow job", "deepthroat", "facial", "swallow", "incest", "taboo",
	"stepmom", "stepsister", "stepbrother", "stepdaughter", "teensex",
	"virginsex", "defloration", "squirt", "squirting", "pegging",
	"rimjob", "analingus", "cunnilingus", "fellatio", "masturbation",
	"orgasm", "penetration", "pornstar", "sexscene", "nudesce",
	"playboy", "penthouse", "hustler", "smut", "lewd", "obscene",
	"adult", "xxxporn", "freeporn", "hdporn", "4kporn", "pornvideo",
	"sexvideo", "sexfilm", "sextube", "porntube", "adultfilm",
	"adultdvd", "javhd", "javlibrary", "fc2", "caribbeancom",
	"1pondo", "heyzo", "tokyohot", "10musume", "pacopacomama",
	"uncensored", "censo", "mosaic", "gravure", "chikan",
	// --- Chinese: explicit terms ---
	"成人", "色情", "情色", "三级片", "三級片", "毛片", "黄片", "黃片", "A片", "AV女优",
	"女优", "女優", "番号", "番號", "无码", "無碼", "有码", "无修正", "無修正",
	"中文字幕无码", "步兵", "骑兵",
	"素人", "人妻", "熟女", "萝莉", "御姐", "巨乳", "美乳", "爆乳",
	"丝袜", "制服诱惑", "乱伦", "强奸", "迷奸", "轮奸", "群交", "口交",
	"肛交", "内射", "中出", "颜射", "潮吹", "自慰", "手淫", "做爱",
	"性交", "性爱", "性交视频", "裸聊", "裸照", "裸体", "全裸", "走光",
	"偷拍", "咸湿", "淫荡", "淫乱", "淫娃", "风骚", "骚货", "荡妇",
	"妓女", "嫖妓", "援交", "包养", "一夜情", "约炮", "约p", "打炮",
	"开房", "性爱视频", "成人视频", "成人电影", "成人影片", "成人网站",
	"黄色网站", "成人论坛", "成人小说", "成人漫画", "成人动漫",
	"里番", "肉番", "本子", "同人志18", "十八禁", "18禁", "限制级",
	"未满18", "深夜场", "伦理片", "艳照", "门事件", "陈冠希",
	"性感写真", "写真视频", "私房照", "福利姬", "福利视频",
	"女主播视频", "抖阴", "快猫", "猫咪视频", "丝瓜视频", "草莓视频",
	"香蕉视频", "蜜桃视频", "茄子视频", "黄瓜视频", "富婆",
	"母狗", "调教", "性奴", "虐待", "凌辱", "SM视频", "重口味",
	"猎奇", "触手", "异种奸", "电车痴汉", "痴汉", "露出", "野外",
	"偷拍自拍", "国产自拍", "自拍视频", "酒店偷拍", "情侣自拍",
}

// adultDomainRe matches common adult-site style domains appearing in names,
// e.g. "www.example.xxx", "site-name.porn", "foo.sex".
var adultDomainPattern = `(?i)\b(?:www\.)?[a-z0-9][a-z0-9-]{1,30}\.(?:xxx|porn|sex|adult|tube)\b`

// javCodePattern matches common JAV product codes like "ABP-123", "SSNI987".
// This is a weak signal: it only flags content when another adult signal
// already matched, to avoid false positives on unrelated codes.
var javCodePattern = `\b[a-zA-Z]{2,6}-?\d{3,5}\b`
