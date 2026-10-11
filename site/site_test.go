package site

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParse(t *testing.T) {
	for name, want := range map[string]Site{"": CN, "cn": CN, "rednote": Rednote, "intl": Rednote, "INTL": Rednote} {
		got, err := Parse(name)
		assert.NoError(t, err, name)
		assert.Equal(t, want, got, name)
	}
	_, err := Parse("xhs")
	assert.Error(t, err, "不认识的站点不能静默回落到中文站")
}

func TestURLs(t *testing.T) {
	assert.Equal(t, "https://www.rednote.com/explore", Rednote.WebURL("/explore"))
	assert.Equal(t, "https://creator.xiaohongshu.com/publish/publish", CN.CreatorURL("/publish/publish"))
}

func TestOwns(t *testing.T) {
	assert.True(t, Rednote.Owns("rednote.com"))
	assert.True(t, Rednote.Owns("edith.rednote.com"))
	assert.False(t, Rednote.Owns("edith.xiaohongshu.com"), "另一个站点的接口")
	assert.False(t, Rednote.Owns("evilrednote.com"), "只认子域名，不认后缀相同的别的域名")
	assert.True(t, CN.Owns("creator.xiaohongshu.com"))
}

func TestSameSite(t *testing.T) {
	assert.True(t, SameSite("rednote", "intl"), "老会话文件记的是 rednote，新的记 intl")
	assert.True(t, SameSite("", "cn"))
	assert.False(t, SameSite("cn", "intl"))
	assert.False(t, SameSite("xhs", "xhs"), "不认识的名字不算同一个站点")
}
