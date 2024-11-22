package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

type Config struct {
	Host         string
	Port         int
	Username     string
	Password     string
	Timeout      time.Duration
	HeartbeatURL string
}

type VHost struct {
	ClusterState map[string]string `json:"cluster_state"`
	Description  string            `json:"description"`
}

func logWithTimestamp(format string, args ...interface{}) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	fmt.Printf("[%s] %s", timestamp, fmt.Sprintf(format, args...))
}

func sendHeartbeat() error {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}
	req, err := http.NewRequest("GET", os.Getenv("HEARTBEAT_URL"), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	return nil
}

func checkRabbitMQStatus(config Config) error {
	url := fmt.Sprintf("http://%s:%d/api/vhosts", config.Host, config.Port)

	client := &http.Client{
		Timeout: config.Timeout,
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("error creating request: %v", err)
	}

	req.SetBasicAuth(config.Username, config.Password)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error making request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading response body: %v", err)
	}

	var vhosts []VHost
	if err := json.Unmarshal(body, &vhosts); err != nil {
		return fmt.Errorf("error parsing JSON response: %v", err)
	}

	// Check if any vhost exists and has a running state
	for _, vhost := range vhosts {
		for _, state := range vhost.ClusterState {
			if state == "running" {
				return nil
			}
		}
	}

	return fmt.Errorf("no running RabbitMQ nodes found")
}

func monitorRabbitMQ(config Config, stopChan <-chan struct{}) {
	ticker := time.NewTicker(config.Timeout)
	defer ticker.Stop()

	// Function to perform a single check
	performCheck := func() {
		if err := checkRabbitMQStatus(config); err != nil {
			logWithTimestamp("❌ Error: %v\n", err)
		} else {
			logWithTimestamp("✅ RabbitMQ is running\n")
			err := sendHeartbeat()
			if err != nil {
				logWithTimestamp("❌ Error sending heartbeat: %v\n", err)
			} else {
				logWithTimestamp("✅ Heartbeat sent\n")
			}
		}
	}

	// Initial check immediately when starting
	performCheck()

	// Continuous monitoring
	for {
		select {
		case <-ticker.C:
			performCheck()
		case <-stopChan:
			logWithTimestamp("Shutting down monitoring...\n")
			return
		}
	}
}

func main() {
	portInt, err := strconv.Atoi(os.Getenv("RABBITMQ_PORT"))
	if err != nil {
		panic(err)
	}
	timeout, err := time.ParseDuration(os.Getenv("HEARTBEAT_INTERVAL"))
	if err != nil {
		panic(err)
	}

	config := Config{
		Host:         os.Getenv("RABBITMQ_HOST"),
		Port:         portInt,
		Username:     os.Getenv("RABBITMQ_USERNAME"),
		Password:     os.Getenv("RABBITMQ_PASSWORD"),
		Timeout:      timeout,
		HeartbeatURL: os.Getenv("HEARTBEAT_URL"),
	}

	configJson, err := json.MarshalIndent(struct {
		Host          string
		Port          int
		Username      string
		Timeout       time.Duration
		CheckInterval time.Duration
	}{
		Host:     config.Host,
		Port:     config.Port,
		Username: config.Username,
		Timeout:  config.Timeout,
	}, "", "  ")
	if err != nil {
		panic(err)
	}

	fmt.Printf("Config: %s\n", configJson)

	// Setup graceful shutdown
	stopChan := make(chan struct{})
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start monitoring in a goroutine
	go monitorRabbitMQ(config, stopChan)

	// Wait for interrupt signal
	<-sigChan
	logWithTimestamp("Received shutdown signal...\n")

	// Trigger graceful shutdown
	close(stopChan)

	// Give some time for the last check to complete
	time.Sleep(2 * time.Second)
}
