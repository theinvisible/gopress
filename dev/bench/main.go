// Command bench is a minimal HTTP load generator for local measurements:
//
//	go run ./dev/bench -c 64 -d 10s http://127.0.0.1:8080/
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	conc := flag.Int("c", 64, "concurrent connections")
	dur := flag.Duration("d", 10*time.Second, "duration")
	gzip := flag.Bool("gzip", true, "send Accept-Encoding: br, gzip")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: bench [-c N] [-d dur] URL")
		os.Exit(2)
	}
	target := flag.Arg(0)
	tr := &http.Transport{MaxIdleConnsPerHost: *conc, DisableCompression: true}
	client := &http.Client{Transport: tr}

	var count, errors atomic.Int64
	lat := make([][]time.Duration, *conc)
	deadline := time.Now().Add(*dur)
	var wg sync.WaitGroup
	for i := range *conc {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				req, _ := http.NewRequest(http.MethodGet, target, nil)
				if *gzip {
					req.Header.Set("Accept-Encoding", "br, gzip")
				}
				start := time.Now()
				resp, err := client.Do(req)
				if err != nil {
					errors.Add(1)
					continue
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 {
					errors.Add(1)
				}
				lat[i] = append(lat[i], time.Since(start))
				count.Add(1)
			}
		}()
	}
	wg.Wait()
	var all []time.Duration
	for _, l := range lat {
		all = append(all, l...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	pct := func(p float64) time.Duration {
		if len(all) == 0 {
			return 0
		}
		return all[int(float64(len(all)-1)*p)]
	}
	fmt.Printf("%s: %d requests in %s, %.0f req/s, errors %d, p50 %s, p90 %s, p99 %s\n",
		target, count.Load(), *dur, float64(count.Load())/dur.Seconds(), errors.Load(), pct(0.5), pct(0.9), pct(0.99))
}
