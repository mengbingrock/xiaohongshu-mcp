package chineseinla

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultMaxImageBytes = 25 << 20
	downloadUserAgent    = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
)

type URLValidator func(context.Context, string) error

type ImageDownloader struct {
	Client      *http.Client
	MaxBytes    int64
	ValidateURL URLValidator
	TempDir     string
}

type ImageFiles struct {
	Paths     []string
	temporary []string
}

func (files ImageFiles) Cleanup() {
	for _, path := range files.temporary {
		_ = os.Remove(path)
	}
}

func NewImageDownloader() *ImageDownloader {
	validator := validatePublicURL
	transport := &http.Transport{
		Proxy:             nil,
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext:       publicDialContext,
	}
	return &ImageDownloader{
		Client: &http.Client{
			Timeout:   45 * time.Second,
			Transport: transport,
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many image redirects")
				}
				return validator(request.Context(), request.URL.String())
			},
		},
		MaxBytes:    defaultMaxImageBytes,
		ValidateURL: validator,
	}
}

func (d *ImageDownloader) Prepare(ctx context.Context, localPaths, remoteURLs []string, sourceURL string) (ImageFiles, error) {
	if d == nil {
		d = NewImageDownloader()
	}
	if d.MaxBytes <= 0 {
		d.MaxBytes = defaultMaxImageBytes
	}
	if d.Client == nil {
		d.Client = NewImageDownloader().Client
	}
	if d.ValidateURL == nil {
		d.ValidateURL = validatePublicURL
	}

	files := ImageFiles{Paths: make([]string, 0, len(localPaths)+len(remoteURLs))}
	for _, raw := range localPaths {
		path, err := validateLocalImage(raw, d.MaxBytes)
		if err != nil {
			files.Cleanup()
			return ImageFiles{}, err
		}
		files.Paths = append(files.Paths, path)
	}
	for _, raw := range remoteURLs {
		path, err := d.download(ctx, raw, sourceURL)
		if err != nil {
			files.Cleanup()
			return ImageFiles{}, err
		}
		files.Paths = append(files.Paths, path)
		files.temporary = append(files.temporary, path)
	}
	return files, nil
}

func (d *ImageDownloader) download(ctx context.Context, raw, sourceURL string) (string, error) {
	if err := d.ValidateURL(ctx, raw); err != nil {
		return "", fmt.Errorf("image URL %q is not allowed: %w", raw, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", fmt.Errorf("create image request: %w", err)
	}
	request.Header.Set("User-Agent", downloadUserAgent)
	request.Header.Set("Accept", "image/png,image/jpeg,image/gif,image/bmp;q=0.9,*/*;q=0.1")
	request.Header.Set("Referer", imageReferer(raw, sourceURL))

	response, err := d.Client.Do(request)
	if err != nil {
		return "", fmt.Errorf("download image %q: %w", raw, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("download image %q: server returned %s", raw, response.Status)
	}
	if response.ContentLength > d.MaxBytes {
		return "", fmt.Errorf("image %q exceeds the %d MiB limit", raw, d.MaxBytes>>20)
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, d.MaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("read image %q: %w", raw, err)
	}
	if int64(len(data)) > d.MaxBytes {
		return "", fmt.Errorf("image %q exceeds the %d MiB limit", raw, d.MaxBytes>>20)
	}
	extension, err := supportedImageExtension(data)
	if err != nil {
		return "", fmt.Errorf("image %q: %w", raw, err)
	}

	file, err := os.CreateTemp(d.TempDir, "chineseinla-*."+extension)
	if err != nil {
		return "", fmt.Errorf("create temporary image: %w", err)
	}
	path := file.Name()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write temporary image: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close temporary image: %w", err)
	}
	return path, nil
}

func validateLocalImage(raw string, maxBytes int64) (string, error) {
	path, err := filepath.Abs(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("resolve local image %q: %w", raw, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("open local image %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("local image %q is not a regular file", path)
	}
	if info.Size() > maxBytes {
		return "", fmt.Errorf("local image %q exceeds the %d MiB limit", path, maxBytes>>20)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open local image %q: %w", path, err)
	}
	defer file.Close()
	header := make([]byte, 512)
	read, err := file.Read(header)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("inspect local image %q: %w", path, err)
	}
	if _, err := supportedImageExtension(header[:read]); err != nil {
		return "", fmt.Errorf("local image %q: %w", path, err)
	}
	return path, nil
}

func supportedImageExtension(data []byte) (string, error) {
	if len(data) < 2 {
		return "", errors.New("file is empty or too small to be an image")
	}
	mimeType := http.DetectContentType(data)
	switch mimeType {
	case "image/jpeg":
		return "jpg", nil
	case "image/png":
		return "png", nil
	case "image/gif":
		return "gif", nil
	case "image/bmp", "image/x-ms-bmp":
		return "bmp", nil
	default:
		return "", fmt.Errorf("unsupported image type %q; use JPEG, PNG, GIF, or BMP", mimeType)
	}
}

func imageReferer(imageURL, sourceURL string) string {
	if sourceURL != "" {
		if parsed, err := url.Parse(sourceURL); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			return parsed.String()
		}
	}
	parsed, err := url.Parse(imageURL)
	if err != nil {
		return BaseURL + "/"
	}
	parsed.Path = "/"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func validatePublicURL(ctx context.Context, raw string) error {
	if err := validateHTTPURL(raw); err != nil {
		return err
	}
	parsed, _ := url.Parse(raw)
	addresses, err := net.DefaultResolver.LookupIP(ctx, "ip", parsed.Hostname())
	if err != nil {
		return fmt.Errorf("resolve host: %w", err)
	}
	if len(addresses) == 0 {
		return errors.New("host did not resolve")
	}
	for _, address := range addresses {
		if !isPublicIP(address) {
			return fmt.Errorf("host resolves to non-public address %s", address)
		}
	}
	return nil
}

func publicDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, candidate := range addresses {
		if !isPublicIP(candidate) {
			return nil, fmt.Errorf("refusing non-public address %s", candidate)
		}
	}
	if len(addresses) == 0 {
		return nil, errors.New("host did not resolve")
	}
	dialer := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
}

func isPublicIP(ip net.IP) bool {
	return ip != nil &&
		!ip.IsLoopback() &&
		!ip.IsPrivate() &&
		!ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() &&
		!ip.IsUnspecified() &&
		!ip.IsMulticast()
}
