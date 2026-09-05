package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"text/tabwriter"
	"time"

	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

func main() {
	command := "up"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		log.Fatal("MYSQL_DSN is required")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatalf("open mysql: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch command {
	case "up":
		if err := store.Migrate(ctx, db); err != nil {
			log.Fatalf("migrate mysql: %v", err)
		}
		fmt.Println("mysql migrations are up to date")
	case "status":
		statuses, err := store.MigrationStatuses(ctx, db)
		if err != nil {
			log.Fatalf("get mysql migration status: %v", err)
		}
		writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "VERSION\tNAME\tSTATUS\tAPPLIED_AT")
		for _, status := range statuses {
			state := "pending"
			appliedAt := "-"
			if status.Applied {
				state = "applied"
				if status.AppliedAt.Valid {
					appliedAt = status.AppliedAt.Time.Format(time.RFC3339)
				}
			}
			fmt.Fprintf(writer, "%d\t%s\t%s\t%s\n", status.Version, status.Name, state, appliedAt)
		}
		if err := writer.Flush(); err != nil {
			log.Fatalf("print mysql migration status: %v", err)
		}
	default:
		log.Fatalf("unknown command %q: use up or status", command)
	}
}
