package main

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func checkAndCreateDir(path string) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		os.MkdirAll(path, os.ModePerm)
	}
}

func digitsLen(n int) int {
	return len(strconv.Itoa(n))
}

func leftPad(s string, padStr string, pLen int) string {
	return strings.Repeat(padStr, pLen-len(s)) + s
}

// newCandidClient returns an http.Client that:
//   - carries the session cookies (so /temp/ signed URLs are authorised)
//   - re-injects all request headers on every redirect hop (so Referer and
//     the browser fingerprint survive the /temp/ → /cache/ 302 bounce)
func newCandidClient(referer string) *http.Client {
	if referer == "" {
		referer = "https://www.suicidegirls.com/"
	}

	headers := map[string]string{
		"User-Agent":      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:150.0) Gecko/20100101 Firefox/150.0",
		"Accept":          "image/avif,image/webp,image/png,image/svg+xml,image/*;q=0.8,*/*;q=0.5",
		"Accept-Language": "en-US,en;q=0.9",
		"Referer":         referer,
		"Sec-Fetch-Dest":  "image",
		"Sec-Fetch-Mode":  "no-cors",
		"Sec-Fetch-Site":  "cross-site",
		"Priority":        "u=4, i",
		"Pragma":          "no-cache",
		"Cache-Control":   "no-cache",
	}

	jar, _ := cookiejar.New(nil)
	cookieData := []struct{ name, value string }{
		{"sessionid", os.Getenv("SESSIONIDTOKEN")},
		{"sgcsrftoken", os.Getenv("SGCSRFTOKEN")},
		{"rscivid", os.Getenv("RSCIVID")},
	}

	var cookies []*http.Cookie
	for _, c := range cookieData {
		if c.value == "" {
			continue
		}
		cookies = append(cookies, &http.Cookie{
			Name:   c.name,
			Value:  c.value,
			Path:   "/",
			Domain: ".suicidegirls.com",
		})
	}

	for _, base := range []string{"https://www.suicidegirls.com", "https://suicidegirls.com"} {
		if u, err := url.Parse(base); err == nil {
			jar.SetCookies(u, cookies)
		}
	}

	return &http.Client{
		Timeout: 120 * time.Second,
		Jar:     jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 10 {
				return fmt.Errorf("too many redirects")
			}
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			return nil
		},
	}
}

// saveImageWithReferer downloads a candid image.
// Uses session cookies (for /temp/ auth) and preserves headers across
// the /temp/ → /cache/ redirect so CloudFront accepts the final request.
func saveImageWithReferer(imageURL string, output string, referer string) (int64, error) {
	client := newCandidClient(referer)

	req, err := http.NewRequest("GET", imageURL, nil)
	if err != nil {
		return 0, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:150.0) Gecko/20100101 Firefox/150.0")
	req.Header.Set("Accept", "image/avif,image/webp,image/png,image/svg+xml,image/*;q=0.8,*/*;q=0.5")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if referer == "" {
		referer = "https://www.suicidegirls.com/"
	}
	req.Header.Set("Referer", referer)
	req.Header.Set("Sec-Fetch-Dest", "image")
	req.Header.Set("Sec-Fetch-Mode", "no-cors")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Priority", "u=4, i")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Printf(" [HTTP %d] %s\n", resp.StatusCode, imageURL)
		return 0, fmt.Errorf("image request failed: %s", resp.Status)
	}

	if ct := resp.Header.Get("Content-Type"); strings.Contains(strings.ToLower(ct), "text/html") {
		return 0, fmt.Errorf("image URL returned HTML (likely ad redirect): %s", ct)
	}

	img, err := os.Create(output)
	if err != nil {
		return 0, err
	}
	defer img.Close()

	n, err := io.Copy(img, resp.Body)
	if err != nil {
		return n, err
	}

	if lastMod := resp.Header.Get("Last-Modified"); lastMod != "" {
		if t, err := http.ParseTime(lastMod); err == nil {
			os.Chtimes(output, t, t)
		}
	}

	return n, nil
}

// saveImage is used for proper album sets, blog images, and group threads.
func saveImage(imageURL string, output string) (int64, error) {
	client := newAuthedClient(imageURL)
	req, err := http.NewRequest("GET", imageURL, nil)
	if err != nil {
		return 0, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Referer", "https://www.suicidegirls.com/")

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("image request failed: %s", resp.Status)
	}

	if ct := resp.Header.Get("Content-Type"); strings.Contains(strings.ToLower(ct), "text/html") {
		return 0, fmt.Errorf("image URL returned HTML (likely ad redirect): %s", ct)
	}

	img, err := os.Create(output)
	if err != nil {
		return 0, err
}
	defer img.Close()

	n, err := io.Copy(img, resp.Body)
	if err != nil {
		return n, err
	}

	if lastMod := resp.Header.Get("Last-Modified"); lastMod != "" {
		if t, err := http.ParseTime(lastMod); err == nil {
			os.Chtimes(output, t, t)
		}
	}

	return n, nil
}

func ZipFiles(filename string, files []string) error {
	newfile, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer newfile.Close()

	zipWriter := zip.NewWriter(newfile)
	defer zipWriter.Close()

	for _, file := range files {
		if file == "" {
			continue
		}

		zipfile, err := os.Open(file)
		if err != nil {
			return err
		}

		info, err := zipfile.Stat()
		if err != nil {
			zipfile.Close()
			return err
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			zipfile.Close()
			return err
		}

		header.Name = filepath.Base(file)
		header.Method = zip.Deflate

		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			zipfile.Close()
			return err
		}

		if _, err = io.Copy(writer, zipfile); err != nil {
			zipfile.Close()
			return err
		}

		zipfile.Close()
	}

	return nil
}

// getBucket groups resources under a single alphabetical character tree structure.
func getBucket(name string) string {
	if name == "" {
		return "#"
	}
	first := strings.ToUpper(string([]rune(name)[0]))
	if first >= "A" && first <= "Z" {
		return first
	}
	return "#"
}