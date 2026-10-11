package cookies

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/xpzouying/xiaohongshu-mcp/site"
)

// TestGetCookiesFilePath 校验路径：COOKIES_PATH 优先，否则当前目录；不再回落到共享的 /tmp。
func TestGetCookiesFilePath(t *testing.T) {
	t.Run("显式指定的COOKIES_PATH永远最优先", func(t *testing.T) {
		t.Setenv("COOKIES_PATH", "/custom/cookies.json")
		assert.Equal(t, "/custom/cookies.json", GetCookiesFilePath())
	})

	t.Run("tmp下的旧文件不再被使用", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("TMPDIR", tmp)
		t.Setenv("COOKIES_PATH", "")
		t.Chdir(t.TempDir())
		assert.NoError(t, os.WriteFile(filepath.Join(tmp, "cookies.json"), []byte("[]"), 0644))

		assert.Equal(t, "cookies.json", GetCookiesFilePath())
	})
}

// TestSaveCookiesPrivateAtomic 会话文件以 0600 写入，目录 0700，且不留临时文件。
func TestSaveCookiesPrivateAtomic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "account")
	path := filepath.Join(dir, "cookies.json")
	assert.NoError(t, NewLoadCookie(path).SaveCookies([]byte("[]")))

	info, err := os.Stat(path)
	assert.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	dirInfo, err := os.Stat(dir)
	assert.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), dirInfo.Mode().Perm())
	entries, err := os.ReadDir(dir)
	assert.NoError(t, err)
	assert.Len(t, entries, 1)
}

// TestLoadSaveDeleteCookies 校验 cookie 文件存取往返与删除的幂等。
func TestLoadSaveDeleteCookies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.json")
	c := NewLoadCookie(path)

	// 未写入时读取应报错
	_, err := c.LoadCookies()
	assert.Error(t, err)

	// 写入后能读回同样的内容（落盘是 v2，排版会变，内容不变）
	want := []byte(`[{"name":"web_session","value":"x"}]`)
	assert.NoError(t, c.SaveCookies(want))
	got, err := c.LoadCookies()
	assert.NoError(t, err)
	assert.Equal(t, decodeJSON(t, want), decodeJSON(t, got))

	// 落盘是 v2 外层对象，不再是裸数组。只看结构，不看排版
	onDisk, err := os.ReadFile(path)
	assert.NoError(t, err)
	info, err := os.Stat(path)
	assert.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	var f map[string]any
	assert.NoError(t, json.Unmarshal(onDisk, &f))
	assert.Equal(t, float64(2), f["version"])

	// 删除后文件消失，且再次删除幂等（不报错）
	assert.NoError(t, c.DeleteCookies())
	assert.NoFileExists(t, path)
	assert.NoError(t, c.DeleteCookies())
}

func TestSaveCookies_TightensLegacyFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.json")
	assert.NoError(t, os.WriteFile(path, []byte(`[]`), 0644))

	c := NewLoadCookie(path)
	assert.NoError(t, c.SaveCookies([]byte(`[{"name":"web_session","value":"new"}]`)))

	info, err := os.Stat(path)
	assert.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

// TestSeed 校验 seed 的持久化与 v1/v2 两种格式的读取。
func TestSeed(t *testing.T) {
	t.Run("v1裸数组读不到seed且cookies原样", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cookies.json")
		raw := []byte(`[{"name":"web_session","value":"x"}]`)
		assert.NoError(t, os.WriteFile(path, raw, 0644))

		c := NewLoadCookie(path)

		got, err := c.LoadCookies()
		assert.NoError(t, err)
		assert.Equal(t, decodeJSON(t, raw), decodeJSON(t, got))
		assert.Equal(t, 0, c.LoadSeed())
	})

	t.Run("存seed后读得回来且cookies内容不走样", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cookies.json")
		c := NewLoadCookie(path)

		// 字段顺序刻意打乱、数值用科学计数法，用来暴露"反序列化再序列化"造成的走样
		raw := []byte(`[{"value":"x","name":"web_session","expires":1.75e9}]`)
		assert.NoError(t, c.SaveCookies(raw))
		assert.NoError(t, c.SaveSeed(23088))

		assert.Equal(t, 23088, c.LoadSeed())

		got, err := c.LoadCookies()
		assert.NoError(t, err)
		// 比语义不比字节：落盘会重新缩进（只动无意义空白），字段和取值一个都不能变
		assert.Equal(t, decodeJSON(t, raw), decodeJSON(t, got))
	})
}

// TestSeedRobustness 校验 seed 在异常与并发写入下的表现。
func TestSeedRobustness(t *testing.T) {
	t.Run("存cookies不冲掉已有的seed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cookies.json")
		c := NewLoadCookie(path)

		assert.NoError(t, c.SaveSeed(23088))
		// 模拟重新登录：cookies 换了一批，seed 必须留着
		assert.NoError(t, c.SaveCookies([]byte(`[{"name":"web_session","value":"new"}]`)))

		assert.Equal(t, 23088, c.LoadSeed())
	})

	t.Run("文件损坏时降级为没有seed且不panic", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cookies.json")
		assert.NoError(t, os.WriteFile(path, []byte(`{"version":2,"seed":`), 0644))

		assert.Equal(t, 0, NewLoadCookie(path).LoadSeed())
	})

	t.Run("文件不存在时读seed返回0", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nope.json")

		assert.Equal(t, 0, NewLoadCookie(path).LoadSeed())
	})
}

// decodeJSON 把 JSON 解成通用结构，用于只比内容、不比排版。
func decodeJSON(t *testing.T, data []byte) any {
	t.Helper()

	var v any
	assert.NoError(t, json.Unmarshal(data, &v))
	return v
}

// TestSaveCookies_CreatesParentDir 保存时父目录不存在也应能落盘。
//
// COOKIES_PATH 指向一个还没创建的目录时，原先会直接写失败。
func TestSaveCookies_CreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "cookies.json")

	c := NewLoadCookie(path)
	assert.NoError(t, c.SaveCookies([]byte(`[{"name":"a","value":"b"}]`)))

	got, err := c.LoadCookies()
	assert.NoError(t, err)

	var cks []map[string]string
	assert.NoError(t, json.Unmarshal(got, &cks))
	assert.Equal(t, "a", cks[0]["name"])
}

func TestDeleteCookies_KeepsSeed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.json")
	c := NewLoadCookie(path)

	assert.NoError(t, c.SaveSeed(424242))
	assert.NoError(t, c.SaveCookies([]byte(`[{"name":"web_session","value":"x"}]`)))
	assert.Equal(t, 424242, c.LoadSeed())

	// 登出后：cookies 不可用，但 seed 必须还在，文件权限不变
	assert.NoError(t, c.DeleteCookies())
	_, err := c.LoadCookies()
	assert.ErrorIs(t, err, ErrNoCookies)
	assert.Equal(t, 424242, c.LoadSeed())
	info, err := os.Stat(path)
	assert.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	// 再次登录后 seed 仍是原来的那个
	assert.NoError(t, c.SaveCookies([]byte(`[{"name":"web_session","value":"y"}]`)))
	assert.Equal(t, 424242, c.LoadSeed())
	got, err := c.LoadCookies()
	assert.NoError(t, err)
	assert.Contains(t, string(got), `"y"`)

	// 幂等
	assert.NoError(t, c.DeleteCookies())
	assert.NoError(t, c.DeleteCookies())
}

func TestSiteRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.json")
	c := NewLoadCookie(path)

	assert.Equal(t, "", c.LoadSite())
	assert.NoError(t, c.SaveSeed(7))
	assert.NoError(t, c.SaveSite("intl"))
	assert.Equal(t, "intl", c.LoadSite())
	assert.Equal(t, 7, c.LoadSeed())

	// 保存 cookies / 登出 都不能丢站点和 seed
	assert.NoError(t, c.SaveCookies([]byte(`[{"name":"id_token","domain":".rednote.com"}]`)))
	assert.Equal(t, "intl", c.LoadSite())
	assert.NoError(t, c.DeleteCookies())
	assert.Equal(t, "intl", c.LoadSite())
	assert.Equal(t, 7, c.LoadSeed())

	// 换站点
	assert.NoError(t, c.SaveSite("cn"))
	assert.Equal(t, "cn", c.LoadSite())
}

// useSite 测试里钉住站点（等于设了 XHS_SITE），结束时取消。
func useSite(t *testing.T, s site.Site) {
	site.Pin(s)
	t.Cleanup(site.Unpin)
}

// TestSiteTagging 钉住站点时，中文站和海外站是两套登录状态：会话文件记下站点，另一个站点的会话不加载、不覆盖。
func TestSiteTagging(t *testing.T) {
	t.Run("写入时记下当前站点", func(t *testing.T) {
		useSite(t, site.Rednote)
		path := filepath.Join(t.TempDir(), "cookies.json")
		assert.NoError(t, NewLoadCookie(path).SaveCookies([]byte(`[{"name":"web_session"}]`)))

		var f sessionFile
		data, _ := os.ReadFile(path)
		assert.NoError(t, json.Unmarshal(data, &f))
		assert.Equal(t, "intl", f.Site)
	})

	t.Run("老版本记的 rednote 也是海外站", func(t *testing.T) {
		useSite(t, site.Rednote)
		path := filepath.Join(t.TempDir(), "cookies.json")
		assert.NoError(t, os.WriteFile(path, []byte(`{"version":2,"site":"rednote","seed":5,"cookies":[{"name":"id_token"}]}`), 0600))
		c := NewLoadCookie(path)
		_, err := c.LoadCookies()
		assert.NoError(t, err)
		assert.Equal(t, 5, c.LoadSeed())
		assert.Equal(t, "intl", c.LoadSite())
	})

	t.Run("没钉住时不查站点", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cookies.json")
		assert.NoError(t, os.WriteFile(path, []byte(`{"version":2,"site":"intl","cookies":[{"name":"id_token"}]}`), 0600))
		_, err := NewLoadCookie(path).LoadCookies()
		assert.NoError(t, err)
	})

	t.Run("另一个站点的会话拒绝加载，seed 也不沿用", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cookies.json")
		useSite(t, site.Rednote)
		c := NewLoadCookie(path)
		assert.NoError(t, c.SaveCookies([]byte(`[{"name":"web_session"}]`)))
		assert.NoError(t, c.SaveSeed(42))

		site.Pin(site.CN)
		_, err := c.LoadCookies()
		assert.ErrorIs(t, err, ErrSiteMismatch)
		assert.Equal(t, 0, c.LoadSeed())
	})

	t.Run("另一个站点的会话不被覆盖", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cookies.json")
		useSite(t, site.Rednote)
		assert.NoError(t, NewLoadCookie(path).SaveCookies([]byte(`[{"name":"web_session"}]`)))
		before, _ := os.ReadFile(path)

		site.Pin(site.CN)
		assert.ErrorIs(t, NewLoadCookie(path).SaveCookies([]byte(`[]`)), ErrSiteMismatch)
		assert.ErrorIs(t, NewLoadCookie(path).SaveSeed(7), ErrSiteMismatch)
		after, _ := os.ReadFile(path)
		assert.Equal(t, before, after)
	})

	t.Run("没有站点字段的老文件是中文站", func(t *testing.T) {
		dir := t.TempDir()
		v1 := filepath.Join(dir, "v1.json")
		v2 := filepath.Join(dir, "v2.json")
		assert.NoError(t, os.WriteFile(v1, []byte(`[{"name":"a"}]`), 0600))
		assert.NoError(t, os.WriteFile(v2, []byte(`{"version":2,"seed":9,"cookies":[{"name":"a"}]}`), 0600))

		useSite(t, site.CN)
		for _, p := range []string{v1, v2} {
			_, err := NewLoadCookie(p).LoadCookies()
			assert.NoError(t, err, p)
		}
		site.Pin(site.Rednote)
		for _, p := range []string{v1, v2} {
			_, err := NewLoadCookie(p).LoadCookies()
			assert.ErrorIs(t, err, ErrSiteMismatch, p)
		}
	})
}
