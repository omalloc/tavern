// timagecheck explicitly checks a deployed HTTP worker; it is never part of the
// default unit test path and does not enable traffic or alter cache state.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/omalloc/tavern/transform"
	"gopkg.in/yaml.v3"
)

func main() {
	configPath := flag.String("config", "", "worker contract YAML file")
	timeout := flag.Duration("timeout", 2*time.Minute, "total matrix check timeout")
	flag.Parse()
	if err := check(*configPath, *timeout, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func check(path string, timeout time.Duration, output io.Writer) error {
	if path == "" || timeout <= 0 {
		return errors.New("positive -timeout and -config are required")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	if stat.Size() > 1<<20 {
		return errors.New("worker contract configuration exceeds 1 MiB")
	}
	decoder := yaml.NewDecoder(io.LimitReader(file, (1<<20)+1))
	decoder.KnownFields(true)
	var config transform.HTTPWorkerConfig
	if err = decoder.Decode(&config); err != nil {
		return err
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return errors.New("worker contract requires exactly one YAML document")
	}
	worker, err := transform.NewHTTPWorker(config, nil)
	if err != nil {
		return err
	}
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signals, timeout)
	defer cancel()
	report, err := worker.Verify(ctx)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
