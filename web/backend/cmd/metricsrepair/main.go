// metricsrepair rebuilds retained metric rollups only when explicitly applied.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lib/pq"
	"server-monitor/internal/models"
)

func main() {
	apply := flag.Bool("apply", false, "rebuild retained complete rollup buckets; default only prints the plan")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if !*apply {
		fmt.Println("Dry run: no database connection or writes. Use -apply after a database backup.")
		if err := encoder.Encode(models.PlanMetricRepair(time.Now())); err != nil {
			os.Exit(1)
		}
		return
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(2)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		// Driver parse/connect errors can contain DSN fragments; never echo them.
		fmt.Fprintln(os.Stderr, "cannot initialize database connection")
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	signalCtx, stopSignal := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignal()
	ctx, cancel := context.WithTimeout(signalCtx, models.MetricRepairTimeout)
	defer cancel()
	pingCtx, stop := context.WithTimeout(ctx, 10*time.Second)
	err = db.PingContext(pingCtx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "database connection failed")
		os.Exit(1)
	}
	result, err := models.RepairMetricHistory(ctx, db)
	if outputErr := encoder.Encode(result); outputErr != nil {
		os.Exit(1)
	}
	if err != nil {
		// The model result records progress without exposing connection details.
		var databaseError *pq.Error
		if errors.As(err, &databaseError) {
			fmt.Fprintf(os.Stderr, "database SQLSTATE: %s\n", databaseError.Code)
		}
		fmt.Fprintln(os.Stderr, "repair failed or timed out; completed batches remain safe to retry; weighting cutoff was not advanced")
		os.Exit(1)
	}
}
