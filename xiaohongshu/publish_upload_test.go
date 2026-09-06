package xiaohongshu

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAcceptsImage(t *testing.T) {
	cases := []struct {
		name   string
		accept string
		want   bool
	}{
		{"图片扩展名", ".png,.webp", true},
		{"大写扩展名", ".JPG", true},
		{"MIME 通配", "image/*", true},
		{"非图片扩展名", ".xyz,.abc", false},
		{"非图片 MIME", "video/mp4", false},
		{"空值", "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, acceptsImage(c.accept))
		})
	}
}

func TestTabAcceptMatches(t *testing.T) {
	assert.True(t, tabAcceptMatches("上传图文", ".jpg,.jpeg,.png,.webp"))
	assert.False(t, tabAcceptMatches("上传图文", ".mp4,.mov"))
	assert.True(t, tabAcceptMatches("上传视频", ".mp4,.mov"))
	assert.False(t, tabAcceptMatches("上传视频", ".jpg,.png"))
}

func TestValidateOriginalEnabled(t *testing.T) {
	t.Run("没有确认弹窗但开关已开启", func(t *testing.T) {
		assert.NoError(t, validateOriginalEnabled(false, true))
	})

	t.Run("确认弹窗完成且开关已开启", func(t *testing.T) {
		assert.NoError(t, validateOriginalEnabled(true, true))
	})

	t.Run("没有确认弹窗且开关未开启时中止", func(t *testing.T) {
		err := validateOriginalEnabled(false, false)
		assert.ErrorContains(t, err, "开关未开启")
	})

	t.Run("确认后开关仍未开启时中止", func(t *testing.T) {
		err := validateOriginalEnabled(true, false)
		assert.ErrorContains(t, err, "开关仍未开启")
	})
}
