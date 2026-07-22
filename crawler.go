package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	gohtml "golang.org/x/net/html"
)

var albumLinkPattern = regexp.MustCompile(`/((?:girls|members))/([^/]+)/album/(\d+)(?:/[^"'#?]+)?/?`)
var memberAlbumLinkPattern = regexp.MustCompile(`/members/([^/]+)/album/(\d+)(?:/[^"'#?]+)?/?`)
var blogLinkPattern = regexp.MustCompile(`/members/([^/]+)/blog/(\d+)(?:/[^"'#?]+)?/?`)
var videoLinkPattern = regexp.MustCompile(`/videos/(\d+)(?:/[^"'#?]+)?/?`)

// ── group thread ────────────────────────────────────────────────────────────
var memberHrefRe = regexp.MustCompile(`href="/(?:members|girls)/([^/"]+)/"`)
var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

type GroupThreadImageBucket struct {
	CommentID   string
	Username    string
	CommentText string
	Images      []string
}

func stripHTML(b []byte) string {
	plain := htmlTagRe.ReplaceAll(b, []byte(" "))
	decoded := html.UnescapeString(string(plain))
	return strings.Join(strings.Fields(decoded), " ")
}

var commentTextMarker = []byte(`<div class="comment-text" data-comment-id="`)
var dataOriginalRe = regexp.MustCompile(`data-original="(https?://[^"]+)"`)

func crawlGroupThreadImageBuckets(rawContents io.Reader) []GroupThreadImageBucket {
	rawBytes, err := io.ReadAll(rawContents)
	if err != nil {
		return nil
	}
	const marker = `<div class="flex-wrapper" data-comment-id="`
	parts := bytes.Split(rawBytes, []byte(marker))

	var buckets []GroupThreadImageBucket
	for _, part := range parts[1:] {
		idEnd := bytes.IndexByte(part, '"')
		if idEnd < 0 {
			continue
		}
		commentID := sanitizeName(string(part[:idEnd]))
		if commentID == "" {
			continue
		}
		ctIdx := bytes.Index(part, commentTextMarker)
		headerBlock := part
		if ctIdx > 0 {
			headerBlock = part[:ctIdx]
		}
		username := ""
		if um := memberHrefRe.FindSubmatch(headerBlock); len(um) > 1 {
			username = sanitizeName(string(um[1]))
		}
		if ctIdx < 0 {
			continue
		}
		openEnd := bytes.IndexByte(part[ctIdx:], '>')
		if openEnd < 0 {
			continue
		}
		body := part[ctIdx+openEnd+1:]
		if timeIdx := bytes.Index(body, []byte("<time>")); timeIdx > 0 {
			body = body[:timeIdx]
		}
		
		seen := map[string]bool{}
		var imgs []string

		// 1. Prioritize explicit high-fidelity animated loop attributes and video sources
		highFidelityPatterns := []*regexp.Regexp{
			regexp.MustCompile(`(?i)data-(?:mp4|gif|webm|animated|video)="([^"]+)"`),
			regexp.MustCompile(`(?i)<source[^>]+src="([^"]+)"`),
			regexp.MustCompile(`(?i)src="([^"]+\.(?:mp4|webm|gif|mov)[^"]*)"`),
		}

		for _, pattern := range highFidelityPatterns {
			matches := pattern.FindAllSubmatch(body, -1)
			for _, m := range matches {
				if len(m) < 2 {
					continue
				}
				u := html.UnescapeString(string(m[1]))
				if u != "" && !seen[u] {
					seen[u] = true
					imgs = append(imgs, u)
				}
			}
		}

		// 2. Fall back to standard original image source if no dynamic vectors exist
		if len(imgs) == 0 {
			matches := dataOriginalRe.FindAllSubmatch(body, -1)
			for _, m := range matches {
				if len(m) < 2 {
					continue
				}
				u := html.UnescapeString(string(m[1]))
				if u != "" && !seen[u] {
					seen[u] = true
					imgs = append(imgs, u)
				}
			}
		}

		if len(imgs) == 0 {
			continue
		}
		commentText := stripHTML(body)
		buckets = append(buckets, GroupThreadImageBucket{
			CommentID:   commentID,
			Username:    username,
			CommentText: commentText,
			Images:      imgs,
		})
	}
	return buckets
}

func getGroupThreadTotalComments(threadURL string) int {
	rawBytes, err := io.ReadAll(getContents(strings.TrimSuffix(threadURL, "/") + "/comments/all?lazy=1"))
	if err != nil {
		return -1
	}
	m := regexp.MustCompile(`(?i)(\d[\d,]*)\s+comment`).FindSubmatch(rawBytes)
	if len(m) < 2 {
		return -1
	}
	n := strings.ReplaceAll(string(m[1]), ",", "")
	v, err := strconv.Atoi(n)
	if err != nil {
		return -1
	}
	return v
}

func getAllGroupThreadImageBuckets(threadURL string) []GroupThreadImageBucket {
	seenImgs := map[string]map[string]bool{}
	bucketMeta := map[string]GroupThreadImageBucket{}
	var bucketOrder []string
	base := strings.TrimSuffix(threadURL, "/")
	baseURL := base + "/comments/all?lazy=1"
	totalComments := -1
	offset := 0
	useFallback := false
	for {
		var pageURL string
		if useFallback {
			pageURL = fmt.Sprintf("%s/comments/?offset=%d&count=600&lazy=1", base, offset)
		} else if offset == 0 {
			pageURL = baseURL
		} else {
			pageURL = fmt.Sprintf("%s&offset=%d", baseURL, offset)
		}
		fmt.Printf("[group] fetching offset=%d fallback=%t\n", offset, useFallback)
		pageSource := getContents(pageURL)
		rawBytes, err := io.ReadAll(pageSource)
		if err != nil {
			if useFallback {
				break
			}
			useFallback = true
			offset = 0
			if totalComments < 0 {
				totalComments = getGroupThreadTotalComments(threadURL)
				if totalComments > 0 {
					fmt.Printf("[group] discovered total comments: %d\n", totalComments)
				}
			}
			continue
		}
		pageBuckets := crawlGroupThreadImageBuckets(bytes.NewReader(rawBytes))
		pageNewImages := 0
		for _, bucket := range pageBuckets {
			key := bucket.CommentID
			if _, ok := seenImgs[key]; !ok {
				seenImgs[key] = map[string]bool{}
				bucketMeta[key] = GroupThreadImageBucket{
					CommentID:   bucket.CommentID,
					Username:    bucket.Username,
					CommentText: bucket.CommentText,
				}
				bucketOrder = append(bucketOrder, key)
			}
			meta := bucketMeta[key]
			if meta.Username == "" && bucket.Username != "" {
				meta.Username = bucket.Username
			}
			if meta.CommentText == "" && bucket.CommentText != "" {
				meta.CommentText = bucket.CommentText
			}
			bucketMeta[key] = meta
			for _, img := range bucket.Images {
				if !seenImgs[key][img] {
					seenImgs[key][img] = true
					pageNewImages++
				}
			}
		}
		seenPosts := len(bucketMeta)
		totalImages := 0
		for key := range bucketMeta {
			totalImages += len(seenImgs[key])
		}
		if totalComments > 0 {
			fmt.Printf("[group] progress: comments %d/%d, posts with images=%d, images=%d, last page posts=%d, last page new images=%d\n", offset, totalComments, seenPosts, totalImages, len(pageBuckets), pageNewImages)
		} else {
			fmt.Printf("[group] progress: comments offset=%d, posts with images=%d, images=%d, last page posts=%d, last page new images=%d\n", offset, seenPosts, totalImages, len(pageBuckets), pageNewImages)
		}
		if useFallback {
			if len(pageBuckets) == 0 {
				break
			}
			offset += 600
			if totalComments > 0 && offset >= totalComments {
				break
			}
			continue
		}
		nextOffset := getNextOffset(bytes.NewReader(rawBytes))
		if nextOffset < 0 {
			if len(pageBuckets) == 0 {
				useFallback = true
				offset = 0
				if totalComments < 0 {
					totalComments = getGroupThreadTotalComments(threadURL)
					if totalComments > 0 {
						fmt.Printf("[group] discovered total comments: %d\n", totalComments)
					}
				}
				continue
			}
			break
		}
		if nextOffset == offset {
			break
		}
		offset = nextOffset
	}
	var result []GroupThreadImageBucket
	for _, key := range bucketOrder {
		meta := bucketMeta[key]
		for img := range seenImgs[key] {
			meta.Images = append(meta.Images, img)
		}
		result = append(result, meta)
	}
	return result
}

// ── image scrapers ───────────────────────────────────────────────────────────

func crawlAlbumImages(rawContents io.Reader) []string {
	z := gohtml.NewTokenizer(rawContents)
	var imagesFound []string
	seen := map[string]bool{}
	for tt := z.Next(); ; tt = z.Next() {
		switch tt {
		case gohtml.ErrorToken:
			return imagesFound
		case gohtml.StartTagToken:
			t := z.Token()
			if t.Data != "a" {
				continue
			}
			link := html.UnescapeString(getValueFromAttribute(t, "href"))
			if strings.HasPrefix(link, "https") && strings.Contains(strings.ToLower(link), ".jpg") {
				if !seen[link] {
					seen[link] = true
					imagesFound = append(imagesFound, link)
				}
			}
		}
	}
}

func crawlCacheImages(rawContents io.Reader) []string {
	z := gohtml.NewTokenizer(rawContents)
	var imagesFound []string
	seen := map[string]bool{}
	inImageSection := false
	depth := 0
	inArticle := false
	for tt := z.Next(); ; tt = z.Next() {
		if tt == gohtml.ErrorToken {
			return imagesFound
		}
		switch tt {
		case gohtml.StartTagToken, gohtml.SelfClosingTagToken:
			t := z.Token()
			switch t.Data {
			case "article":
				if !inArticle {
					inArticle = true
					depth = 1
				} else {
					depth++
				}
			case "section":
				if !inArticle {
					continue
				}
				for _, a := range t.Attr {
					if a.Key == "class" && strings.Contains(a.Val, "image-section") {
						inImageSection = true
						break
					}
				}
			case "noscript":
				if !inImageSection {
					continue
				}
				raw := getValueFromAttribute(t, "data-retina")
				if raw == "" {
					raw = getValueFromAttribute(t, "data-src")
				}
				if raw == "" {
					continue
				}
				u := html.UnescapeString(raw)
				if u != "" && !seen[u] {
					seen[u] = true
					imagesFound = append(imagesFound, u)
				}
			}
		case gohtml.EndTagToken:
			t := z.Token()
			switch t.Data {
			case "section":
				inImageSection = false
			case "article":
				if inArticle {
					depth--
					if depth == 0 {
						return imagesFound
					}
				}
			}
		}
	}
}

func getAlbumInfoImages(postID string) []string {
	apiURL := fmt.Sprintf("https://www.suicidegirls.com/api/get_album_info/%s/?geometries=2432,1216", postID)
	client := newAuthedClient(apiURL)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:150.0) Gecko/20100101 Firefox/150.0")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Referer", "https://www.suicidegirls.com/")
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return nil
	}
	defer resp.Body.Close()
	var result struct {
		Photos []struct {
			AlbumPhotoID int               `json:"album_photo_id"`
			URLs         map[string]string `json:"urls"`
		} `json:"photos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}
	if len(result.Photos) <= 1 {
		return nil
	}
	photos := result.Photos
	for i := 1; i < len(photos); i++ {
		for j := i; j > 0 && photos[j].AlbumPhotoID < photos[j-1].AlbumPhotoID; j-- {
			photos[j], photos[j-1] = photos[j-1], photos[j]
		}
	}
	var images []string
	for _, p := range photos {
		if u := p.URLs["2432"]; u != "" {
			images = append(images, u)
		} else if u := p.URLs["1216"]; u != "" {
			images = append(images, u)
		}
	}
	return images
}

func crawlCandidImages(rawContents io.Reader) []string {
	z := gohtml.NewTokenizer(rawContents)
	var imagesFound []string
	seen := map[string]bool{}
	for tt := z.Next(); ; tt = z.Next() {
		if tt == gohtml.ErrorToken {
			return imagesFound
		}
		if tt != gohtml.StartTagToken {
			continue
		}
		t := z.Token()
		if t.Data != "a" {
			continue
		}
		raw := getValueFromAttribute(t, "data-picture-url")
		if raw == "" {
			continue
		}
		u := html.UnescapeString(raw)
		if u != "" && !seen[u] {
			seen[u] = true
			imagesFound = append(imagesFound, u)
		}
	}
}

func crawlBlogImagesRegex(rawBytes []byte) []string {
	seen := map[string]bool{}
	var result []string
	for _, m := range dataOriginalRe.FindAllSubmatch(rawBytes, -1) {
		if len(m) < 2 {
			continue
		}
		u := html.UnescapeString(string(m[1]))
		if u != "" && !seen[u] {
			seen[u] = true
			result = append(result, u)
		}
	}
	return result
}

// ── video stream ─────────────────────────────────────────────────────────────

var videoSourcesRe = regexp.MustCompile(`"sources"\s*:\s*(\[[^\]]+\])`)
var videoFileRe = regexp.MustCompile(`"file"\s*:\s*"([^"]+)"`)
var hlsURLRe = regexp.MustCompile(`https?://[^"'<>\s]+\.m3u8(?:\?[^"'<>\s]*)?`)
var mp4URLRe = regexp.MustCompile(`https?://[^"'<>\s]+\.mp4(?:\?[^"'<>\s]*)?`)

func crawlVideoStream(rawContents io.Reader) string {
	rawBytes, err := io.ReadAll(rawContents)
	if err != nil {
		return ""
	}
	if m := videoSourcesRe.FindSubmatch(rawBytes); len(m) > 1 {
		var sources []map[string]any
		if err := json.Unmarshal(m[1], &sources); err == nil {
			for _, src := range sources {
				if file, ok := src["file"].(string); ok && strings.Contains(file, ".m3u8") {
					return html.UnescapeString(file)
				}
			}
			for _, src := range sources {
				if file, ok := src["file"].(string); ok && file != "" {
					return html.UnescapeString(file)
				}
			}
		}
	}
	if m := videoFileRe.FindSubmatch(rawBytes); len(m) > 1 {
		return html.UnescapeString(string(m[1]))
	}
	if m := hlsURLRe.Find(rawBytes); len(m) > 0 {
		return html.UnescapeString(string(m))
	}
	if m := mp4URLRe.Find(rawBytes); len(m) > 0 {
		return html.UnescapeString(string(m))
	}
	return ""
}

// ── link crawlers ─────────────────────────────────────────────────────────────

func crawlAlbums(rawContents io.Reader, modelName string) []string {
	z := gohtml.NewTokenizer(rawContents)
	var albumsFound []string
	seen := map[string]bool{}
	for tt := z.Next(); ; tt = z.Next() {
		if tt == gohtml.ErrorToken {
			return albumsFound
		}
		if tt != gohtml.StartTagToken {
			continue
		}
		t := z.Token()
		if t.Data != "a" {
			continue
		}
		link := getValueFromAttribute(t, "href")
		if link == "" {
			continue
		}
		if !strings.HasPrefix(link, "/") && !strings.HasPrefix(link, "https://www.suicidegirls.com") {
			continue
		}
		m := albumLinkPattern.FindStringSubmatch(link)
		if m == nil || (modelName != "" && m[2] != modelName) {
			continue
		}
		if !strings.HasPrefix(link, "http") {
			link = "https://www.suicidegirls.com" + link
		}
		if !seen[link] {
			seen[link] = true
			albumsFound = append(albumsFound, link)
		}
	}
}

func crawlMemberAlbums(rawContents io.Reader, modelName string) []string {
	z := gohtml.NewTokenizer(rawContents)
	var found []string
	seen := map[string]bool{}
	for tt := z.Next(); ; tt = z.Next() {
		if tt == gohtml.ErrorToken {
			return found
		}
		if tt != gohtml.StartTagToken {
			continue
		}
		t := z.Token()
		if t.Data != "a" {
			continue
		}
		link := getValueFromAttribute(t, "href")
		if link == "" {
			continue
		}
		if !strings.HasPrefix(link, "/") && !strings.HasPrefix(link, "https://www.suicidegirls.com") {
			continue
		}
		m := memberAlbumLinkPattern.FindStringSubmatch(link)
		if m == nil || (modelName != "" && m[1] != modelName) {
			continue
		}
		if !strings.HasPrefix(link, "http") {
			link = "https://www.suicidegirls.com" + link
		}
		if !seen[link] {
			seen[link] = true
			found = append(found, link)
		}
	}
}

func crawlBlogLinks(rawContents io.Reader, modelName string) []string {
	z := gohtml.NewTokenizer(rawContents)
	var found []string
	seen := map[string]bool{}
	for tt := z.Next(); ; tt = z.Next() {
		if tt == gohtml.ErrorToken {
			return found
		}
		if tt != gohtml.StartTagToken {
			continue
		}
		t := z.Token()
		if t.Data != "a" {
			continue
		}
		link := getValueFromAttribute(t, "href")
		if link == "" {
			continue
		}
		if !strings.HasPrefix(link, "/") && !strings.HasPrefix(link, "https://www.suicidegirls.com") {
			continue
		}
		m := blogLinkPattern.FindStringSubmatch(link)
		if m == nil || (modelName != "" && m[1] != modelName) {
			continue
		}
		if !strings.HasPrefix(link, "http") {
			link = "https://www.suicidegirls.com" + link
		}
		if !seen[link] {
			seen[link] = true
			found = append(found, link)
		}
	}
}

func crawlVideoLinks(rawContents io.Reader) []string {
	z := gohtml.NewTokenizer(rawContents)
	var found []string
	seen := map[string]bool{}
	for tt := z.Next(); ; tt = z.Next() {
		if tt == gohtml.ErrorToken {
			return found
		}
		if tt != gohtml.StartTagToken {
			continue
		}
		t := z.Token()
		if t.Data != "a" {
			continue
		}
		link := getValueFromAttribute(t, "href")
		if link == "" {
			continue
		}
		if !strings.HasPrefix(link, "/") && !strings.HasPrefix(link, "https://www.suicidegirls.com") {
			continue
		}
		if videoLinkPattern.FindStringSubmatch(link) == nil {
			continue
		}
		if !strings.HasPrefix(link, "http") {
			link = "https://www.suicidegirls.com" + link
		}
		if !seen[link] {
			seen[link] = true
			found = append(found, link)
		}
	}
}

// ── paginated sweeps ──────────────────────────────────────────────────────────

func getAllAlbumLinks(modelURL string, modelName string) []string {
	var all []string
	seen := map[string]bool{}
	offset := 0
	for {
		var pageURL string
		if offset == 0 {
			pageURL = modelURL
		} else {
			pageURL = fmt.Sprintf("%s?offset=%d", modelURL, offset)
		}
		pageSource := getContents(pageURL)
		rawBytes, _ := io.ReadAll(pageSource)
		for _, link := range crawlAlbums(bytes.NewReader(rawBytes), modelName) {
			if !seen[link] {
				seen[link] = true
				all = append(all, link)
			}
		}
		nextOffset := getNextOffset(bytes.NewReader(rawBytes))
		if nextOffset < 0 {
			break
		}
		offset = nextOffset
	}
	return all
}

func getAllMemberAlbumLinks(modelURL string, modelName string) []string {
	var all []string
	seen := map[string]bool{}
	offset := 0
	for {
		var pageURL string
		if offset == 0 {
			pageURL = modelURL
		} else {
			pageURL = fmt.Sprintf("%s?offset=%d", modelURL, offset)
		}
		pageSource := getContents(pageURL)
		rawBytes, _ := io.ReadAll(pageSource)
		for _, link := range crawlMemberAlbums(bytes.NewReader(rawBytes), modelName) {
			if !seen[link] {
				seen[link] = true
				all = append(all, link)
			}
		}
		nextOffset := getNextOffset(bytes.NewReader(rawBytes))
		if nextOffset < 0 {
			break
		}
		offset = nextOffset
	}
	return all
}

func getAllBlogLinks(modelURL string, modelName string) []string {
	var all []string
	seen := map[string]bool{}
	offset := 0
	for {
		var pageURL string
		if offset == 0 {
			pageURL = modelURL
		} else {
			pageURL = fmt.Sprintf("%s?offset=%d", modelURL, offset)
		}
		pageSource := getContents(modelURL) // fallback safety
		pageSource = getContents(pageURL)
		rawBytes, _ := io.ReadAll(pageSource)
		for _, link := range crawlBlogLinks(bytes.NewReader(rawBytes), modelName) {
			if !seen[link] {
				seen[link] = true
				all = append(all, link)
			}
		}
		nextOffset := getNextOffset(bytes.NewReader(rawBytes))
		if nextOffset < 0 {
			break
		}
		offset = nextOffset
	}
	return all
}

func getAllVideoLinks(modelURL string) []string {
	var all []string
	seen := map[string]bool{}
	offset := 0
	for {
		var pageURL string
		if offset == 0 {
			pageURL = modelURL
		} else {
			pageURL = fmt.Sprintf("%s?offset=%d", modelURL, offset)
		}
		pageSource := getContents(pageURL)
		rawBytes, _ := io.ReadAll(pageSource)
		for _, link := range crawlVideoLinks(bytes.NewReader(rawBytes)) {
			if !seen[link] {
				seen[link] = true
				all = append(all, link)
			}
		}
		nextOffset := getNextOffset(bytes.NewReader(rawBytes))
		if nextOffset < 0 {
			break
		}
		offset = nextOffset
	}
	return all
}

// ── page helpers ──────────────────────────────────────────────────────────────

func getTitle(rawContents io.Reader) string {
	z := gohtml.NewTokenizer(rawContents)
	for tt := z.Next(); ; tt = z.Next() {
		switch tt {
		case gohtml.ErrorToken:
			return ""
		case gohtml.StartTagToken:
			t := z.Token()
			if t.Data != "title" {
				continue
			}
			if z.Next() == gohtml.TextToken {
				return z.Token().Data
			}
		}
	}
}

type PageInfo struct {
	ModelName string
	PostName  string
	AlbumName string
	IsCandid  bool
}

func parsePageInfo(rawTitle string) PageInfo {
	cleanTitle := strings.TrimSpace(rawTitle)
	if idx := strings.Index(cleanTitle, "Photo Album"); idx != -1 {
		model := strings.TrimSpace(strings.TrimRight(cleanTitle[:idx], " -:|"))
		after := strings.TrimSpace(cleanTitle[idx+len("Photo Album"):])
		name := strings.TrimSpace(strings.TrimLeft(after, " -:|"))
		name = strings.TrimSpace(strings.SplitN(name, " | ", 2)[0])
		if byIdx := strings.LastIndex(name, " by "); byIdx != -1 {
			name = strings.TrimSpace(name[:byIdx])
		}
		return PageInfo{ModelName: sanitizeName(model), AlbumName: sanitizeName(name)}
	}
	if idx := strings.LastIndex(cleanTitle, " by "); idx != -1 {
		postName := strings.TrimSpace(cleanTitle[:idx])
		rest := cleanTitle[idx+len(" by "):]
		model := strings.TrimSpace(strings.SplitN(rest, " | ", 2)[0])
		return PageInfo{ModelName: sanitizeName(model), PostName: sanitizeName(postName), IsCandid: true}
	}
	fmt.Println("Warning: unrecognised title format:", rawTitle)
	return PageInfo{ModelName: sanitizeName(cleanTitle)}
}

func sanitizeName(s string) string {
	replacer := strings.NewReplacer(
		"/", "-", `\`, "-", ":", "-", "*", "-",
		"?", "", `"`, "", "<", "", ">", "", "|", "-",
	)
	s = replacer.Replace(s)

	// Filter out characters outside the BMP (strips modern high-plane emojis)
	var sb strings.Builder
	for _, r := range s {
		if r <= 0xFFFF {
			sb.WriteRune(r)
		}
	}
	s = sb.String()

	s = strings.TrimSpace(s)
	// Remove trailing periods which completely break Windows/SMB directory specifications
	s = strings.TrimRight(s, ".")
	return strings.TrimSpace(s)
}

func truncateName(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return strings.TrimSpace(string(runes[:maxLen]))
}

func getAlbumDate(rawContents io.Reader) (time.Time, error) {
	z := gohtml.NewTokenizer(rawContents)
	for tt := z.Next(); ; tt = z.Next() {
		if tt == gohtml.ErrorToken {
			break
		}
		if tt == gohtml.StartTagToken {
			t := z.Token()
			if t.Data == "time" {
				if z.Next() == gohtml.TextToken {
					text := strings.TrimSpace(z.Token().Data)
					if text != "" {
						if parsed, err := time.Parse("Jan 2, 2006", text); err == nil {
							return parsed, nil
						}
						if parsed, err := time.Parse("Jan 2", text); err == nil {
							return parsed.AddDate(time.Now().Year(), 0, 0), nil
						}
					}
				}
			}
		}
	}
	return time.Time{}, fmt.Errorf("date not found in page")
}

func newAuthedClient(target string) http.Client {
	sessionidCookie := os.Getenv("SESSIONIDTOKEN")
	sgcsrftoken := os.Getenv("SGCSRFTOKEN")
	rsciVid := os.Getenv("RSCIVID")

	jar, _ := cookiejar.New(nil)
	cookieData := []struct{ name, value string }{
		{"sessid", sessionidCookie},
		{"sgcsrftoken", sgcsrftoken},
		{"rscivid", rsciVid},
	}
	var cookies []*http.Cookie
	for _, c := range cookieData {
		if c.value == "" {
			continue
		}
		cookies = append(cookies, &http.Cookie{
			Name: c.name, Value: c.value,
			Path: "/", Domain: ".suicidegirls.com",
		})
	}
	for _, base := range []string{target, "https://www.suicidegirls.com", "https://suicidegirls.com"} {
		if u, err := url.Parse(base); err == nil {
			jar.SetCookies(u, cookies)
		}
	}
	return http.Client{Jar: jar}
}

func getContents(link string) io.Reader {
	client := newAuthedClient(link)
	req, _ := http.NewRequest("GET", link, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Referer", "https://www.suicidegirls.com/")
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	return resp.Body
}

func getValueFromAttribute(t gohtml.Token, attr string) string {
	for _, a := range t.Attr {
		if a.Key == attr {
			return a.Val
		}
	}
	return ""
}

func getNextOffset(rawContents io.Reader) int {
	z := gohtml.NewTokenizer(rawContents)
	for tt := z.Next(); ; tt = z.Next() {
		if tt == gohtml.ErrorToken {
			return -1
		}
		if tt != gohtml.StartTagToken && tt != gohtml.SelfClosingTagToken {
			continue
		}
		t := z.Token()
		if t.Data != "link" {
			continue
		}
		if getValueFromAttribute(t, "rel") == "next" {
			href := getValueFromAttribute(t, "href")
			if idx := strings.Index(href, "offset="); idx != -1 {
				raw := href[idx+len("offset="):]
				if amp := strings.IndexByte(raw, '&'); amp != -1 {
					raw = raw[:amp]
				}
				if n, err := strconv.Atoi(raw); err == nil {
					return n
				}
			}
		}
	}
}
