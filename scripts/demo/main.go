package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	baseURL     = "http://localhost:8080"
	numParallel = 14
)

type submitResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type taskDetail struct {
	Type     string `json:"type"`
	Status   string `json:"status"`
	Position int    `json:"position"`
}

type workflowDetail struct {
	ID     string       `json:"id"`
	Status string       `json:"status"`
	Tasks  []taskDetail `json:"tasks"`
}

func main() {
	fmt.Print("\033[H\033[2J")
	fmt.Println("⚡================================================================⚡")
	fmt.Println("🚀             WORKFLOW ENGINE - HIGH CONCURRENCY DEMO             🚀")
	fmt.Println("⚡================================================================⚡")

	resp, err := http.Get(baseURL + "/workflows")
	if err != nil {
		fmt.Printf("\n❌ Error: Cannot connect to the workflow engine at %s\n", baseURL)
		fmt.Println("👉 Please start the engine first in another terminal (run 'make run' or 'make docker-up').")
		os.Exit(1)
	}
	resp.Body.Close()

	fmt.Println("\n✅ Connected to engine successfully.")
	fmt.Println("----------------------------------------------------------------")
	time.Sleep(1 * time.Second)

	runConcurrencyBurst()

	fmt.Println("\n\n🎉 Demo finished successfully! Bounded concurrency proved in real-time.")
}

func runConcurrencyBurst() {
	fmt.Printf("🔥 Submitting %d workflows simultaneously to the worker pool...\n", numParallel)
	fmt.Println("💡 Watch closely: exactly 4 should run in parallel, proving the worker limit!")
	fmt.Println("----------------------------------------------------------------")
	time.Sleep(1500 * time.Millisecond)

	payload := map[string]any{
		"input": map[string]any{
			"a":  100,
			"b":  2,
			"op": "divide",
		},
		"tasks": []map[string]any{
			{"type": "Calculate", "config": map[string]any{"a": "$.input.a", "b": "$.input.b", "op": "$.input.op"}},
			{"type": "Calculate", "config": map[string]any{"a": "$.steps.0", "b": 2, "op": "divide"}},
			{"type": "Calculate", "config": map[string]any{"a": "$.steps.1", "b": 5, "op": "multiply"}},
			{"type": "Print", "config": map[string]any{"template": "Final pipeline output: {{ $.steps.2 }}"}},
		},
	}

	ids := make([]string, numParallel)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i := 0; i < numParallel; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := submitWorkflow(payload)
			mu.Lock()
			ids[idx] = id
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	fmt.Printf("\n📥 All %d workflows queued! Starting real-time visualization dashboard...\n\n", numParallel)
	time.Sleep(1 * time.Second)

	firstDraw := true
	for {
		statuses := make([]workflowDetail, numParallel)
		var pollWg sync.WaitGroup
		for i, id := range ids {
			pollWg.Add(1)
			go func(idx int, wfID string) {
				defer pollWg.Done()
				statuses[idx] = fetchWorkflow(wfID)
			}(i, id)
		}
		pollWg.Wait()

		// Cofa kursor w pionie i wymusza powrót na początek linii (\r)
		if !firstDraw {
			fmt.Printf("\r\033[%dA", numParallel)
		}
		firstDraw = false

		activeWorkers := 0
		allDone := true

		for i, w := range statuses {
			if w.Status == "Running" {
				activeWorkers++
			}
			if w.Status != "Completed" && w.Status != "Failed" {
				allDone = false
			}

			progress := getProgressBar(w)
			fmt.Printf("\r\033[2K📊 \033[1mWorkflow %02d\033[0m [%s] -> %-18s %s\n",
				i+1,
				w.ID[:8],
				formatStatus(w.Status),
				progress,
			)
		}

		fmt.Printf("\r\033[2K\n⚙️  Active Worker Pool Load: [\033[32m%s\033[0m] (%d/4 active workers)",
			strings.Repeat("█", activeWorkers)+strings.Repeat("░", 4-activeWorkers),
			activeWorkers,
		)

		// Cofa o 1 linię w górę i resetuje pozycję poziomą na kolumnę 1
		fmt.Print("\033[1A\r")

		if allDone {
			fmt.Printf("\r\033[%dB\n", numParallel+1)
			break
		}

		time.Sleep(150 * time.Millisecond)
	}
}

func submitWorkflow(payload any) string {
	body, _ := json.Marshal(payload)
	resp, err := http.Post(baseURL+"/workflows", "application/json", bytes.NewBuffer(body))
	if err != nil {
		fmt.Printf("❌ Failed to submit workflow: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var r submitResponse
	_ = json.NewDecoder(resp.Body).Decode(&r)
	return r.ID
}

func fetchWorkflow(id string) workflowDetail {
	resp, err := http.Get(fmt.Sprintf("%s/workflows/%s", baseURL, id))
	if err != nil {
		return workflowDetail{ID: id, Status: "Failed"}
	}
	defer resp.Body.Close()

	var w workflowDetail
	_ = json.NewDecoder(resp.Body).Decode(&w)
	return w
}

func getProgressBar(w workflowDetail) string {
	if w.Status == "Completed" {
		return "\033[32m[██████████] 100% Done\033[0m"
	}
	if w.Status == "Failed" {
		return "\033[31m[XXXXXXXXXX] Failed ❌\033[0m"
	}
	if w.Status == "Pending" {
		return "\033[37m[░░░░░░░░░░] Waiting in queue...\033[0m"
	}

	completedTasks := 0
	for _, t := range w.Tasks {
		if t.Status == "Completed" {
			completedTasks++
		}
	}

	percent := (completedTasks * 100) / len(w.Tasks)
	filledBlocks := (completedTasks * 10) / len(w.Tasks)
	emptyBlocks := 10 - filledBlocks

	return fmt.Sprintf("\033[34m[%s%s] %d%% executing task %d/4\033[0m",
		strings.Repeat("█", filledBlocks),
		strings.Repeat(" ", emptyBlocks),
		percent,
		completedTasks+1,
	)
}

func formatStatus(status string) string {
	switch status {
	case "Pending":
		return "\033[33mPending 💤\033[0m"
	case "Running":
		return "\033[34mRunning 🏃\033[0m"
	case "Completed":
		return "\033[32mCompleted\033[0m"
	case "Failed":
		return "\033[31mFailed\033[0m"
	default:
		return status
	}
}
