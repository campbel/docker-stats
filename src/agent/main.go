package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/robfig/cron"
	"github.com/sirupsen/logrus"
)

var (
	dockerClient  *client.Client
	statsInterval = os.Getenv("stats_interval")
	logFormat     = os.Getenv("log_format")
	logLevel      = os.Getenv("log_level")
)

func init() {

	if logFormat == "" {
		logFormat = "json"
	}

	if logLevel == "" {
		logLevel = "info"
	}

	if statsInterval == "" {
		statsInterval = "@every 1m"
	}

	switch logFormat {
	case "text":
		logrus.SetFormatter(&logrus.TextFormatter{})
	case "json":
		logrus.SetFormatter(&logrus.JSONFormatter{})
	}

	switch logLevel {
	case "debug":
		logrus.SetLevel(logrus.DebugLevel)
	case "info":
		logrus.SetLevel(logrus.InfoLevel)
	}
}

func main() {

	logrus.WithFields(logrus.Fields{
		"environmnent": map[string]interface{}{
			"log_format":     logFormat,
			"log_level":      logLevel,
			"stats_interval": statsInterval,
		},
	}).Info("starting up...")

	var err error
	dockerClient, err = client.NewEnvClient()
	if err != nil {
		logrus.Error(err.Error())
		return
	}

	stats()
	c := cron.New()
	c.AddFunc(statsInterval, stats)
	c.Start()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, err := dockerClient.Ping(context.Background())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		fmt.Fprint(w, "OK")
	})

	server := &http.Server{
		Addr:    ":80",
		Handler: mux,
	}

	// Start the server and handle errors. ErrServerClosed will ocurr when we call shutdown above.
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logrus.WithFields(logrus.Fields{"error": err}).Error("shutting down")
	} else {
		logrus.Info("shutting down")
	}
}

// Collect stats from Docker API and log it. This is used to create das
func stats() {
	containers, err := dockerClient.ContainerList(context.Background(), types.ContainerListOptions{})
	if err != nil {
		logrus.WithFields(logrus.Fields{"error": err}).Error("error getting container list")
	}

	for _, container := range containers {
		go func(container types.Container) {
			stats, err := dockerClient.ContainerStats(context.Background(), container.ID, false)
			if err != nil {
				logrus.WithFields(logrus.Fields{"error": err}).Error("error getting container stats")
				return
			}
			defer stats.Body.Close()

			var info *types.StatsJSON
			if err := json.NewDecoder(stats.Body).Decode(&info); err != nil {
				logrus.WithFields(logrus.Fields{"error": err}).Error("error decoding stats")
				return
			}

			netRead, netWrite := calculateNetwork(info.Networks)

			blkRead, blkWrite := calculateBlockIO(info.BlkioStats)

			logrus.WithFields(logrus.Fields{
				"Names":   container.Names,
				"Image":   container.Image,
				"ImageID": container.ImageID,
				"Labels":  container.Labels,
				"State":   container.State,
				"Status":  container.Status,
				"OS":      stats.OSType,
				"Stats": map[string]interface{}{
					"CPU_PCT":      fmt.Sprintf("%.2f", calculateCPUPercent(info)),
					"MEM_MB":       fmt.Sprintf("%.2f", float64(info.MemoryStats.Usage)/1024/1024),
					"MEM_PCT":      fmt.Sprintf("%.2f", 100.0*float64(info.MemoryStats.Usage)/float64(info.MemoryStats.Limit)),
					"NET_READ_MB":  fmt.Sprintf("%.2f", netRead/1024/1024),
					"NET_WRITE_MB": fmt.Sprintf("%.2f", netWrite/1024/1024),
					"BLK_READ_MB":  fmt.Sprintf("%.2f", blkRead/1024/1024),
					"BLK_WRITE_MB": fmt.Sprintf("%.2f", blkWrite/1024/1024),
					"PIDS":         info.PidsStats.Current,
				},
			}).Info("stats")
		}(container)
	}
}

func calculateCPUPercent(stats *types.StatsJSON) float64 {
	var (
		cpuPercent = 0.0
		// calculate the change for the cpu usage of the container in between readings
		cpuDelta = float64(stats.CPUStats.CPUUsage.TotalUsage) - float64(stats.PreCPUStats.CPUUsage.TotalUsage)
		// calculate the change for the entire system between readings
		systemDelta = float64(stats.CPUStats.SystemUsage) - float64(stats.PreCPUStats.SystemUsage)
		onlineCPUs  = float64(stats.CPUStats.OnlineCPUs)
	)

	if onlineCPUs == 0.0 {
		onlineCPUs = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if systemDelta > 0.0 && cpuDelta > 0.0 {
		cpuPercent = (cpuDelta / systemDelta) * onlineCPUs * 100.0
	}
	return cpuPercent
}

func calculateBlockIO(blkio types.BlkioStats) (blkRead float64, blkWrite float64) {
	for _, bioEntry := range blkio.IoServiceBytesRecursive {
		switch strings.ToLower(bioEntry.Op) {
		case "read":
			blkRead += float64(bioEntry.Value)
		case "write":
			blkWrite += float64(bioEntry.Value)
		}
	}
	return
}

func calculateNetwork(network map[string]types.NetworkStats) (netRead float64, netWrite float64) {
	for _, v := range network {
		netRead += float64(v.RxBytes)
		netWrite += float64(v.TxBytes)
	}
	return
}
