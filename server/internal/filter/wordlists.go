package filter

import (
	"encoding/base64"
	"strings"
)

// The keyword lists below are stored base64-encoded — one keyword per line
// inside the blob — so this source file carries no explicit terms in plain
// text (keeps the repo clean for code search, scanners and casual readers).
//
// To view or edit a list:
//
//	pbpaste | base64 -d                 # decode a blob to one keyword per line
//	base64 < keywords.txt | fold -w 76  # re-encode an edited list
//
// mustDecodeList panics on a malformed blob, which surfaces at init in every
// test run.
func mustDecodeList(blob string) []string {
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(blob), ""))
	if err != nil {
		panic("filter: bad wordlist blob: " + err.Error())
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// adultKeywords is the built-in adult keyword list (Chinese and English), in
// three sections: English explicit terms; Chinese explicit terms; and terms
// mined from LLM moderation removals (2026-07) that had high frequency in
// removed titles and zero hits in LLM-approved titles, including CN/EN adult
// studios and networks. Profanity words are deliberately absent: corpus
// replay showed they hit mainstream releases (a 2006 documentary, Netflix
// series titles).
//
// Matching is case-insensitive. Short pure-ASCII tokens (<= 4 chars) are
// matched on word boundaries to avoid false positives such as "Avatar" or
// "Java". Longer tokens and CJK tokens use substring match.
var adultKeywords = mustDecodeList(`
cG9ybgpwb3Jubwpwb3Jub2dyYXBoeQpwb3JuaHViCnh2aWRlb3MKeHZpZGVvCnhueHgKeGhhbXN0
ZXIKcmVkdHViZQp5b3Vwb3JuCnlvdWppenoKdHViZTgKc3BhbmtiYW5nCmJyYXp6ZXJzCm5hdWdo
dHlhbWVyaWNhCnJlYWxpdHlraW5ncwpiYW5nYnJvcwptb2Zvcwpvbmx5ZmFucwpjaGF0dXJiYXRl
CmNhbWdpcmwKY2FtNApsaXZlamFzbWluCnN0cmlwY2hhdApib25nYWNhbXMKbXlmcmVlY2Ftcwpo
ZW50YWkKZWNjaGkKZnV0YW5hcmkKc2hvdGFjb24KbG9saWNvbgpydWxlMzQKcjM0CmRvdWppbjE4
Cm5oZW50YWkKaGFuaW1lCnh4eApzZXgKc2V4eQpzZXh1YWwKc2V4dGFwZQpjdW1zaG90CmNyZWFt
cGllCmJsb3dqb2IKaGFuZGpvYgpmb290am9iCnRpdGpvYgpidWtrYWtlCmdhbmdiYW5nCm9yZ3kK
dGhyZWVzb21lCmZvdXJzb21lCmFuYWwKZHAKYmRzbQpib25kYWdlCmZlbWRvbQpkb21pbmF0cml4
Cm1pbGYKZ2lsZgpkaWxmCmNvdWdhcgpiYncKbnVkZQpudWRlcwpuYWtlZAp0b3BsZXNzCnVwc2tp
cnQKZG93bmJsb3VzZQpuaXBwbGUKYm9vYnMKdGl0cwpwdXNzeQp2YWdpbmEKY2xpdG9yaXMKZGls
ZG8KdmlicmF0b3IKZmxlc2hsaWdodAplcm90aWMKZXJvdGljYQpzZW5zdWFsCmx1c3QKaG9ybnkK
c2x1dAp3aG9yZQpob29rZXIKZXNjb3J0CmJyb3RoZWwKc3RyaXBwZXIKc3RyaXB0ZWFzZQpsYXBk
YW5jZQpwZWVwCnZveWV1cgpleGhpYml0aW9uaXN0CmZldGlzaApraW5reQpoYXJkY29yZQpzb2Z0
Y29yZQp4eHh2aWRlbwphZHVsdHZpZGVvCjE4cGx1cwoxOG9ubHkKbnNmdwpmYXAKZmFwcGluZwpq
ZXJrIG9mZgpoYW5kIGpvYgpibG93IGpvYgpkZWVwdGhyb2F0CmZhY2lhbApzd2FsbG93CmluY2Vz
dAp0YWJvbwpzdGVwbW9tCnN0ZXBzaXN0ZXIKc3RlcGJyb3RoZXIKc3RlcGRhdWdodGVyCnRlZW5z
ZXgKdmlyZ2luc2V4CmRlZmxvcmF0aW9uCnNxdWlydApzcXVpcnRpbmcKcGVnZ2luZwpyaW1qb2IK
YW5hbGluZ3VzCmN1bm5pbGluZ3VzCmZlbGxhdGlvCm1hc3R1cmJhdGlvbgpvcmdhc20KcGVuZXRy
YXRpb24KcG9ybnN0YXIKc2V4c2NlbmUKbnVkZXNjZQpwbGF5Ym95CnBlbnRob3VzZQpodXN0bGVy
CnNtdXQKbGV3ZApvYnNjZW5lCmFkdWx0Cnh4eHBvcm4KZnJlZXBvcm4KaGRwb3JuCjRrcG9ybgpw
b3JudmlkZW8Kc2V4dmlkZW8Kc2V4ZmlsbQpzZXh0dWJlCnBvcm50dWJlCmFkdWx0ZmlsbQphZHVs
dGR2ZApqYXZoZApqYXZsaWJyYXJ5CmZjMgpjYXJpYmJlYW5jb20KMXBvbmRvCmhleXpvCnRva3lv
aG90CjEwbXVzdW1lCnBhY29wYWNvbWFtYQp1bmNlbnNvcmVkCmNlbnNvCm1vc2FpYwpncmF2dXJl
CmNoaWthbgrmiJDkuroK6Imy5oOFCuaDheiJsgrkuInnuqfniYcK5LiJ57Sa54mHCuavm+eJhwrp
u4TniYcK6buD54mHCkHniYcKQVblpbPkvJgK5aWz5LyYCuWls+WEqgrnlarlj7cK55Wq6JmfCuaX
oOeggQrnhKHnorwK5pyJ56CBCuaXoOS/ruatowrnhKHkv67mraMK5Lit5paH5a2X5bmV5peg56CB
CuatpeWFtQrpqpHlhbUK57Sg5Lq6CuS6uuWmuwrnhp/lpbMK6JCd6I6JCuW+oeWnkArlt6jkubMK
576O5LmzCueIhuS5swrkuJ3oopwK5Yi25pyN6K+x5oORCuS5seS8pgrlvLrlpbgK6L+35aW4Cui9
ruWluArnvqTkuqQK5Y+j5LqkCuiCm+S6pArlhoXlsIQK5Lit5Ye6CuminOWwhArmva7lkLkK6Ieq
5oWwCuaJi+a3qwrlgZrniLEK5oCn5LqkCuaAp+eIsQrmgKfkuqTop4bpopEK6KO46IGKCuijuOeF
pwroo7jkvZMK5YWo6KO4Cui1sOWFiQrlgbfmi40K5ZK45rm/Cua3q+iNoQrmt6vkubEK5rer5aiD
CumjjumqmgrpqprotKcK6I2h5aaHCuWmk+Wlswrlq5blppMK5o+05LqkCuWMheWFuwrkuIDlpJzm
g4UK57qm54KuCue6pnAK5omT54KuCuW8gOaIvwrmgKfniLHop4bpopEK5oiQ5Lq66KeG6aKRCuaI
kOS6uueUteW9sQrmiJDkurrlvbHniYcK5oiQ5Lq6572R56uZCum7hOiJsue9keermQrmiJDkurro
rrrlnZsK5oiQ5Lq65bCP6K+0CuaIkOS6uua8q+eUuwrmiJDkurrliqjmvKsK6YeM55WqCuiCieeV
qgrmnKzlrZAK5ZCM5Lq65b+XMTgK5Y2B5YWr56aBCjE456aBCumZkOWItue6pwrmnKrmu6ExOArm
t7HlpJzlnLoK5Lym55CG54mHCuiJs+eFpwrpl6jkuovku7YK6ZmI5Yag5biMCuaAp+aEn+WGmeec
nwrlhpnnnJ/op4bpopEK56eB5oi/54WnCuemj+WIqeWnrArnpo/liKnop4bpopEK5aWz5Li75pKt
6KeG6aKRCuaKlumYtArlv6vnjKsK54yr5ZKq6KeG6aKRCuS4neeTnOinhumikQrojYnojpPop4bp
opEK6aaZ6JWJ6KeG6aKRCuicnOahg+inhumikQrojITlrZDop4bpopEK6buE55Oc6KeG6aKRCuWv
jOWphgrmr43ni5cK6LCD5pWZCuaAp+WltAromZDlvoUK5YeM6L6xClNN6KeG6aKRCumHjeWPo+WR
swrnjI7lpYcK6Kem5omLCuW8guenjeWluArnlLXovabnl7TmsYkK55e05rGJCumcsuWHugrph47l
pJYK5YG35ouN6Ieq5ouNCuWbveS6p+iHquaLjQroh6rmi43op4bpopEK6YWS5bqX5YG35ouNCuaD
heS+o+iHquaLjQrllarllaoK56eB5ouNCuaOouiKsQrlsJHlpocK6auY5r2uCuWvneWPlgrlpbbl
rZAK5oq95o+SCuWkp+WwuuW6pgrlsI/nqbQK5aup56m0CueIhuaTjQrnuqbllaoK5Zu95qihCumc
suiEuArlj43lt67lqYoK5bm85aWzCuWtpueUn+WmuQrlsYHogqEK5rWB5Ye6CuazhOWvhgrlpJbl
m7TlpbMK5ZCO5YWl5byPCum6u+ixhuS8oOWqkgrlpKnnvo7kvKDlqpIK5pif56m65Lyg5aqSCuae
nOWGu+S8oOWqkgrnsr7kuJzlvbHkuJoKYmxhY2tlZAp2aXhlbi5jb20KdHVzaHkKbWV0YXJ0CnNl
eGFydAptYW55dmlkcwpjbGlwczRzYWxl
`)

// adultExceptions are known-legitimate phrases that contain an adult keyword.
// They are removed from the text before keyword matching. Currently: the
// three script variants of the anime "Saga of Tanya the Evil", whose Japanese
// title contains a keyword.
var adultExceptions = mustDecodeList(`
5bm85aWz5oiw6KiYCuW5vOWls+aImOiusArlubzlpbPmiKboqJg=
`)

// adultNameKeywords are matched against the torrent NAME only, never file
// paths. These are adult-forum banners and release-site tags: ad files
// carrying the same banners get bundled into perfectly legitimate uploads,
// so a file-path match would reject normal content. A banner in the display
// name, though, marks the release itself as coming from an adult site.
var adultNameKeywords = mustDecodeList(`
56ys5LiA5Lya5omACuesrOS4gOacg+aJgArojYnmprQKdDY2eQpzZXhpbnNleApzZXg4LmNjCuah
g+iKseaXjwp0aHoubGEKbWFkb3VidApra3MxMS5jYwpieWRkYS5jYwp1NWE1LmNvbQpkeHhkb20K
ZGNjZG9tCm9kbmJ0CjIyc2h0Lm1lCmp1bHlqYWlsYmFpdA==
`)

// adultDomainRe matches common adult-site style domains appearing in names,
// e.g. "www.example.xxx".
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
