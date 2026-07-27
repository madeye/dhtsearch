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
	// --- Mined from LLM moderation removals (2026-07): terms with high
	// frequency in removed titles and zero hits in LLM-approved titles. ---
	"啪啪", "私拍", "探花", "少妇", "高潮", "寝取", "奶子", "抽插",
	"大尺度", "小穴", "嫩穴", "爆操", "约啪", "国模", "露脸", "反差婊",
	"幼女", "学生妹", "屁股", "流出", "泄密", "外围女", "后入式",
	"麻豆传媒", "天美传媒", "星空传媒", "果冻传媒", "精东影业",
	// English porn studios and networks seen in removed titles. Profanity
	// words (fuck etc.) are deliberately absent: corpus replay showed they
	// hit mainstream releases (FUCK.2006, The.End.Of.The.Fucking.World).
	"blacked", "vixen.com", "tushy", "metart", "sexart", "manyvids",
	"clips4sale",
}

// adultExceptions are known-legitimate phrases that contain an adult keyword.
// They are removed from the text before keyword matching, so 幼女戰記 (the
// anime "Saga of Tanya the Evil") does not trip the 幼女 keyword.
var adultExceptions = []string{
	"幼女戰記", "幼女战记", "幼女戦記",
}

// adultNameKeywords are matched against the torrent NAME only, never file
// paths. These are adult-forum banners and release-site tags: ad files like
// "1024草榴社區 t66y.com.txt" get bundled into perfectly legitimate uploads,
// so a file-path match would reject normal content. A banner in the display
// name, though, marks the release itself as coming from an adult site.
var adultNameKeywords = []string{
	"第一会所", "第一會所", "草榴", "t66y", "sexinsex", "sex8.cc",
	"桃花族", "thz.la", "madoubt", "kks11.cc", "bydda.cc", "u5a5.com",
	"dxxdom", "dccdom", "odnbt", "22sht.me", "julyjailbait",
}

// adultDomainRe matches common adult-site style domains appearing in names,
// e.g. "www.example.xxx", "site-name.porn", "foo.sex".
var adultDomainPattern = `(?i)\b(?:www\.)?[a-z0-9][a-z0-9-]{1,30}\.(?:xxx|porn|sex|adult|tube)\b`

// javCodePattern matches common JAV product codes like "ABP-123", "SSNI987".
// This is a weak signal: it only flags content when another adult signal
// already matched, to avoid false positives on unrelated codes.
var javCodePattern = `\b[a-zA-Z]{2,6}-?\d{3,5}\b`

// javStudioPattern matches product codes of known JAV studios, which is a
// strong signal on its own (unlike the generic shape above). Prefixes come
// from LLM moderation removals plus the major studio catalogs; all had zero
// hits in LLM-approved titles.
var javStudioPattern = `\b(?:ssis|ssni|snis|sone|snos|ipzz|ipz|ipx|mide|midv|mida|abp|abw|pred|juq|meyd|pppd|ebod|miaa|cawd|waaa|fsdss|dldss|dass)-?\d{3,5}\b`
