package reddit

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseForums(t *testing.T) {
	html := `<html><body>
      <div class="search-result search-result-subreddit">
        <a class="search-title" href="/r/COROLLA/">r/COROLLA</a>
        <p class="search-result-body">Toyota Corolla owners and questions.</p>
        <span class="search-subscribers">73,251 subscribers</span>
      </div>
    </body></html>`
	forums, err := ParseForums(strings.NewReader(html), 10)
	require.NoError(t, err)
	require.Len(t, forums, 1)
	assert.Equal(t, "COROLLA", forums[0].Name)
	assert.Equal(t, 73251, forums[0].Subscribers)
	assert.Equal(t, "Toyota Corolla owners and questions.", forums[0].Description)
}

func TestParsePosts(t *testing.T) {
	html := `<html><body>
      <div class="thing link" data-fullname="t3_1uyxj8j" data-author="Ordinaryguyisme">
        <p class="title"><a class="title" href="https://i.redd.it/photo.jpg">Best pic Ive gotten so far</a></p>
        <div class="score unvoted" title="58">58 points</div>
        <p class="tagline"><time datetime="2026-07-17T08:00:00+00:00"></time></p>
        <a class="comments" href="/r/COROLLA/comments/1uyxj8j/best_pic_ive_gotten_so_far/">2 comments</a>
      </div>
    </body></html>`
	posts, err := ParsePosts(strings.NewReader(html), "COROLLA", 10)
	require.NoError(t, err)
	require.Len(t, posts, 1)
	assert.Equal(t, "t3_1uyxj8j", posts[0].ID)
	assert.Equal(t, 58, posts[0].Score)
	assert.Equal(t, 2, posts[0].CommentCount)
	assert.Equal(t, "https://i.redd.it/photo.jpg", posts[0].OutboundURL)
}

func TestParsePostAndComments(t *testing.T) {
	html := `<html><body>
      <div class="thing link" data-fullname="t3_1uyxj8j" data-author="Ordinaryguyisme">
        <a class="title" href="/r/COROLLA/comments/1uyxj8j/best_pic_ive_gotten_so_far/">Best pic Ive gotten so far</a>
        <div class="score" title="58"></div>
      </div>
      <div class="thing comment" data-fullname="t1_p6vi0wn" data-parent-fullname="t3_1uyxj8j" data-author="Infinite_You3755">
        <div class="entry">
          <div class="score" title="1"></div>
          <time datetime="2026-08-30T22:15:00+00:00"></time>
          <div class="usertext-body"><div class="md"><p>Great shot—the blue looks fantastic.</p></div></div>
          <a class="bylink" href="/r/COROLLA/comments/1uyxj8j/best_pic_ive_gotten_so_far/p6vi0wn/">permalink</a>
        </div>
      </div>
    </body></html>`
	result, err := ParsePost(strings.NewReader(html), "COROLLA", "t3_1uyxj8j", 10)
	require.NoError(t, err)
	assert.Equal(t, "Best pic Ive gotten so far", result.Post.Title)
	require.Len(t, result.Comments, 1)
	assert.Equal(t, "t1_p6vi0wn", result.Comments[0].ID)
	assert.Equal(t, "Great shot—the blue looks fantastic.", result.Comments[0].Body)
	assert.Equal(t, "https://www.reddit.com/r/COROLLA/comments/1uyxj8j/best_pic_ive_gotten_so_far/p6vi0wn/", result.Comments[0].URL)
}
