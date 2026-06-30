package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const baseURL = "https://api.assemblyai.com/v2"

// Spinner — живёт всю работу программы, надпись меняется через SetLabel.
type Spinner struct {
	label string
	mu    sync.Mutex
	stop  chan bool
	start time.Time
}

func NewSpinner() *Spinner {
	s := &Spinner{stop: make(chan bool), start: time.Now()}
	go s.run()
	return s
}

func (s *Spinner) SetLabel(label string) {
	s.mu.Lock()
	s.label = label
	s.mu.Unlock()
}

func (s *Spinner) run() {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	i := 0
	for {
		select {
		case <-s.stop:
			fmt.Printf("\r\033[K") // стереть строку
			return
		default:
			s.mu.Lock()
			label := s.label
			s.mu.Unlock()
			elapsed := int(time.Since(s.start).Seconds())
			fmt.Printf("\r%s  %s  (%ds)   ", frames[i%len(frames)], label, elapsed)
			i++
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func (s *Spinner) Stop() {
	s.stop <- true
}

func main() {
	// 1. Check arguments
	if len(os.Args) < 3 {
		fmt.Println("Usage: transcribe <input.mp4> <output.txt>")
		os.Exit(1)
	}
	inputPath := os.Args[1]
	outputPath := os.Args[2]

	apiKey := os.Getenv("ASSEMBLYAI_API_KEY")
	if apiKey == "" {
		fmt.Println("Error: ASSEMBLYAI_API_KEY is not set")
		os.Exit(1)
	}

	client := &http.Client{}

	// Спиннер запускается ОДИН раз и крутится всю работу программы.
	sp := NewSpinner()

	// 2. Upload file -> upload_url
	sp.SetLabel("uploading file")
	uploadURL, err := uploadFile(client, apiKey, inputPath)
	if err != nil {
		sp.Stop()
		fmt.Println("Upload error:", err)
		os.Exit(1)
	}

	// 3. Create transcription job
	sp.SetLabel("creating job")
	id, err := createTranscript(client, apiKey, uploadURL)
	if err != nil {
		sp.Stop()
		fmt.Println("Job creation error:", err)
		os.Exit(1)
	}

	// 4. Poll status
	sp.SetLabel("processing")
	text, err := pollTranscript(client, apiKey, id)
	if err != nil {
		sp.Stop()
		fmt.Println("Transcription error:", err)
		os.Exit(1)
	}

	// Спиннер останавливаем перед финальным выводом.
	sp.Stop()

	// 5. Write to file
	if err := os.WriteFile(outputPath, []byte(text), 0644); err != nil {
		fmt.Println("Write error:", err)
		os.Exit(1)
	}
	fmt.Printf("Done. Transcript saved to: %s\n", outputPath)
}

func uploadFile(client *http.Client, apiKey, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	req, _ := http.NewRequest("POST", baseURL+"/upload", file)
	req.Header.Set("authorization", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		UploadURL string `json:"upload_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.UploadURL, nil
}

func createTranscript(client *http.Client, apiKey, audioURL string) (string, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"audio_url":          audioURL,
		"speech_models":      []string{"universal-3-pro", "universal-2"},
		"language_detection": true,
		"speaker_labels":     true,
	})

	req, _ := http.NewRequest("POST", baseURL+"/transcript", bytes.NewReader(body))
	req.Header.Set("authorization", apiKey)
	req.Header.Set("content-type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.ID, nil
}

func pollTranscript(client *http.Client, apiKey, id string) (string, error) {
	for {
		req, _ := http.NewRequest("GET", baseURL+"/transcript/"+id, nil)
		req.Header.Set("authorization", apiKey)

		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var result struct {
			Status     string `json:"status"`
			Text       string `json:"text"`
			Error      string `json:"error"`
			Utterances []struct {
				Speaker string `json:"speaker"`
				Text    string `json:"text"`
			} `json:"utterances"`
		}
		if err := json.Unmarshal(bodyBytes, &result); err != nil {
			return "", err
		}

		switch result.Status {
		case "completed":
			var sb strings.Builder
			if len(result.Utterances) > 0 {
				for _, u := range result.Utterances {
					sb.WriteString(fmt.Sprintf("Speaker %s: %s\n\n", u.Speaker, u.Text))
				}
			} else {
				sb.WriteString(result.Text)
			}
			return sb.String(), nil
		case "error":
			return "", fmt.Errorf("%s", result.Error)
		default:
			time.Sleep(2 * time.Second)
		}
	}
}