// check-govulncheck compares symbol-reachable govulncheck JSON findings with a reviewed baseline.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type event struct {
	Finding *struct {
		OSV   string `json:"osv"`
		Trace []struct {
			Function string `json:"function"`
		} `json:"trace"`
	} `json:"finding"`
}

func loadAllowlist(path string) (map[string]struct{}, error) {
	// #nosec G304 -- this local review tool intentionally reads the reviewer-supplied baseline path.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	allowed := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if line != "" {
			allowed[line] = struct{}{}
		}
	}
	return allowed, scanner.Err()
}

func symbolFindings(reader io.Reader) (map[string]struct{}, error) {
	found := make(map[string]struct{})
	decoder := json.NewDecoder(reader)
	for {
		var item event
		if err := decoder.Decode(&item); err == io.EOF {
			return found, nil
		} else if err != nil {
			return nil, fmt.Errorf("decode govulncheck JSON: %w", err)
		}
		if item.Finding == nil || item.Finding.OSV == "" {
			continue
		}
		for _, frame := range item.Finding.Trace {
			if frame.Function != "" {
				found[item.Finding.OSV] = struct{}{}
				break
			}
		}
	}
}

func difference(left, right map[string]struct{}) []string {
	var result []string
	for id := range left {
		if _, ok := right[id]; !ok {
			result = append(result, id)
		}
	}
	sort.Strings(result)
	return result
}

func main() {
	allowPath := flag.String("allow", "security/govulncheck-allowlist.txt", "reviewed symbol-finding baseline")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: check-govulncheck [-allow file] report.json")
		os.Exit(2)
	}
	allowed, err := loadAllowlist(*allowPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load allowlist: %v\n", err)
		os.Exit(1)
	}
	report, err := os.Open(flag.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "open report: %v\n", err)
		os.Exit(1)
	}
	defer report.Close()
	found, err := symbolFindings(report)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	unexpected := difference(found, allowed)
	missing := difference(allowed, found)
	if len(unexpected) > 0 || len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "govulncheck symbol baseline changed; unexpected=%v missing=%v\n", unexpected, missing)
		os.Exit(1)
	}
	fmt.Printf("govulncheck symbol baseline matched %d reviewed finding(s)\n", len(found))
}
