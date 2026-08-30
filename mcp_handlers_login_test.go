package main

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoginSessionStatusResultIncludesSecurityChallengeImage(t *testing.T) {
	image := []byte("security-qr")
	result := loginSessionStatusResult(LoginSessionStatus{
		SessionID:      "session-id",
		State:          LoginSessionCaptchaNeeded,
		ChallengeImage: image,
	})

	require.Len(t, result.Content, 2)
	assert.Equal(t, "text", result.Content[0].Type)
	assert.Contains(t, result.Content[0].Text, "需要扫描账号安全验证二维码")
	assert.Equal(t, "image", result.Content[1].Type)
	assert.Equal(t, "image/png", result.Content[1].MimeType)
	assert.Equal(t, base64.StdEncoding.EncodeToString(image), result.Content[1].Data)
}

func TestLoginSessionStatusResultOmitsImageForNonChallengeState(t *testing.T) {
	result := loginSessionStatusResult(LoginSessionStatus{
		SessionID:      "session-id",
		State:          LoginSessionQRScanned,
		ChallengeImage: []byte("stale-image"),
	})

	require.Len(t, result.Content, 1)
	assert.Equal(t, "text", result.Content[0].Type)
}
