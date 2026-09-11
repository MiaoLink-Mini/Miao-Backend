package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"weagent/backend/internal/fakenode"
)

func main() {
	url := flag.String("gateway", "http://127.0.0.1:8080", "Gateway URL")
	dir := flag.String("state-dir", ".runtime/fake-node", "Private journal and identity directory")
	name := flag.String("name", "喵连 Fake Node", "Device display name")
	flag.Parse()
	ctx, c := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer c()
	err := fakenode.Run(ctx, fakenode.Config{URL: *url, Dir: *dir, Name: *name, Output: os.Stdout})
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "Fake Node stopped:", err)
		os.Exit(1)
	}
}
