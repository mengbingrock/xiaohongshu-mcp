package xiaohongshu

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateVerificationCode(t *testing.T) {
	for _, code := range []string{"012345", "999999", " 123456 "} {
		assert.NoError(t, ValidateVerificationCode(code), "valid code %q", code)
	}

	for _, code := range []string{"", "12345", "1234567", "12a456", "１２３４５６"} {
		assert.ErrorIs(t, ValidateVerificationCode(code), ErrInvalidVerificationCode, "invalid code %q", code)
	}
}

func TestClassifyLoginDOMObservation(t *testing.T) {
	tests := []struct {
		name string
		dom  loginDOMObservation
		want LoginPageState
	}{
		{
			name: "authenticated wins",
			dom: loginDOMObservation{
				Authenticated: true, CaptchaVisible: true, SecurityQRVisible: true, QRScanned: true, OTPVisible: true,
			},
			want: LoginPageAuthenticated,
		},
		{
			name: "account security QR wins over captcha-named SMS modal",
			dom: loginDOMObservation{
				SecurityQRVisible: true, QRScanned: true, SMSModalVisible: true,
			},
			want: LoginPageCaptchaNeeded,
		},
		{
			name: "account security QR wins over an overlapping OTP input",
			dom: loginDOMObservation{
				SecurityQRVisible: true, QRScanned: true, SMSModalVisible: true, OTPVisible: true,
			},
			want: LoginPageCaptchaNeeded,
		},
		{
			name: "scanned SMS modal wins over captcha-named CSS",
			dom: loginDOMObservation{
				CaptchaVisible: true, QRScanned: true, OTPVisible: true,
			},
			want: LoginPageOTPRequired,
		},
		{
			name: "SMS modal mount before input is a scanned transition",
			dom: loginDOMObservation{
				CaptchaVisible: true, QRScanned: true, SMSModalVisible: true,
			},
			want: LoginPageQRScanned,
		},
		{
			name: "real captcha without scanned OTP",
			dom:  loginDOMObservation{CaptchaVisible: true, QRScanned: true},
			want: LoginPageCaptchaNeeded,
		},
		{
			name: "phone login OTP is ignored before QR scan",
			dom:  loginDOMObservation{OTPVisible: true},
			want: LoginPageWaitingForScan,
		},
		{
			name: "QR scan without OTP",
			dom:  loginDOMObservation{QRScanned: true},
			want: LoginPageQRScanned,
		},
		{
			name: "waiting",
			dom:  loginDOMObservation{},
			want: LoginPageWaitingForScan,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classifyLoginDOMObservation(tt.dom).State)
		})
	}
}

func TestIsVerificationSubmitText(t *testing.T) {
	for _, text := range []string{"验证", "  验证  ", "确 认", "提交"} {
		assert.True(t, isVerificationSubmitText(text), "text %q", text)
	}
	for _, text := range []string{"", "获取验证码", "登录", "关闭"} {
		assert.False(t, isVerificationSubmitText(text), "text %q", text)
	}
}

func TestVerificationCodeKeys(t *testing.T) {
	keys := verificationCodeKeys("907214")
	assert.Len(t, keys, 6)
	assert.Equal(t, []string{"9", "0", "7", "2", "1", "4"}, []string{
		keys[0].Info().Key,
		keys[1].Info().Key,
		keys[2].Info().Key,
		keys[3].Info().Key,
		keys[4].Info().Key,
		keys[5].Info().Key,
	})
}

func TestVerificationSubmitEnabled(t *testing.T) {
	trueValue := "true"
	disabledValue := "disabled"
	falseValue := "false"

	assert.False(t, verificationSubmitEnabled("btn text-default-bold btn-disabled btn-block", &trueValue, nil))
	assert.False(t, verificationSubmitEnabled("btn disabled", nil, nil))
	assert.False(t, verificationSubmitEnabled("btn", &disabledValue, nil))
	assert.False(t, verificationSubmitEnabled("btn", nil, &trueValue))
	assert.True(t, verificationSubmitEnabled("btn text-default-bold btn-block", nil, nil))
	assert.True(t, verificationSubmitEnabled("btn text-default-bold btn-block", &falseValue, &falseValue))
}
