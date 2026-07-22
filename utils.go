package main

import (
"archive/zip"
"bytes"
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

func newCandidClient(referer string) *http.Client {
if referer == "" {
referer = "https://www.suicidegirls.com/"
}

headers := map[string]string{
"User-Agent":      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:150.0) Gecko/20100101 Firefox/150.0",
"Accept":          "image/avif,image/webp,image/png,image/svg+xml,video/*;q=0.8,*/*;q=0.5",
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

func saveImageWithReferer(imageURL string, output string, referer string) (int64, string, error) {
client := newCandidClient(referer)

req, err := http.NewRequest("GET", imageURL, nil)
if err != nil {
return 0, "", err
}

req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:150.0) Gecko/20100101 Firefox/150.0")
req.Header.Set("Accept", "image/avif,image/webp,image/png,image/svg+xml,video/*;q=0.8,*/*;q=0.5")
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
return 0, "", err
}
defer resp.Body.Close()

if resp.StatusCode < 200 || resp.StatusCode >= 300 {
return 0, "", fmt.Errorf("image request failed: %s", resp.Status)
}

sniffBuf := make([]byte, 512)
nSniff, err := io.ReadFull(resp.Body, sniffBuf)
if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
return 0, "", err
}
sniffBuf = sniffBuf[:nSniff]

contentType := strings.ToLower(http.DetectContentType(sniffBuf))
if strings.Contains(contentType, "octet-stream") || contentType == "" {
contentType = strings.ToLower(resp.Header.Get("Content-Type"))
}

ext := ".jpg"
if strings.Contains(contentType, "image/gif") {
ext = ".gif"
} else if strings.Contains(contentType, "image/webp") {
ext = ".webp"
} else if strings.Contains(contentType, "image/png") {
ext = ".png"
} else if strings.Contains(contentType, "video/mp4") {
ext = ".mp4"
} else if strings.Contains(contentType, "video/webm") {
ext = ".webm"
}

basePath := output
for _, badExt := range []string{".jpg", ".jpeg", ".gif", ".webp", ".mp4", ".png"} {
if strings.HasSuffix(strings.ToLower(basePath), badExt) {
basePath = basePath[:len(basePath)-len(badExt)]
break
}
}

finalOutput := basePath + ext
img, err := os.Create(finalOutput)
if err != nil {
return 0, "", err
}
defer img.Close()

bodyReader := io.MultiReader(bytes.NewReader(sniffBuf), resp.Body)
n, err := io.Copy(img, bodyReader)
if err != nil {
return n, finalOutput, err
}

if lastMod := resp.Header.Get("Last-Modified"); lastMod != "" {
if t, err := http.ParseTime(lastMod); err == nil {
os.Chtimes(finalOutput, t, t)
}
}

return n, finalOutput, nil
}

func saveImage(imageURL string, output string) (int64, string, error) {
client := newAuthedClient(imageURL)
req, err := http.NewRequest("GET", imageURL, nil)
if err != nil {
return 0, "", err
}

req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36")
req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,video/*,image/*,*/*;q=0.8")
req.Header.Set("Accept-Language", "en-US,en;q=0.5")
req.Header.Set("Referer", "https://www.suicidegirls.com/")

resp, err := client.Do(req)
if err != nil {
return 0, "", err
}
defer resp.Body.Close()

if resp.StatusCode < 200 || resp.StatusCode >= 300 {
return 0, "", fmt.Errorf("image request failed: %s", resp.Status)
}

sniffBuf := make([]byte, 512)
nSniff, err := io.ReadFull(resp.Body, sniffBuf)
if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
return 0, "", err
}
sniffBuf = sniffBuf[:nSniff]

contentType := strings.ToLower(http.DetectContentType(sniffBuf))
if strings.Contains(contentType, "octet-stream") || contentType == "" {
contentType = strings.ToLower(resp.Header.Get("Content-Type"))
}

ext := ".jpg"
if strings.Contains(contentType, "image/gif") {
ext = ".gif"
} else if strings.Contains(contentType, "image/webp") {
ext = ".webp"
} else if strings.Contains(contentType, "image/png") {
ext = ".png"
} else if strings.Contains(contentType, "video/mp4") {
ext = ".mp4"
} else if strings.Contains(contentType, "video/webm") {
ext = ".webm"
}

basePath := output
for _, badExt := range []string{".jpg", ".jpeg", ".gif", ".webp", ".mp4", ".png"} {
if strings.HasSuffix(strings.ToLower(basePath), badExt) {
basePath = basePath[:len(basePath)-len(badExt)]
break
}
}

finalOutput := basePath + ext
img, err := os.Create(finalOutput)
if err != nil {
return 0, "", err
}
defer img.Close()

bodyReader := io.MultiReader(bytes.NewReader(sniffBuf), resp.Body)
n, err := io.Copy(img, bodyReader)
if err != nil {
return n, finalOutput, err
}

if lastMod := resp.Header.Get("Last-Modified"); lastMod != "" {
if t, err := http.ParseTime(lastMod); err == nil {
os.Chtimes(finalOutput, t, t)
}
}

return n, finalOutput, nil
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
