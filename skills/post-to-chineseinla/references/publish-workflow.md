# ChineseInLA publishing workflow reference

The MCP integration and Go helper use a single persistent, isolated ChineseInLA Chromium profile. They do not read the user's everyday Chrome profile and do not share Xiaohongshu cookies.

## MCP tools

One `/mcp` endpoint exposes both the existing Xiaohongshu tools and these namespaced ChineseInLA tools:

- `chineseinla_open_login`
- `chineseinla_check_login`
- `chineseinla_list_forums`
- `chineseinla_prepare_post`
- `chineseinla_publish_post`

`chineseinla_prepare_post` accepts `forum_id`, `post_type`, `title`, `body`, optional `tags`, `images`, `image_urls`, `source_url`, and `video_urls`, plus required first authorization `confirm_preparation=true`. It fills but does not submit the form, and returns a random `draft_id`. In headless mode it also returns the PNG preview as an MCP image content block.

After review and a separate user confirmation, pass that exact `draft_id` to `chineseinla_publish_post` with `confirm_publish=true`. A stale or missing ID is rejected before the publish control is clicked. ChineseInLA MCP operations are serialized because one server instance owns one prepared form at a time; Xiaohongshu tools remain independently available.

## Commands and output

Run commands from the repository root.

```bash
# Open the login page in the isolated browser
go run ./cmd/chineseinla login

# Exit 0 when authenticated; exit 1 when user action is required
go run ./cmd/chineseinla check-login

# Fetch the current forum catalog as JSON
go run ./cmd/chineseinla forums

# Fill a form and upload media, but never publish
go run ./cmd/chineseinla prepare \
  --forum-id 21 \
  --post-type question|classified|other \
  --title-file /absolute/title.txt \
  --body-file /absolute/body.txt

# Click the site's publish control only after a separate user confirmation
go run ./cmd/chineseinla publish --confirm
```

`prepare` accepts these repeatable options:

- `--tag VALUE`
- `--image /absolute/local-image.jpg`
- `--image-url https://public.example/image.jpg`
- `--video-url https://public.example/video`

It also accepts `--source-url`. Direct `--title` and `--body` exist for manual use, but files are safer for generated Unicode content.

All normal output is JSON. Exit codes are:

- `0`: command completed;
- `1`: login, CAPTCHA, or another explicit user action is required;
- `2`: validation, download, page-structure, or automation error.

## Session and configuration

Defaults:

- CDP port: `9223`;
- profile: the OS user-config directory under `xiaohongshu-mcp/chineseinla/profile`;
- prepared state: the adjacent `prepared.json` file.

Overrides are available as global flags before the command:

```bash
go run ./cmd/chineseinla \
  --headless \
  --cdp-port 9333 \
  --profile-dir /absolute/profile \
  --state-file /absolute/prepared.json \
  --preview-image /absolute/prepared-preview.png \
  --browser-bin /absolute/chromium \
  forums
```

Equivalent environment variables are `CHINESEINLA_CDP_PORT`, `CHINESEINLA_PROFILE_DIR`, `CHINESEINLA_STATE_PATH`, `CHINESEINLA_PREVIEW_PATH`, `CHINESEINLA_BROWSER_BIN`, and `CHINESEINLA_HEADLESS`.

Headless mode supports `forums`, `check-login`, `prepare`, and `publish`. It requires an already authenticated persistent profile. `prepare` writes a focused PNG of the filled form and returns its absolute path as `preview_image`; the agent must inspect and display that image before requesting the separate publish confirmation. Use visible mode for interactive login or CAPTCHA handling.

Use identical global mode, port, profile, state, and preview paths for headless `prepare` and `publish`. A post prepared in one mode cannot be published in the other.

The prepared-state file stores only a random draft ID, forum/form metadata, title, and preparation time. It never stores credentials or post body text. `publish` targets the still-open prepared form, so user edits made during visual review are retained.

## Media behavior

- Local and remote media are validated as JPEG, PNG, GIF, or BMP, matching the current uploader.
- Remote images are limited to 25 MiB, use browser-like request headers, and set the source page as the referrer when supplied.
- Redirects and DNS targets are checked; loopback, private, link-local, multicast, and unspecified network destinations are rejected.
- All remote images are downloaded successfully before form filling starts. A failed image aborts preparation rather than silently producing a partial upload.
- Temporary remote-image files are removed after the site uploader accepts them.
- Video URLs are appended to the body as links and are never downloaded.

## Recovery

- Not logged in: run `login`, let the user finish sign-in, run `check-login`, then retry.
- CAPTCHA/human verification: stop automation. If running headless, restart the workflow visibly so the user can complete it; do not bypass it.
- Forum ID missing: rerun `forums`; the site's live catalog may have changed.
- Image rejected: use a supported image type or omit that image with the user's approval. Do not disable URL safety checks.
- Form selector error: leave the browser open, inspect the current live form, and update `internal/chineseinla/automation.go`. Do not fall back to a direct HTTP POST because the browser-owned form tokens and user preview are deliberate safeguards.
- Publish timeout: inspect the visible form or regenerate a headless preview and check the account's published-topic list. Do not retry until absence is verified, and do not report success unless the CLI returns `published` with a topic URL.
