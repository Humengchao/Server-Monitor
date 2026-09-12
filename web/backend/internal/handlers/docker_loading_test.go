package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDockerInventoryDoesNotRequestSizeOrStats(testContext *testing.T) {
	calls := []string{}
	containers, err := readDockerContainers(context.Background(), false, func(_ context.Context, arguments string, _ int) (string, error) {
		calls = append(calls, arguments)
		return `{"id":"abc123","name":"fast","state":"running","size":"0B"}`, nil
	})
	if err != nil || len(calls) != 1 || strings.Contains(calls[0], "--size") || strings.Contains(calls[0], "stats ") {
		testContext.Fatal(calls, err)
	}
	if len(containers) != 1 || containers[0].Name != "fast" || containers[0].StatsAvailable || containers[0].DiskAvailable {
		testContext.Fatal(containers)
	}
}
func TestDockerStatisticsFallsBackWhenSizeUnsupported(testContext *testing.T) {
	calls := []string{}
	containers, err := readDockerContainers(context.Background(), true, func(_ context.Context, arguments string, _ int) (string, error) {
		calls = append(calls, arguments)
		if strings.Contains(arguments, "--size") {
			return "", errors.New("unsupported flag")
		}
		if strings.HasPrefix(arguments, "stats ") {
			return `{"ID":"abc123","CPUPerc":"12.0%","MemUsage":"1MiB / 4MiB","MemPerc":"25.0%","BlockIO":"1kB / 2kB"}`, nil
		}
		return `{"id":"abc123","name":"fast","state":"running"}`, nil
	})
	if err != nil || len(calls) != 3 || len(containers) != 1 {
		testContext.Fatal(calls, err, containers)
	}
	if !containers[0].StatsAvailable || containers[0].CPUPercent != 12 || containers[0].DiskAvailable {
		testContext.Fatal(containers)
	}
}
func TestDockerStatisticsFailureIsNotReportedAsZero(testContext *testing.T) {
	_, err := readDockerContainers(context.Background(), true, func(_ context.Context, arguments string, _ int) (string, error) {
		if strings.HasPrefix(arguments, "stats ") {
			return "", errors.New("daemon unavailable")
		}
		return `{"id":"abc123","state":"running"}`, nil
	})
	if err == nil {
		testContext.Fatal("statistics failure hidden")
	}
}
