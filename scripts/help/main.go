package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func main() {
	f, err := os.Open("Makefile")
	if err != nil {
		fmt.Printf("Error opening Makefile: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	fmt.Println("\nUsage: make [target]")
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "## ") {
			parts := strings.SplitN(line[3:], ": ", 2)
			if len(parts) == 2 {
				fmt.Printf("  \033[36m%-24s\033[0m %s\n", parts[0], parts[1])
			}
		}
	}
	fmt.Println("")
}
