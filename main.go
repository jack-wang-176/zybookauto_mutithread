package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// ---------------------------------------------------------
// Struct Definitions
// ---------------------------------------------------------

type ZySession struct {
	Token       string
	UserID      int
	Client      *http.Client
	TimeSpoofed int
	mu          sync.Mutex
}

type LoginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginResp struct {
	Success bool `json:"success"`
	Session struct {
		Token string `json:"auth_token"`
	} `json:"session"`
	User struct {
		UserID int `json:"user_id"`
	} `json:"user"`
}

type ZybookResp struct {
	Success bool `json:"success"`
	Item    struct {
		ZyBooks []ZyBook `json:"zybooks"`
	} `json:"items"`
}

type ZyBook struct {
	AutoSubscribe bool   `json:"autoSubscribe"`
	ZyBookID      int    `json:"zybook_id"`
	ZyBookCode    string `json:"zybook_code"`
	Title         string `json:"title"`
}

type Chapter struct {
	Title    string    `json:"title"`
	Number   int       `json:"number"`
	Sections []Section `json:"sections"`
}

type Section struct {
	Number                 int `json:"number"`
	CanonicalSectionID     int `json:"canonical_section_id"`
	CanonicalSectionNumber int `json:"canonical_section_number"`
}

type ContentResource struct {
	ID    int    `json:"id"`
	Type  string `json:"type"`
	Parts int    `json:"parts"`
}

type ZybookSectionContent struct {
	Success bool `json:"success"`
	Section struct {
		ContentResources []ContentResource `json:"content_resources"`
	} `json:"section"`
}

// ---------------------------------------------------------
// Core Functions
// ---------------------------------------------------------

func (s *ZySession) Login(yourAccount LoginReq) error {
	loginURL := "https://zyserver.zybooks.com/v1/signin"

	jsonData, err := json.Marshal(yourAccount)
	if err != nil {
		return fmt.Errorf("failed to marshal login payload: %w", err)
	}

	req, err := http.NewRequest("POST", loginURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send login request: %w", err)
	}
	defer resp.Body.Close()

	var loginResp LoginResp
	if err := json.NewDecoder(resp.Body).Decode(&loginResp); err != nil {
		return fmt.Errorf("failed to decode login response: %w", err)
	}

	if !loginResp.Success {
		return fmt.Errorf("failed to login: invalid credentials or server rejected")
	}

	s.Token = loginResp.Session.Token
	s.UserID = loginResp.User.UserID
	return nil
}

func (s *ZySession) GetBooks() ([]ZyBook, error) {
	userIDStr := strconv.Itoa(s.UserID)
	urlStr := "https://zyserver.zybooks.com/v1/user/" + userIDStr + "/items?items=%5B%22zybooks%22%5D"

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create get books request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)

	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send get books request: %w", err)
	}
	defer resp.Body.Close()

	var zybookResp ZybookResp
	if err := json.NewDecoder(resp.Body).Decode(&zybookResp); err != nil {
		return nil, fmt.Errorf("failed to decode books response: %w", err)
	}

	if !zybookResp.Success {
		return nil, fmt.Errorf("failed to fetch books: server returned success=false")
	}

	var zyBooks []ZyBook
	for _, zybook := range zybookResp.Item.ZyBooks {
		if !zybook.AutoSubscribe {
			zyBooks = append(zyBooks, zybook)
		}
	}
	return zyBooks, nil
}

func (s *ZySession) GetSections(zyBookCode string) ([]Chapter, error) {
	safeComponent := url.PathEscape(zyBookCode)

	// Fetch books overview which inherently contains the chapters structure
	urlStr := "https://zyserver.zybooks.com/v1/zybooks?zybooks=%5B%22" + safeComponent + "%22%5D"

	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)

	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Anonymous struct mapped directly to the nested JSON hierarchy
	var zybookResp struct {
		Zybooks []struct {
			Chapters []Chapter `json:"chapters"`
		} `json:"zybooks"`
	}

	if err = json.NewDecoder(resp.Body).Decode(&zybookResp); err != nil {
		return nil, err
	}

	// Safety check to prevent index out of bounds
	if len(zybookResp.Zybooks) == 0 {
		return nil, fmt.Errorf("chapter list is empty, API might have rejected the request")
	}

	return zybookResp.Zybooks[0].Chapters, nil
}

func (s *ZySession) GetSectionContent(zyBookCode string, chapterNum, sectionNum int) ([]ContentResource, error) {
	safeComponent := url.PathEscape(zyBookCode)
	urlStr := "https://zyserver.zybooks.com/v1/zybook/" + safeComponent + "/chapter/" + strconv.Itoa(chapterNum) + "/section/" + strconv.Itoa(sectionNum)

	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)

	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	var zybookSectionContent ZybookSectionContent
	if err := json.NewDecoder(resp.Body).Decode(&zybookSectionContent); err != nil {
		return nil, fmt.Errorf("failed to decode section content: %w", err)
	}

	if !zybookSectionContent.Success {
		return nil, fmt.Errorf("failed to get section content: server returned success=false")
	}

	return zybookSectionContent.Section.ContentResources, nil
}

func (s *ZySession) GetBuildKey() (string, error) {
	resp, err := s.Client.Get("https://learn.zybooks.com/signin")
	if err != nil {
		return "", fmt.Errorf("failed to get homepage: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	re := regexp.MustCompile(`name="zybooks-web/config/environment"\s+content="([^"]+)"`)
	match := re.FindStringSubmatch(string(respBody))
	if len(match) < 2 {
		return "", fmt.Errorf("failed to match regex for buildkey")
	}

	unescapedURL, err := url.PathUnescape(match[1])
	if err != nil {
		return "", fmt.Errorf("failed to unescape buildkey string: %w", err)
	}

	var configData struct {
		App struct {
			BuildKey string `json:"BUILDKEY"`
		} `json:"APP"`
	}

	if err = json.Unmarshal([]byte(unescapedURL), &configData); err != nil {
		return "", fmt.Errorf("failed to decode config json: %w", err)
	}

	return configData.App.BuildKey, nil
}

func (s *ZySession) GenTimestamp() string {
	now := time.Now().UTC()

	s.mu.Lock()
	spoof := time.Duration(s.TimeSpoofed) * time.Second
	s.mu.Unlock()

	random := time.Duration(rand.Intn(1000)) * time.Millisecond
	finalTime := now.Add(spoof + random)

	return finalTime.Format("2006-01-02T15:04:05.000Z")
}

func GenChksum(actID, ts, auth, part, buildKey string) string {
	h := md5.New()
	io.WriteString(h, "content_resource/")
	io.WriteString(h, actID)
	io.WriteString(h, "/activity")
	io.WriteString(h, ts)
	io.WriteString(h, auth)
	io.WriteString(h, actID)
	io.WriteString(h, part)
	io.WriteString(h, "true")
	io.WriteString(h, buildKey)
	return hex.EncodeToString(h.Sum(nil))
}

func (s *ZySession) SpendTime(secID, actID, part int, code string) (bool, error) {
	t := rand.Intn(60) + 1

	s.mu.Lock()
	s.TimeSpoofed += t
	s.mu.Unlock()

	// Strict JSON structure enforcing purely integer types for IDs
	payload := struct {
		TimeSpentRecords []struct {
			CanonicalSectionID int    `json:"canonical_section_id"`
			ContentResourceID  int    `json:"content_resource_id"`
			Part               int    `json:"part"`
			TimeSpent          int    `json:"time_spent"`
			Timestamp          string `json:"timestamp"`
		} `json:"time_spent_records"`
		AuthToken string `json:"auth_token"`
	}{
		TimeSpentRecords: []struct {
			CanonicalSectionID int    `json:"canonical_section_id"`
			ContentResourceID  int    `json:"content_resource_id"`
			Part               int    `json:"part"`
			TimeSpent          int    `json:"time_spent"`
			Timestamp          string `json:"timestamp"`
		}{
			{
				CanonicalSectionID: secID,
				ContentResourceID:  actID,
				Part:               part,
				TimeSpent:          t,
				Timestamp:          s.GenTimestamp(),
			},
		},
		AuthToken: s.Token,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("failed to marshal JSON payload: %w", err)
	}

	safeCode := url.PathEscape(code)
	urlStr := "https://zyserver2.zybooks.com/v1/zybook/" + safeCode + "/time_spent"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", urlStr, bytes.NewBuffer(jsonData))
	if err != nil {
		return false, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Token)

	resp, err := s.Client.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("failed to decode response: %w", err)
	}

	return result.Success, nil
}

func (s *ZySession) SolvePart(actID, secID, part int, code, buildKey string) (bool, error) {
	// Temporarily convert to strings solely for URL construction and Checksum generation
	actIDStr := strconv.Itoa(actID)
	partStr := strconv.Itoa(part)

	urlStr := "https://zyserver.zybooks.com/v1/content_resource/" + actIDStr + "/activity"

	// 1. Spoof time spent
	if _, err := s.SpendTime(secID, actID, part, code); err != nil {
		return false, fmt.Errorf("failed to spoof time spent: %w", err)
	}

	// 2. Generate timestamp and checksum
	ts := s.GenTimestamp()
	chksm := GenChksum(actIDStr, ts, s.Token, partStr, buildKey)

	// 3. Construct payload using strictly required types
	payload := struct {
		Part       int    `json:"part"`
		Complete   bool   `json:"complete"`
		Metadata   string `json:"metadata"`
		ZybookCode string `json:"zybook_code"`
		AuthToken  string `json:"auth_token"`
		Timestamp  string `json:"timestamp"`
		CS         string `json:"__cs__"`
	}{
		Part:       part,
		Complete:   true,
		Metadata:   "{}",
		ZybookCode: code,
		AuthToken:  s.Token,
		Timestamp:  ts,
		CS:         chksm,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("failed to marshal JSON payload: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", urlStr, bytes.NewBuffer(jsonData))
	if err != nil {
		return false, fmt.Errorf("failed to create request: %w", err)
	}

	// Mimic standard browser headers
	req.Header.Set("Host", "zyserver.zybooks.com")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://learn.zybooks.com")
	req.Header.Set("Referer", "https://learn.zybooks.com/")
	req.Header.Set("Authorization", "Bearer "+s.Token)

	resp, err := s.Client.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("failed to parse result: %w", err)
	}

	if !result.Success {
		return false, fmt.Errorf("rejected by server (success: false)")
	}

	return result.Success, nil
}

func (s *ZySession) SolveSection(chapter Chapter, section Section, code, buildKey string) {
	secName := strconv.Itoa(chapter.Number) + "." + strconv.Itoa(section.Number)
	fmt.Printf("\n▶ Starting to solve section %s ...\n", secName)

	secID := section.CanonicalSectionID

	// Attempt standard fetch, fallback to canonical section number if failed
	problems, err := s.GetSectionContent(code, chapter.Number, section.Number)
	if err != nil {
		fmt.Printf("  [Warning] Failed to fetch content for %s natively, trying canonical fallback...\n", secName)

		problems, err = s.GetSectionContent(code, chapter.Number, section.CanonicalSectionNumber)
		if err != nil {
			fmt.Printf("  [Error] Completely failed to fetch problems for %s: %v\n", secName, err)
			return
		}
	}

	for pIdx, problem := range problems {
		pNum := pIdx + 1
		actID := problem.ID
		parts := problem.Parts

		if parts > 0 {
			for partIdx := 0; partIdx < parts; partIdx++ {
				success, err := s.SolvePart(actID, secID, partIdx, code, buildKey)
				if success {
					fmt.Printf("  ✔ Solved Problem %d - Part %d\n", pNum, partIdx+1)
				} else {
					fmt.Printf("  ✖ Failed Problem %d - Part %d (Reason: %v)\n", pNum, partIdx+1, err)
				}
				time.Sleep(time.Millisecond * 500) // Crucial to prevent rate limiting
			}
		} else {
			// If no sub-parts exist, treat as part 0
			success, err := s.SolvePart(actID, secID, 0, code, buildKey)
			if success {
				fmt.Printf("  ✔ Solved Problem %d\n", pNum)
			} else {
				fmt.Printf("  ✖ Failed Problem %d (Reason: %v)\n", pNum, err)
			}
			time.Sleep(time.Millisecond * 500) // Crucial to prevent rate limiting
		}
	}
	fmt.Printf("✅ Section %s completed\n", secName)
}

// ---------------------------------------------------------
// Application Entry Point
// ---------------------------------------------------------

func main() {
	// Note: Ensure you define YourAccount credentials accurately before running.
	// For example:

	session := ZySession{
		Client: &http.Client{},
	}

	// 1. Authentication
	fmt.Println("Attempting to login...")
	if err := session.Login(YourAccount); err != nil {
		fmt.Printf("[-] Login failed: %v\n", err)
		return
	}
	fmt.Printf("[+] Login successful! (UserID: %d)\n", session.UserID)

	// 2. Fetch BuildKey for MD5 signatures
	fmt.Println("Fetching BuildKey...")
	buildKey, err := session.GetBuildKey()
	if err != nil {
		fmt.Printf("[-] Failed to get build key: %v\n", err)
		return
	}
	fmt.Printf("[+] BuildKey retrieved: %s\n", buildKey)

	// 3. Fetch Enrolled Books
	books, err := session.GetBooks()
	if err != nil {
		fmt.Printf("[-] Failed to get books: %v\n", err)
		return
	}
	fmt.Printf("[+] Found %d active books.\n", len(books))

	// 4. Initialize Concurrency Limiters
	maxGoroutines := 7
	sem := make(chan struct{}, maxGoroutines) // Semaphore to limit concurrent network requests
	var wg sync.WaitGroup

	// 5. Execution Loop
	for _, book := range books {
		fmt.Printf("\n========================================\n")
		fmt.Printf("Processing Book: %s\n", book.ZyBookCode)
		fmt.Printf("========================================\n")

		chapters, err := session.GetSections(book.ZyBookCode)
		if err != nil {
			fmt.Printf("[-] Failed to get chapters for %s: %v\n", book.ZyBookCode, err)
			continue
		}

		for _, chapter := range chapters {
			for _, section := range chapter.Sections {
				wg.Add(1)

				// Launch concurrent solver routines
				go func(c Chapter, sec Section, bookCode string) {
					defer wg.Done()

					// Random initial backoff to distribute API hits
					time.Sleep(time.Duration(rand.Intn(2000)) * time.Millisecond)

					sem <- struct{}{}        // Acquire token from semaphore
					defer func() { <-sem }() // Release token back to semaphore

					session.SolveSection(c, sec, bookCode, buildKey)
				}(chapter, section, book.ZyBookCode)
			}
		}
	}

	// Wait for all spawned goroutines to complete
	wg.Wait()
	fmt.Println("\n🎉 All tasks completed!")
}
