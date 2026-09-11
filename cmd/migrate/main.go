package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"
	"weagent/backend/database"
)

func main() {
	down := flag.Bool("allow-destructive-down", false, "Allow Down only on a disposable database fixture")
	flag.Parse()
	action := "up"
	if flag.NArg() > 0 {
		action = flag.Arg(0)
	}
	if action != "up" && action != "status" && action != "down" {
		fmt.Fprintln(os.Stderr, "usage: migrate [-allow-destructive-down] up|status|down")
		os.Exit(2)
	}
	if action == "down" && !*down {
		fmt.Fprintln(os.Stderr, "Down deletes business data; explicit -allow-destructive-down required")
		os.Exit(2)
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL required")
		os.Exit(2)
	}
	p, db, e := database.Provider(url)
	if e != nil {
		fmt.Fprintln(os.Stderr, "migration initialization failed")
		os.Exit(1)
	}
	defer db.Close()
	ctx, c := context.WithTimeout(context.Background(), time.Minute)
	defer c()
	switch action {
	case "up":
		_, e = p.Up(ctx)
	case "down":
		_, e = p.Down(ctx)
	case "status":
		var version int64
		version, e = p.GetDBVersion(ctx)
		if e == nil {
			fmt.Println("database schema version:", version)
		}
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, "migration failed; inspect database/fixture configuration")
		os.Exit(1)
	}
	fmt.Println("migration", action, "completed")
}
