package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/xpzouying/xiaohongshu-mcp/internal/chineseinla"
)

type stringList []string

func (values *stringList) String() string {
	return strings.Join(*values, ",")
}

func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

type errorOutput struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Action string `json:"action,omitempty"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	config, err := chineseinla.DefaultConfig()
	if err != nil {
		writeError(stderr, err, "")
		return 2
	}

	global := flag.NewFlagSet("chineseinla", flag.ContinueOnError)
	global.SetOutput(stderr)
	global.IntVar(&config.CDPPort, "cdp-port", config.CDPPort, "dedicated Chrome DevTools port")
	global.StringVar(&config.ProfileDir, "profile-dir", config.ProfileDir, "isolated persistent browser profile")
	global.StringVar(&config.CookiePath, "cookies-file", config.CookiePath, "ChineseInLA cookies JSON file")
	global.StringVar(&config.StatePath, "state-file", config.StatePath, "prepared-post state file")
	global.StringVar(&config.PreviewPath, "preview-image", config.PreviewPath, "headless prepared-form preview PNG")
	global.StringVar(&config.BrowserBin, "browser-bin", config.BrowserBin, "browser executable (defaults to the repository's bundled browser)")
	global.BoolVar(&config.Headless, "headless", config.Headless, "run Chromium without a visible window")
	if err := global.Parse(args); err != nil {
		return 2
	}
	remaining := global.Args()
	if len(remaining) == 0 {
		printUsage(stderr)
		return 2
	}

	automation := chineseinla.NewAutomation(config)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	command := remaining[0]
	commandArgs := remaining[1:]
	switch command {
	case "login":
		if len(commandArgs) != 0 {
			writeError(stderr, errors.New("login does not accept positional arguments"), "")
			return 2
		}
		result, commandErr := automation.Login(ctx)
		if commandErr != nil {
			return reportCommandError(stderr, commandErr)
		}
		writeJSON(stdout, result)
		return 0

	case "check-login":
		if len(commandArgs) != 0 {
			writeError(stderr, errors.New("check-login does not accept positional arguments"), "")
			return 2
		}
		result, commandErr := automation.CheckLogin(ctx)
		if commandErr != nil {
			return reportCommandError(stderr, commandErr)
		}
		writeJSON(stdout, result)
		if !result.LoggedIn {
			return 1
		}
		return 0

	case "forums":
		if len(commandArgs) != 0 {
			writeError(stderr, errors.New("forums does not accept positional arguments"), "")
			return 2
		}
		forums, commandErr := automation.Forums(ctx)
		if commandErr != nil {
			return reportCommandError(stderr, commandErr)
		}
		writeJSON(stdout, struct {
			Status string              `json:"status"`
			Count  int                 `json:"count"`
			Forums []chineseinla.Forum `json:"forums"`
		}{Status: "ok", Count: len(forums), Forums: forums})
		return 0

	case "prepare":
		request, parseErr := parsePrepare(commandArgs, stderr)
		if parseErr != nil {
			writeError(stderr, parseErr, "")
			return 2
		}
		result, commandErr := automation.Prepare(ctx, request)
		if commandErr != nil {
			return reportCommandError(stderr, commandErr)
		}
		writeJSON(stdout, result)
		return 0

	case "publish":
		publishFlags := flag.NewFlagSet("publish", flag.ContinueOnError)
		publishFlags.SetOutput(stderr)
		confirmed := publishFlags.Bool("confirm", false, "required explicit authorization to click Publish")
		if err := publishFlags.Parse(commandArgs); err != nil {
			return 2
		}
		if len(publishFlags.Args()) != 0 {
			writeError(stderr, errors.New("publish does not accept positional arguments"), "")
			return 2
		}
		result, commandErr := automation.Publish(ctx, *confirmed)
		if commandErr != nil {
			return reportCommandError(stderr, commandErr)
		}
		writeJSON(stdout, result)
		return 0

	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		writeError(stderr, fmt.Errorf("unknown command %q", command), "")
		printUsage(stderr)
		return 2
	}
}

func parsePrepare(args []string, stderr io.Writer) (chineseinla.PrepareRequest, error) {
	flags := flag.NewFlagSet("prepare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	forumID := flags.Int("forum-id", 0, "live ChineseInLA forum ID")
	postTypeValue := flags.String("post-type", "", "question, classified, or other")
	title := flags.String("title", "", "post title")
	titleFile := flags.String("title-file", "", "UTF-8 file containing the post title")
	body := flags.String("body", "", "post body")
	bodyFile := flags.String("body-file", "", "UTF-8 file containing the post body")
	sourceURL := flags.String("source-url", "", "public source URL to append as attribution")
	var tags, images, imageURLs, videoURLs stringList
	flags.Var(&tags, "tag", "tag (repeatable or comma-separated)")
	flags.Var(&images, "image", "local JPEG/PNG/GIF/BMP image (repeatable)")
	flags.Var(&imageURLs, "image-url", "public image URL (repeatable)")
	flags.Var(&videoURLs, "video-url", "video or embed URL to preserve as a link (repeatable)")
	if err := flags.Parse(args); err != nil {
		return chineseinla.PrepareRequest{}, err
	}
	if len(flags.Args()) != 0 {
		return chineseinla.PrepareRequest{}, errors.New("prepare does not accept positional arguments")
	}

	resolvedTitle, err := readInput(*title, *titleFile, "title")
	if err != nil {
		return chineseinla.PrepareRequest{}, err
	}
	resolvedBody, err := readInput(*body, *bodyFile, "body")
	if err != nil {
		return chineseinla.PrepareRequest{}, err
	}
	postType, err := chineseinla.ParsePostType(*postTypeValue)
	if err != nil {
		return chineseinla.PrepareRequest{}, err
	}

	return chineseinla.PrepareRequest{
		ForumID:   *forumID,
		PostType:  postType,
		Title:     resolvedTitle,
		Body:      resolvedBody,
		Tags:      tags,
		Images:    images,
		ImageURLs: imageURLs,
		SourceURL: *sourceURL,
		VideoURLs: videoURLs,
	}, nil
}

func readInput(direct, path, label string) (string, error) {
	if strings.TrimSpace(direct) != "" && strings.TrimSpace(path) != "" {
		return "", fmt.Errorf("use either --%s or --%s-file, not both", label, label)
	}
	if strings.TrimSpace(path) == "" {
		if strings.TrimSpace(direct) == "" {
			return "", fmt.Errorf("--%s or --%s-file is required", label, label)
		}
		return direct, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s file: %w", label, err)
	}
	return string(data), nil
}

func reportCommandError(stderr io.Writer, err error) int {
	action := ""
	code := 2
	if errors.Is(err, chineseinla.ErrNotLoggedIn) {
		action = "Run login, complete sign-in in the dedicated browser, then retry."
		code = 1
	}
	if errors.Is(err, chineseinla.ErrCaptcha) {
		action = "Complete the human verification in the dedicated browser; do not try to bypass it."
		code = 1
	}
	writeError(stderr, err, action)
	return code
}

func writeJSON(writer io.Writer, value any) {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func writeError(writer io.Writer, err error, action string) {
	writeJSON(writer, errorOutput{Status: "error", Error: err.Error(), Action: action})
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, `Usage: chineseinla [global flags] <command> [flags]

Commands:
  login          Open ChineseInLA login in the isolated browser profile
  check-login    Report whether that profile is authenticated
  forums         Return the live postable forum catalog as JSON
  prepare        Fill and upload a post for visible review; never publishes
  publish        Publish the prepared browser form; requires --confirm

Global example (read-only headless forum check):
  chineseinla --headless --cdp-port 9333 --profile-dir /tmp/chineseinla-headless forums

Headless prepare example (still requires a separate publish confirmation):
  chineseinla --headless --preview-image /tmp/chineseinla-preview.png prepare \
    --forum-id 23 --post-type other --title-file title.txt --body-file body.txt

Prepare example:
  chineseinla prepare --forum-id 21 --post-type other \
    --title-file title.txt --body-file body.txt --image-url https://example.com/photo.jpg

Publish example (only after a separate user confirmation):
  chineseinla publish --confirm`)
}
