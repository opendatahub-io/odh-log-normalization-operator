package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/opendatahub-io/log-normalizer-operator/internal/normalizer"
	"github.com/opendatahub-io/log-normalizer-operator/internal/normalizer/multiline"
)

func main() {
	cfg := configFromEnv()
	resource := resourceFromEnv()

	emitter := normalizer.NewEmitter(os.Stdout)

	var otlpEmitter *normalizer.OTLPEmitter
	if endpoint := os.Getenv("LOG_NORMALIZER_OTLP_ENDPOINT"); endpoint != "" {
		ctx := context.Background()
		var err error
		otlpEmitter, err = normalizer.NewOTLPEmitter(ctx, endpoint, resource)
		if err != nil {
			fmt.Fprintf(os.Stderr, "OTLP emitter init failed (continuing with stdout only): %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "OTLP export enabled: %s\n", endpoint)
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := otlpEmitter.Shutdown(ctx); err != nil {
					fmt.Fprintf(os.Stderr, "OTLP shutdown error: %v\n", err)
				}
			}()
		}
	}

	drain := normalizer.NewDrainMiner(normalizer.DrainConfig{
		SimilarityThreshold: cfg.drainSimilarity,
		MaxClusters:         cfg.drainMaxClusters,
	})

	var consolidators []multiline.Consolidator
	for _, c := range cfg.consolidators {
		switch c {
		case "go":
			consolidators = append(consolidators, &multiline.GoConsolidator{})
		case "java":
			consolidators = append(consolidators, &multiline.JavaConsolidator{})
		case "python":
			consolidators = append(consolidators, &multiline.PythonConsolidator{})
		}
	}

	emit := func(rec normalizer.LogRecord) {
		normalizer.PromoteTraceFields(&rec)
		rec.Resource = resource
		if err := emitter.Emit(rec); err != nil {
			fmt.Fprintf(os.Stderr, "emit error: %v\n", err)
		}
		if otlpEmitter != nil {
			otlpEmitter.Emit(rec)
		}
	}

	flush := func(lines []string) {
		combined := strings.Join(lines, "\n")
		rec := processLine(combined, drain)
		emit(rec)
	}

	var reassembler *multiline.Reassembler
	if cfg.multilineEnabled {
		reassembler = multiline.NewReassembler(
			cfg.multilineMaxLines,
			time.Duration(cfg.multilineFlushTimeoutMs)*time.Millisecond,
			flush,
			consolidators...,
		)
		defer reassembler.Close()
	}

	reader, err := normalizer.OpenInput(os.Getenv("LOG_NORMALIZER_PIPE_PATH"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	if closer, ok := reader.(interface{ Close() error }); ok && reader != os.Stdin {
		defer func() { _ = closer.Close() }()
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		if normalizer.IsOTLPFeedback(line) {
			continue
		}

		if reassembler != nil {
			reassembler.Add(line)
		} else {
			rec := processLine(line, drain)
			emit(rec)
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "scanner error: %v\n", err)
		os.Exit(1)
	}
}

func processLine(line string, drain *normalizer.DrainMiner) normalizer.LogRecord {
	format := normalizer.DetectFormat(line)

	switch format {
	case normalizer.FormatJSON:
		rec, err := normalizer.ParseJSON(line)
		if err != nil {
			return normalizer.LogRecord{
				Timestamp:    time.Now().UTC().Format(time.RFC3339Nano),
				Level:        "info",
				Message:      line,
				SourceFormat: string(normalizer.FormatPlain),
			}
		}
		rec.SourceFormat = string(normalizer.FormatJSON)
		if rec.Timestamp == "" {
			rec.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
		}
		if cluster := drain.Process(line); cluster != nil {
			rec.TemplateID = cluster.ID
		}
		return rec

	case normalizer.FormatKlog:
		rec := normalizer.ParseKlog(line)
		rec.SourceFormat = string(normalizer.FormatKlog)
		if rec.Timestamp == "" {
			rec.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
		}
		if cluster := drain.Process(line); cluster != nil {
			rec.TemplateID = cluster.ID
		}
		return rec

	case normalizer.FormatLogfmt:
		rec := normalizer.ParseLogfmt(line)
		rec.SourceFormat = string(normalizer.FormatLogfmt)
		if rec.Timestamp == "" {
			rec.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
		}
		if cluster := drain.Process(line); cluster != nil {
			rec.TemplateID = cluster.ID
		}
		return rec

	case normalizer.FormatControllerRuntime:
		rec := normalizer.ParseControllerRuntime(line)
		if rec.Timestamp == "" {
			rec.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
		}
		if cluster := drain.Process(line); cluster != nil {
			rec.TemplateID = cluster.ID
		}
		return rec

	default:
		cluster := drain.Process(line)
		rec := normalizer.LogRecord{
			Timestamp:    time.Now().UTC().Format(time.RFC3339Nano),
			Level:        inferLevel(line),
			Message:      line,
			SourceFormat: string(normalizer.FormatPlain),
		}
		if cluster != nil {
			rec.TemplateID = cluster.ID
		}
		return rec
	}
}

func inferLevel(line string) string {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "error") || strings.Contains(lower, "fatal") || strings.Contains(lower, "panic"):
		return "error"
	case strings.Contains(lower, "warn"):
		return "warning"
	case strings.Contains(lower, "debug") || strings.Contains(lower, "trace"):
		return "debug"
	default:
		return "info"
	}
}

type config struct {
	outputFormat           string
	drainSimilarity        float64
	drainMaxClusters       int
	multilineEnabled       bool
	multilineMaxLines      int
	multilineFlushTimeoutMs int
	consolidators          []string
}

func configFromEnv() config {
	cfg := config{
		outputFormat:           envOrDefault("LOG_NORMALIZER_OUTPUT_FORMAT", "json"),
		drainSimilarity:        envFloatOrDefault("LOG_NORMALIZER_DRAIN_SIMILARITY", 0.4),
		drainMaxClusters:       envIntOrDefault("LOG_NORMALIZER_DRAIN_MAX_CLUSTERS", 1000),
		multilineEnabled:       envBoolOrDefault("LOG_NORMALIZER_MULTILINE_ENABLED", true),
		multilineMaxLines:      envIntOrDefault("LOG_NORMALIZER_MULTILINE_MAX_LINES", 100),
		multilineFlushTimeoutMs: envIntOrDefault("LOG_NORMALIZER_MULTILINE_FLUSH_TIMEOUT_MS", 2000),
	}

	if v := os.Getenv("LOG_NORMALIZER_CONSOLIDATORS"); v != "" {
		cfg.consolidators = strings.Split(v, ",")
	} else {
		cfg.consolidators = []string{"go"}
	}

	return cfg
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envFloatOrDefault(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envIntOrDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func resourceFromEnv() map[string]any {
	res := make(map[string]any)
	mapping := map[string]string{
		"LOG_NORMALIZER_POD_NAME":  "k8s.pod.name",
		"LOG_NORMALIZER_NAMESPACE": "k8s.namespace.name",
		"LOG_NORMALIZER_NODE_NAME": "k8s.node.name",
		"LOG_NORMALIZER_POD_UID":   "k8s.pod.uid",
	}
	for envKey, attrKey := range mapping {
		if v := os.Getenv(envKey); v != "" {
			res[attrKey] = v
		}
	}
	if len(res) == 0 {
		return nil
	}
	return res
}


func envBoolOrDefault(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
