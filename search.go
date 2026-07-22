package main

import (
"bytes"
"fmt"
"io"
"net/url"
"os"
"regexp"
"strings"
"sync"
)

// discoverThreadsViaSearch queries the specific search endpoints to extract thread links.
func discoverThreadsViaSearch(query string) []string {
escapedQuery := url.QueryEscape(query)

baseEndpoints := []string{
fmt.Sprintf("https://www.suicidegirls.com/search/?category=threads&s=%s&g=", escapedQuery),
fmt.Sprintf("https://www.suicidegirls.com/search/?s=%s", escapedQuery),
fmt.Sprintf("https://www.suicidegirls.com/api/search/?category=threads&s=%s&g=", escapedQuery),
fmt.Sprintf("https://www.suicidegirls.com/api/search/?s=%s", escapedQuery),
}

patterns := []*regexp.Regexp{
regexp.MustCompile(`(?i)/groups\\?/([^/'"\s>\\]+)\\?/thread\\?/(\d+)`),
regexp.MustCompile(`(?i)/groups\\?/thread\\?/([^/'"\s>\\]+)\\?/(\d+)`),
regexp.MustCompile(`(?i)"thread_id"[\s:]+["']?(\d+)["']?`), 
}

var threads []string
seen := map[string]bool{}

for idx, baseStr := range baseEndpoints {
for p := 0; p < 5; p++ {
var searchURL string
if strings.Contains(baseStr, "?") {
searchURL = fmt.Sprintf("%s&page=%d&offset=%d", baseStr, p+1, p*24)
} else {
searchURL = fmt.Sprintf("%s?page=%d&offset=%d", baseStr, p+1, p*24)
}

fmt.Printf("[global-search] Probing matrix endpoint [%d/%d] (Page %d): %s\n", idx+1, len(baseEndpoints), p+1, searchURL)
pageSource := getContents(searchURL)
rawBytes, err := io.ReadAll(pageSource)
if err != nil {
break
}

newThreadsFoundOnPage := 0
for _, pattern := range patterns {
matches := pattern.FindAllSubmatch(rawBytes, -1)
for _, m := range matches {
var link string

if len(m) == 3 {
firstMatch := strings.ReplaceAll(string(m[1]), "\\", "")
secondMatch := string(m[2])

if regexp.MustCompile(`^\d+$`).MatchString(secondMatch) {
link = fmt.Sprintf("https://www.suicidegirls.com/groups/%s/thread/%s/", firstMatch, secondMatch)
} else {
link = fmt.Sprintf("https://www.suicidegirls.com/groups/thread/%s/%s/", secondMatch, firstMatch)
}
} else if len(m) == 2 {
threadID := string(m[1])
link = fmt.Sprintf("https://www.suicidegirls.com/groups/group/thread/%s/", threadID)
}

if link != "" && !seen[link] {
seen[link] = true
threads = append(threads, link)
newThreadsFoundOnPage++
}
}
}

if newThreadsFoundOnPage == 0 && p > 0 {
break
}
}
}
return threads
}

// executeSearchAndDownload streams comments, cross-references identities, and isolates/downloads matching images.
func executeSearchAndDownload(threadURL string, targetUser string, downloadsDir string) int {
targetLower := strings.ToLower(strings.TrimSpace(targetUser))

normalizedURL := threadURL
if strings.Contains(threadURL, "/all/") {
normalizedURL = strings.Replace(threadURL, "/all/", "/thread/", 1)
}

threadID := "thread"
if m := regexp.MustCompile(`\d+`).FindAllString(threadURL, -1); len(m) > 0 {
threadID = m[len(m)-1]
}

groupName := "group"
urlClean := strings.ReplaceAll(threadURL, "https://", "")
urlClean = strings.ReplaceAll(urlClean, "http://", "")
urlClean = strings.ReplaceAll(urlClean, "www.suicidegirls.com", "")
parts := strings.Split(urlClean, "/")
for _, p := range parts {
pClean := strings.TrimSpace(p)
if pClean == "" || pClean == "groups" || pClean == "thread" || pClean == "all" || regexp.MustCompile(`^\d+$`).MatchString(pClean) {
continue
}
groupName = sanitizeName(pClean)
break
}

fmt.Printf("[search] Target Context Isolated — ID: %s | Group Layout: %s\n", threadID, groupName)
fmt.Printf("[search] Launching comments stream parsing sequence...\n")

threadPageSource := getContents(normalizedURL)
threadBytes, err := io.ReadAll(threadPageSource)
threadTitle := threadID
if err == nil {
rawTitle := strings.TrimSpace(strings.Split(getTitle(bytes.NewReader(threadBytes)), " by ")[0])
sanitizedTitle := sanitizeName(rawTitle)
if sanitizedTitle != "" {
threadTitle = sanitizedTitle
}
}
threadTitle = truncateName(threadTitle, 60)

threadDir := fmt.Sprintf("%s/search/%s/%s/%s - %s", downloadsDir, targetUser, groupName, threadID, threadTitle)

buckets := getAllGroupThreadImageBuckets(normalizedURL)

matchCount := 0
seenUrls := map[string]bool{}

checkAndCreateDir(threadDir)
existingEntries, _ := os.ReadDir(threadDir)

fmt.Printf("[search-diagnostics] Scanning %d total parsed comment blocks...\n", len(buckets))

for _, bucket := range buckets {
poster := bucket.Username
commentText := bucket.CommentText

posterClean := strings.ToLower(strings.TrimSpace(poster))
commentClean := strings.ToLower(commentText)

isAuthor := strings.EqualFold(posterClean, targetLower)
isMentioned := strings.Contains(commentClean, targetLower)

if isAuthor || isMentioned {
if len(bucket.Images) == 0 {
continue
}

// Core Deduplication Check: Log skips explicitly so execution visibility is clear
prefix := bucket.CommentID + " - "
alreadyOnDisk := false
for _, e := range existingEntries {
if strings.HasPrefix(e.Name(), prefix) {
alreadyOnDisk = true
break
}
}
if alreadyOnDisk {
fmt.Printf("[skip] Search thread match %s — comment %s already exists on disk\n", threadID, bucket.CommentID)
continue
}

var newImages []string
for _, img := range bucket.Images {
if !seenUrls[img] {
seenUrls[img] = true
newImages = append(newImages, img)
}
}

if len(newImages) == 0 {
continue
}

matchCount++

commentSnippet := truncateName(sanitizeName(commentText), 60)
var baseName string
if commentSnippet != "" {
baseName = fmt.Sprintf("%s - %s - %s", bucket.CommentID, poster, commentSnippet)
} else {
baseName = fmt.Sprintf("%s - %s", bucket.CommentID, poster)
}

total := len(newImages)
var wg sync.WaitGroup
var mu sync.Mutex

for i, imageURL := range newImages {
wg.Add(1)
go func(idx int, urlStr string) {
defer wg.Done()
imageOutputBase := fmt.Sprintf("%s/%s - %04d", threadDir, baseName, idx+1)
b, _, err := saveImage(urlStr, imageOutputBase)
mu.Lock()
defer mu.Unlock()
if err != nil {
fmt.Printf("[search error] %s [%04d/%04d] — %v\n", baseName, idx+1, total, err)
return
}
fmt.Printf("[downloaded] %s [%04d/%04d] — %.2f MB\n", baseName, idx+1, total, float64(b)/1024/1024)
}(i, imageURL)
}
wg.Wait()
}
}

fmt.Printf("[search] Thread processed fully. Total matched comments saved: %d\n", matchCount)
return matchCount
}

// runGlobalSearch leverages the platform search engine to scrape all matching elements globally.
func runGlobalSearch(targetUser string, downloadsDir string) {
fmt.Printf("[global-search] Querying platform indices for: %s\n", targetUser)
threads := discoverThreadsViaSearch(targetUser)

if len(threads) == 0 {
fmt.Println("[global-search] Query completed. No group thread intersections detected.")
return
}

fmt.Printf("[global-search] Isolated %d candidate threads. Launching collection scans...\n", len(threads))
fmt.Println(strings.Repeat("=", 80))

totalHits := 0
for idx, threadURL := range threads {
fmt.Printf("\n[global-search] Scanning Thread [%d/%d]: %s\n", idx+1, len(threads), threadURL)
hits := executeSearchAndDownload(threadURL, targetUser, downloadsDir)
totalHits += hits
}

fmt.Println(strings.Repeat("=", 80))
fmt.Printf("[global-search] Operation finalized. Total processed matches: %d\n", totalHits)
}
