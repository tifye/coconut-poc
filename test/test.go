package main

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/charmbracelet/log"
)

func main() {
	reqs := []string{
		"http://localhost:9997/",
		"http://localhost:9997/fonts/Konigsberg.otf",
		"http://localhost:9997/@vite/client",
		"http://localhost:9997/@fs/D:/Projects/personal-site/node_modules/astro/dist/runtime/client/hmr.js",
		"http://localhost:9997/src/layouts/Layout.astro",
		"http://localhost:9997/src/pages/index.astro",
		"http://localhost:9997/node_modules/@astrojs/tailwind/base.css",
	}

	client := http.DefaultClient
	wg := sync.WaitGroup{}
	wg.Add(len(reqs))
	for _, r := range reqs {
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", r, nil)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
			defer cancel()
			req = req.WithContext(ctx)
			_, err := client.Do(req)
			if err != nil {
				log.Error("failed to get", "req", r, "err", err)
			}
		}()
	}
	wg.Wait()
}
