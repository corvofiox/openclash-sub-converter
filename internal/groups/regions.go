package groups

// regionInfo 描述一个地区的规范标识与五层识别别名。
//
// emoji/iso/region 为规范字段（组名与输出用）；zh/py/en 为识别别名，
// 城市别名并入对应语言层（中文城市→zh、英文城市→en，拼音城市不收）。
// iso 与 emoji 码位一一对应（一致性由 TestRegionTablesValid 强制）。
type regionInfo struct {
	emoji  string   // 规范 emoji（组名用）
	iso    string   // 大写双字母（即国旗码位推导值，如 🇭🇰→HK）
	region string   // 地区中文名（组名用，规范名）
	zh     []string // 中文别名（简体+繁体+常用异名+城市，如 澳大利亚/澳洲/澳大利亞）
	py     []string // 拼音别名（小写无调号全拼，如 xianggang/aomen）
	en     []string // 英文别名（小写；含多词短语、习惯写法与城市名）
}

// 层序常量：显式度（可信度）从高到低，仅作同位置平局裁决（实际几乎不触发）。
const (
	layerEmoji = 1
	layerZH    = 2
	layerPY    = 3
	layerEN    = 4
	layerISO   = 5
)

// isoDenyNo 是 ISO 层 denylist 的唯一项："no" 是常见编号前缀（No.1/No.01），
// 误判为挪威代价高，故不参与 ISO 匹配；挪威仍可由 norway/挪威 命中。
const isoDenyNo = "no"

// regionInfos 是 47 地区五层别名数据表（有序切片保证确定性；同名字段优先级
// 与输出顺序都依赖此顺序）。别名含地区名与无歧义城市名——一个别名只映射一个
// 地区，全局唯一性由 TestRegionTablesValid 强制。
var regionInfos = []regionInfo{
	{emoji: "🇭🇰", iso: "HK", region: "香港", zh: []string{"香港"}, py: []string{"xianggang"}, en: []string{"hong kong", "hongkong"}},
	{emoji: "🇲🇴", iso: "MO", region: "澳门", zh: []string{"澳门", "澳門"}, py: []string{"aomen"}, en: []string{"macau", "macao"}},
	{emoji: "🇹🇼", iso: "TW", region: "台湾", zh: []string{"台湾", "臺灣", "台北", "臺北"}, py: []string{"taiwan"}, en: []string{"taiwan", "taipei"}},
	{emoji: "🇯🇵", iso: "JP", region: "日本", zh: []string{"日本", "东京", "東京", "大阪", "名古屋"}, py: []string{"riben"}, en: []string{"japan", "tokyo", "osaka", "nagoya"}},
	{emoji: "🇰🇷", iso: "KR", region: "韩国", zh: []string{"韩国", "韓國", "首尔", "首爾", "釜山"}, py: []string{"hanguo"}, en: []string{"korea", "south korea", "seoul", "busan"}},
	{emoji: "🇸🇬", iso: "SG", region: "新加坡", zh: []string{"新加坡"}, py: []string{"xinjiapo"}, en: []string{"singapore"}},
	{emoji: "🇲🇾", iso: "MY", region: "马来西亚", zh: []string{"马来西亚", "馬來西亞", "吉隆坡"}, py: []string{"malaixiya"}, en: []string{"malaysia", "kuala lumpur", "kualalumpur"}},
	{emoji: "🇹🇭", iso: "TH", region: "泰国", zh: []string{"泰国", "泰國", "曼谷"}, py: []string{"taiguo"}, en: []string{"thailand", "thai", "bangkok"}},
	{emoji: "🇻🇳", iso: "VN", region: "越南", zh: []string{"越南", "河内", "河內", "胡志明"}, py: []string{"yuenan"}, en: []string{"vietnam", "hanoi", "ho chi minh", "hochiminh"}},
	{emoji: "🇮🇩", iso: "ID", region: "印尼", zh: []string{"印尼", "印度尼西亚", "印度尼西亞", "雅加达", "雅加達"}, py: []string{"yinni"}, en: []string{"indonesia", "jakarta"}},
	{emoji: "🇵🇭", iso: "PH", region: "菲律宾", zh: []string{"菲律宾", "菲律賓", "马尼拉", "馬尼拉"}, py: []string{"feilvbin"}, en: []string{"philippines", "manila"}},
	{emoji: "🇮🇳", iso: "IN", region: "印度", zh: []string{"印度", "孟买", "孟買", "德里"}, py: []string{"yindu"}, en: []string{"india", "mumbai", "delhi"}},
	{emoji: "🇺🇸", iso: "US", region: "美国", zh: []string{"美国", "美國", "纽约", "紐約", "洛杉矶", "洛杉磯", "旧金山", "三藩市", "西雅图", "芝加哥", "拉斯维加斯", "达拉斯", "休斯顿", "休斯頓"}, py: []string{"meiguo"}, en: []string{"usa", "united states", "america", "new york", "nyc", "los angeles", "san francisco", "seattle", "chicago", "las vegas", "dallas", "houston"}},
	{emoji: "🇨🇦", iso: "CA", region: "加拿大", zh: []string{"加拿大", "多伦多", "多倫多", "温哥华", "溫哥華", "蒙特利尔", "蒙特利爾"}, py: []string{"jianada"}, en: []string{"canada", "toronto", "vancouver", "montreal"}},
	{emoji: "🇲🇽", iso: "MX", region: "墨西哥", zh: []string{"墨西哥"}, py: []string{"moxige"}, en: []string{"mexico"}},
	{emoji: "🇧🇷", iso: "BR", region: "巴西", zh: []string{"巴西"}, py: []string{"baxi"}, en: []string{"brazil"}},
	{emoji: "🇦🇺", iso: "AU", region: "澳大利亚", zh: []string{"澳大利亚", "澳洲", "澳大利亞", "悉尼", "雪梨", "墨尔本", "墨爾本", "珀斯"}, py: []string{"aodaliya"}, en: []string{"australia", "sydney", "melbourne", "perth"}},
	{emoji: "🇳🇿", iso: "NZ", region: "新西兰", zh: []string{"新西兰", "紐西蘭"}, py: []string{"xinxilan"}, en: []string{"new zealand"}},
	{emoji: "🇬🇧", iso: "GB", region: "英国", zh: []string{"英国", "英國", "伦敦", "倫敦", "曼彻斯特", "曼徹斯特"}, py: []string{"yinguo"}, en: []string{"uk", "united kingdom", "london", "manchester"}},
	{emoji: "🇫🇷", iso: "FR", region: "法国", zh: []string{"法国", "法國", "巴黎"}, py: []string{"faguo"}, en: []string{"france", "paris"}},
	{emoji: "🇩🇪", iso: "DE", region: "德国", zh: []string{"德国", "德國", "法兰克福", "法蘭克福", "柏林", "慕尼黑"}, py: []string{"deguo"}, en: []string{"germany", "frankfurt", "berlin", "munich"}},
	{emoji: "🇳🇱", iso: "NL", region: "荷兰", zh: []string{"荷兰", "荷蘭", "阿姆斯特丹"}, py: []string{"helan"}, en: []string{"netherlands", "holland", "amsterdam"}},
	{emoji: "🇸🇪", iso: "SE", region: "瑞典", zh: []string{"瑞典"}, py: []string{"ruidian"}, en: []string{"sweden"}},
	{emoji: "🇫🇮", iso: "FI", region: "芬兰", zh: []string{"芬兰", "芬蘭"}, py: []string{"fenlan"}, en: []string{"finland"}},
	{emoji: "🇳🇴", iso: "NO", region: "挪威", zh: []string{"挪威"}, py: []string{"nuowei"}, en: []string{"norway"}},
	{emoji: "🇩🇰", iso: "DK", region: "丹麦", zh: []string{"丹麦", "丹麥"}, py: []string{"danmai"}, en: []string{"denmark"}},
	{emoji: "🇮🇹", iso: "IT", region: "意大利", zh: []string{"意大利", "義大利", "米兰", "米蘭", "罗马", "羅馬"}, py: []string{"yidali"}, en: []string{"italy", "milan", "rome"}},
	{emoji: "🇪🇸", iso: "ES", region: "西班牙", zh: []string{"西班牙", "马德里", "馬德里", "巴塞罗那", "巴塞隆納"}, py: []string{"xibanya"}, en: []string{"spain", "madrid", "barcelona"}},
	{emoji: "🇵🇹", iso: "PT", region: "葡萄牙", zh: []string{"葡萄牙"}, py: []string{"putaoya"}, en: []string{"portugal"}},
	{emoji: "🇹🇷", iso: "TR", region: "土耳其", zh: []string{"土耳其", "伊斯坦布尔", "伊斯坦布爾"}, py: []string{"tuerqi"}, en: []string{"turkey", "istanbul"}},
	{emoji: "🇷🇺", iso: "RU", region: "俄罗斯", zh: []string{"俄罗斯", "俄羅斯", "莫斯科"}, py: []string{"eluosi"}, en: []string{"russia", "moscow"}},
	{emoji: "🇦🇪", iso: "AE", region: "阿联酋", zh: []string{"阿联酋", "阿聯酋", "迪拜"}, py: []string{"alianqiu"}, en: []string{"uae", "emirates", "united arab emirates", "dubai"}},
	{emoji: "🇮🇱", iso: "IL", region: "以色列", zh: []string{"以色列"}, py: []string{"yiselie"}, en: []string{"israel"}},
	{emoji: "🇿🇦", iso: "ZA", region: "南非", zh: []string{"南非"}, py: []string{"nanfei"}, en: []string{"south africa"}},
	{emoji: "🇪🇬", iso: "EG", region: "埃及", zh: []string{"埃及"}, py: []string{"aiji"}, en: []string{"egypt"}},
	{emoji: "🇦🇷", iso: "AR", region: "阿根廷", zh: []string{"阿根廷"}, py: []string{"agenting"}, en: []string{"argentina"}},
	{emoji: "🇨🇱", iso: "CL", region: "智利", zh: []string{"智利"}, py: []string{"zhili"}, en: []string{"chile"}},
	{emoji: "🇵🇱", iso: "PL", region: "波兰", zh: []string{"波兰", "波蘭"}, py: []string{"bolan"}, en: []string{"poland"}},
	{emoji: "🇨🇭", iso: "CH", region: "瑞士", zh: []string{"瑞士", "苏黎世", "蘇黎世", "日内瓦", "日內瓦"}, py: []string{"ruishi"}, en: []string{"switzerland", "swiss", "zurich", "geneva"}},
	{emoji: "🇦🇹", iso: "AT", region: "奥地利", zh: []string{"奥地利", "奧地利"}, py: []string{"aodili"}, en: []string{"austria"}},
	{emoji: "🇧🇪", iso: "BE", region: "比利时", zh: []string{"比利时", "比利時"}, py: []string{"bilishi"}, en: []string{"belgium"}},
	{emoji: "🇨🇿", iso: "CZ", region: "捷克", zh: []string{"捷克"}, py: []string{"jieke"}, en: []string{"czech", "czechia"}},
	{emoji: "🇬🇷", iso: "GR", region: "希腊", zh: []string{"希腊", "希臘"}, py: []string{"xila"}, en: []string{"greece"}},
	{emoji: "🇭🇺", iso: "HU", region: "匈牙利", zh: []string{"匈牙利"}, py: []string{"xiongyali"}, en: []string{"hungary"}},
	{emoji: "🇮🇪", iso: "IE", region: "爱尔兰", zh: []string{"爱尔兰", "愛爾蘭"}, py: []string{"aierlan"}, en: []string{"ireland"}},
	{emoji: "🇱🇺", iso: "LU", region: "卢森堡", zh: []string{"卢森堡", "盧森堡"}, py: []string{"lusenbao"}, en: []string{"luxembourg"}},
	{emoji: "🇺🇦", iso: "UA", region: "乌克兰", zh: []string{"乌克兰", "烏克蘭"}, py: []string{"wukelan"}, en: []string{"ukraine"}},
}

// better 返回新命中是否更优：位置更靠前胜；同位置层序更小（显式度更高）胜；
// 同位置同层别名更长者胜（更具体者优先，如 印度尼西亚 > 印尼，与表序无关）。
// 三者全平局返回 false（保持先遍历者胜，行为确定）。
func better(idx, layer, aliasLen, bestIdx, bestLayer, bestAliasLen int) bool {
	if bestIdx < 0 || idx < bestIdx {
		return true
	}
	if idx > bestIdx {
		return false
	}
	if layer != bestLayer {
		return layer < bestLayer
	}
	return aliasLen > bestAliasLen
}

// tokenIndex 返回 sub 在 s 中第一次"独立成词"的字节位置；命中串前后必须是边界
// 字符（非 [a-z] 或串首尾）。s 须为小写化串——ASCII 小写等长，字节索引与原名
// 一致，故 tokenIndex 返回的位置可与其他层（emoji/中文）跨层比较。
func tokenIndex(s, sub string) int {
	n, m := len(s), len(sub)
	for i := 0; i+m <= n; i++ {
		if s[i:i+m] != sub {
			continue
		}
		if (i == 0 || !isAsciiLetter(s[i-1])) && (i+m == n || !isAsciiLetter(s[i+m])) {
			return i
		}
	}
	return -1
}

// isAsciiLetter 判断字节是否为小写 ASCII 字母（调用前须已小写化）。
func isAsciiLetter(b byte) bool { return b >= 'a' && b <= 'z' }
