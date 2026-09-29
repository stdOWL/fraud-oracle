// Command ofac downloads the OFAC SDN list and writes the Ethereum addresses it contains
// as the sanctions fixture consumed by the API. Run it to refresh testdata/sanctions.json.
//
//	go run ./cmd/ofac -out testdata/sanctions.json
//
// Source: https://www.treasury.gov/ofac/downloads/sdn.xml (~30 MB). Entries are tagged
// "Digital Currency Address - ETH" in <idType>. The list is static at commit time on purpose:
// scoring must be deterministic across CRE nodes, so the service never fetches at runtime.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const sdnURL = "https://www.treasury.gov/ofac/downloads/sdn.xml"

var ethID = regexp.MustCompile(`Digital Currency Address - ETH</idType>\s*<idNumber>(0x[0-9a-fA-F]{40})`)

type fixture struct {
	Source    string   `json:"source"`
	FetchedAt string   `json:"fetchedAt"`
	Count     int      `json:"count"`
	Addresses []string `json:"addresses"`
}

func main() {
	out := flag.String("out", "testdata/sanctions.json", "output path")
	flag.Parse()

	resp, err := http.Get(sdnURL)
	if err != nil {
		fail(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fail(fmt.Errorf("GET %s: %s", sdnURL, resp.Status))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fail(err)
	}

	seen := map[string]bool{}
	for _, m := range ethID.FindAllSubmatch(body, -1) {
		seen[strings.ToLower(string(m[1]))] = true
	}
	addrs := make([]string, 0, len(seen))
	for a := range seen {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	if len(addrs) == 0 {
		fail(fmt.Errorf("no ETH addresses found; SDN XML layout may have changed"))
	}

	f := fixture{Source: sdnURL, FetchedAt: time.Now().UTC().Format(time.RFC3339), Count: len(addrs), Addresses: addrs}
	buf, _ := json.MarshalIndent(f, "", "  ")
	if err := os.WriteFile(*out, append(buf, '\n'), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("wrote %d ETH addresses to %s\n", len(addrs), *out)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ofac:", err)
	os.Exit(1)
}
