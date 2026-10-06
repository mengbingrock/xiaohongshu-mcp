package xiaohongshu

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPageAccessErr(t *testing.T) {
	code := func(a PageAccess) string {
		var e *PageAccessError
		if err := a.Err(); errors.As(err, &e) {
			return e.Code
		}
		return ""
	}
	assert.Equal(t, "", code(PageAccess{Authenticated: true}))
	assert.Equal(t, CodeSecurityVerification, code(PageAccess{SecurityVerification: true, LoginVisible: true}))
	assert.Equal(t, CodeLoginRequired, code(PageAccess{LoginVisible: true}))
	assert.Equal(t, CodeLoginRequired, code(PageAccess{Guest: true}))
	assert.Equal(t, CodeAccountRestricted, code(PageAccess{Authenticated: true, Markers: []string{"访问频繁", "该账号疑似存在风险"}}))
	assert.Equal(t, CodeRateLimited, code(PageAccess{Authenticated: true, Markers: []string{"操作频繁"}}))
	assert.Equal(t, "", code(PageAccess{Authenticated: true, Markers: []string{"验证码"}}), "正文里出现「验证码」三个字不算拦截")
}
