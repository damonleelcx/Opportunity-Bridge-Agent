package leadgraph

import "testing"

// standsAlone is the check that catches a reading that dropped a character from
// a name. A substring check passes that exact error. See standsAlone.
func TestANameMustStandAloneInItsTopic(t *testing.T) {
	cases := []struct {
		name, text string
		want       bool
		why        string
	}{
		{"罗子", "45 罗子衿 （杭州）设计中心负责人", false, "the reading dropped 衿 (production path, 2026-09-11)"},
		{"罗子衿", "45 罗子衿 （杭州）设计中心负责人", true, "the real name"},
		{"林默然", "林默然，设计总监、视觉、包装（已离职去Brightly）", true, "punctuation after"},
		{"程昱", "39 创新设计部 负责人 程昱，ID设计 美图", true, "name mid-line"},
		{"陆行舟", "36 陆行舟（25年2月进的） 资深设计专家", true, "bracket after"},
		{"Leo Chen", "Leo Chen UED 用户体验设计负责人", true, "a Latin name with a space"},
		{"Marcus", "Creative Team Leader Marcus", true, "at the end"},
		{"Ann", "Anna ux 华为", false, "a Latin name that continues"},
		{"Ivan", "33 Ivan 48w 15 ai平板", true, "digits either side do not continue a name"},
		{"冯时雨", "19 Audio 首席体验官 冯时雨", true, "Han name after a Latin word"},
		{"罗子", "罗子衿 和 罗子 是两个人", true, "a later occurrence stands alone"},
		{"张三", "张三负责设计", false, "written into the sentence: flagged, which costs a second look"},
		{"", "韩子墨 高级视觉", false, "no name at all"},
	}
	for _, c := range cases {
		if got := standsAlone(c.name, c.text); got != c.want {
			t.Errorf("standsAlone(%q, %q) = %v, want %v: %s", c.name, c.text, got, c.want, c.why)
		}
	}
}
