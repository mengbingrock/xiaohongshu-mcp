package chineseinla

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseForumPostsUsesLiveTopicRows(t *testing.T) {
	t.Parallel()
	html := `<html><body>
<div class="topic_list_detail">
 <div class="topic_list_11"><span class="bid"></span></div>
 <div class="topic_list_12"><div class="havepage1"><a href="/f/page_viewtopic/t_1117020/bdidw_816177154.html" class="title">移民申请，各类离婚</a></div></div>
 <div class="topic_list_2"><span class="author"><a href="/user/id_462266.html">同舟事务所</a></span><span class="time">2024-12-03</span></div>
 <div class="topic_list_3"><span class="reply_count">61</span><span class="read_count">301,557</span></div>
</div>
<div class="topic_list_detail">
 <div class="topic_list_11 topic_list_112"><span class="read"></span></div>
 <div class="topic_list_12"><div class="havenopage"><a href="/f/page_viewtopic/t_3139709.html" class="title">法律与移民服务</a><span class="img"></span></div></div>
 <div class="topic_list_2"><span class="author"><a href="/user/id_2375200.html">9152柯柯</a></span><span class="time">12:39 pm</span></div>
 <div class="topic_list_3"><span class="reply_count">0</span><span class="read_count">10</span></div>
</div>
<a href="/f/page_viewforum/f_102/start_15.html">下一页</a>
</body></html>`

	posts, hasNext, err := ParseForumPosts(strings.NewReader(html), 102, 1)
	require.NoError(t, err)
	require.Len(t, posts, 2)
	assert.True(t, hasNext)
	assert.Equal(t, 1117020, posts[0].TopicID)
	assert.Equal(t, BaseURL+"/f/page_viewtopic/t_1117020.html", posts[0].URL)
	assert.Equal(t, 462266, posts[0].AuthorID)
	assert.Equal(t, 301557, posts[0].ViewCount)
	assert.True(t, posts[0].Highlighted)
	assert.Equal(t, 3139709, posts[1].TopicID)
	assert.True(t, posts[1].HasImages)
}

func TestParseTopicReadsOnlyRealPostContent(t *testing.T) {
	t.Parallel()
	html := `<html><body>
	<a href="/f/page_viewforum/f_11.html">活动预告</a>
	<div class="forum_path"><a href="/f/page_viewforum/f_102.html">法律服务</a></div>
<table><tr class="post_line">
 <td class="post_left"><div class="user_name"><a href="/user/id_2475045.html">ElaineLA</a></div></td>
 <td class="post_right">
  <div class="post_top"><div class="post_title"><h1>寻找加州华人民事诉讼律师</h1></div><div class="post_time"><span>发布于: 2026/08/24 2:14 am</span><span>更新于: 2026/08/24, 2:14 am</span><div class="post_quote"><a href="/f/page_pppping/mode_quote/p_4399652.html">引用</a></div></div></div>
  <div class="post_body"><div class="post_ads">广告文字</div><p class="real-content">大家好，<br>这是正文。<img src="/uploads/example.jpg"></p></div>
 </td>
</tr></table>
<a href="/f/page_viewtopic/t_3138499/start_10.html">下一页</a>
</body></html>`

	result, err := ParseTopic(strings.NewReader(html), 3138499, 1)
	require.NoError(t, err)
	assert.Equal(t, 102, result.Forum.ID)
	assert.Equal(t, "寻找加州华人民事诉讼律师", result.Title)
	assert.True(t, result.HasNext)
	require.Len(t, result.Messages, 1)
	message := result.Messages[0]
	assert.Equal(t, 4399652, message.PostID)
	assert.Equal(t, 2475045, message.AuthorID)
	assert.Equal(t, "ElaineLA", message.Author)
	assert.Equal(t, "2026/08/24 2:14 am", message.PublishedAt)
	assert.Equal(t, "2026/08/24, 2:14 am", message.UpdatedAt)
	assert.Equal(t, "大家好，\n这是正文。", message.Body)
	assert.NotContains(t, message.Body, "广告")
	assert.Equal(t, []string{BaseURL + "/uploads/example.jpg"}, message.ImageURLs)
}

func TestValidateReaderRequestBounds(t *testing.T) {
	t.Parallel()
	page, limit, err := validateReaderRequest(6, 102, 0, 0, forumPostsPerPage)
	require.NoError(t, err)
	assert.Equal(t, 1, page)
	assert.Equal(t, 15, limit)
	_, _, err = validateReaderRequest(6, 102, 1, 16, forumPostsPerPage)
	assert.ErrorContains(t, err, "between 1 and 15")
	_, _, err = validateReaderRequest(5, 102, 101, 1, forumPostsPerPage)
	assert.ErrorContains(t, err, "between 1 and 100")
}
