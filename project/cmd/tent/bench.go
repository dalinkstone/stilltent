package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

// BenchResult holds the results of a benchmark run.
type BenchResult struct {
	Timestamp    string              `json:"timestamp"`
	Platform     string              `json:"platform"`
	Arch         string              `json:"arch"`
	NumCPU       int                 `json:"num_cpu"`
	Benchmarks   map[string]BenchRun `json:"benchmarks"`
	TotalElapsed string              `json:"total_elapsed"`
}

// BenchRun holds a single benchmark measurement.
type BenchRun struct {
	Name     string  `json:"name"`
	Duration string  `json:"duration"`
	DurationMs float64 `json:"duration_ms"`
	Result   string  `json:"result,omitempty"`
	Error    string  `json:"error,omitempty"`
}

func benchCmd() *cobra.Command {
	var (
		outputJSON bool
		outputFile string
		suite      string
	)

	cmd := &cobra.Command{
		Use:   "bench [name]",
		Short: "Benchmark sandbox performance",
		Long: `Run performance benchmarks for tent sandboxes. Measures boot time,
disk I/O throughput, memory operations, and sandbox lifecycle performance.

If a sandbox name is given, measures boot/stop cycle time for that sandbox.
Without a name, runs synthetic benchmarks for the local system.

Suites:
  all       Run all benchmarks (default)
  io        Disk I/O benchmarks only
  memory    Memory throughput benchmarks only
  lifecycle Sandbox lifecycle benchmarks only
  system    System capability benchmarks only

Examples:
  tent bench
  tent bench --suite io
  tent bench --suite memory --json
  tent bench mybox --json --output results.json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			start := time.Now()
			results := &BenchResult{
				Timestamp:  time.Now().UTC().Format(time.RFC3339),
				Platform:   runtime.GOOS,
				Arch:       runtime.GOARCH,
				NumCPU:     runtime.NumCPU(),
				Benchmarks: make(map[string]BenchRun),
			}

			if suite == "" {
				suite = "all"
			}

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				home, _ := os.UserHomeDir()
				baseDir = home + "/.tent"
			}

			// Run sandbox lifecycle bench if name provided
			if len(args) == 1 {
				name := args[0]
				if suite == "all" || suite == "lifecycle" {
					results.Benchmarks["lifecycle_stop_start"] = benchLifecycle(baseDir, name)
				}
			}

			// Synthetic benchmarks
			if suite == "all" || suite == "io" {
				if !outputJSON {
					fmt.Println("Running disk I/O benchmarks...")
				}
				results.Benchmarks["seq_write_4k"] = benchDiskWrite(baseDir, 4096, 1000)
				results.Benchmarks["seq_write_64k"] = benchDiskWrite(baseDir, 65536, 200)
				results.Benchmarks["seq_write_1m"] = benchDiskWrite(baseDir, 1024*1024, 50)
				results.Benchmarks["seq_read_1m"] = benchDiskRead(baseDir, 1024*1024, 50)
				results.Benchmarks["random_write_4k"] = benchRandomWrite(baseDir, 4096, 500)
			}

			if suite == "all" || suite == "memory" {
				if !outputJSON {
					fmt.Println("Running memory benchmarks...")
				}
				results.Benchmarks["mem_alloc_1m"] = benchMemAlloc(1024 * 1024, 100)
				results.Benchmarks["mem_alloc_16m"] = benchMemAlloc(16*1024*1024, 20)
				results.Benchmarks["mem_copy_4m"] = benchMemCopy(4 * 1024 * 1024, 50)
			}

			if suite == "all" || suite == "system" {
				if !outputJSON {
					fmt.Println("Running system capability benchmarks...")
				}
				results.Benchmarks["dir_create"] = benchDirCreate(baseDir, 200)
				results.Benchmarks["file_stat"] = benchFileStat(baseDir, 500)
				results.Benchmarks["goroutine_spawn"] = benchGoroutineSpawn(1000)
			}

			results.TotalElapsed = time.Since(start).String()

			// Output
			if outputJSON {
				data, err := json.MarshalIndent(results, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to marshal results: %w", err)
				}
				if outputFile != "" {
					if err := os.WriteFile(outputFile, data, 0644); err != nil {
						return fmt.Errorf("failed to write output file: %w", err)
					}
					fmt.Printf("Results written to %s\n", outputFile)
				} else {
					fmt.Println(string(data))
				}
			} else {
				fmt.Printf("\n%-25s %15s %15s  %s\n", "BENCHMARK", "DURATION", "THROUGHPUT", "STATUS")
				fmt.Printf("%-25s %15s %15s  %s\n", "─────────", "────────", "──────────", "──────")
				for _, key := range benchOrder() {
					b, ok := results.Benchmarks[key]
					if !ok {
						continue
					}
					status := "OK"
					if b.Error != "" {
						status = "FAIL"
					}
					result := b.Result
					if result == "" {
						result = "-"
					}
					fmt.Printf("%-25s %15s %15s  %s\n", b.Name, b.Duration, result, status)
				}
				fmt.Printf("\nPlatform: %s/%s (%d CPUs)\n", results.Platform, results.Arch, results.NumCPU)
				fmt.Printf("Total time: %s\n", results.TotalElapsed)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output results in JSON format")
	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Write results to file (implies --json)")
	cmd.Flags().StringVar(&suite, "suite", "all", "Benchmark suite to run (all, io, memory, lifecycle, system)")

	return cmd
}

func benchOrder() []string {
	return []string{
		"seq_write_4k", "seq_write_64k", "seq_write_1m", "seq_read_1m",
		"random_write_4k", "mem_alloc_1m", "mem_alloc_16m", "mem_copy_4m",
		"dir_create", "file_stat", "goroutine_spawn", "lifecycle_stop_start",
	}
}

func benchLifecycle(baseDir, name string) BenchRun {
	run := BenchRun{Name: "lifecycle_stop_start"}

	hvBackend, err := vm.NewPlatformBackend(baseDir)
	if err != nil {
		run.Error = err.Error()
		return run
	}

	manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
	if err != nil {
		run.Error = err.Error()
		return run
	}

	if err := manager.Setup(); err != nil {
		run.Error = err.Error()
		return run
	}

	// Time stop + start cycle
	start := time.Now()
	if err := manager.Stop(name); err != nil {
		run.Error = fmt.Sprintf("stop failed: %v", err)
		return run
	}
	if err := manager.Start(name); err != nil {
		run.Error = fmt.Sprintf("start failed: %v", err)
		return run
	}
	elapsed := time.Since(start)
	run.Duration = elapsed.String()
	run.DurationMs = float64(elapsed.Milliseconds())
	run.Result = fmt.Sprintf("%.0f ms", float64(elapsed.Milliseconds()))
	return run
}

func benchDiskWrite(baseDir string, blockSize, count int) BenchRun {
	name := fmt.Sprintf("seq_write_%s", benchHumanSize(blockSize))
	run := BenchRun{Name: name}

	dir := filepath.Join(baseDir, "bench_tmp")
	if err := os.MkdirAll(dir, 0755); err != nil {
		run.Error = err.Error()
		return run
	}
	defer os.RemoveAll(dir)

	testFile := filepath.Join(dir, "bench_write")
	buf := make([]byte, blockSize)
	_, _ = rand.Read(buf)

	f, err := os.Create(testFile)
	if err != nil {
		run.Error = err.Error()
		return run
	}

	start := time.Now()
	for i := 0; i < count; i++ {
		if _, err := f.Write(buf); err != nil {
			f.Close()
			run.Error = err.Error()
			return run
		}
	}
	f.Sync()
	f.Close()
	elapsed := time.Since(start)

	totalBytes := int64(blockSize) * int64(count)
	mbps := float64(totalBytes) / elapsed.Seconds() / (1024 * 1024)

	run.Duration = elapsed.String()
	run.DurationMs = float64(elapsed.Milliseconds())
	run.Result = fmt.Sprintf("%.1f MB/s", mbps)
	return run
}

func benchDiskRead(baseDir string, blockSize, count int) BenchRun {
	name := fmt.Sprintf("seq_read_%s", benchHumanSize(blockSize))
	run := BenchRun{Name: name}

	dir := filepath.Join(baseDir, "bench_tmp")
	if err := os.MkdirAll(dir, 0755); err != nil {
		run.Error = err.Error()
		return run
	}
	defer os.RemoveAll(dir)

	// Create test file first
	testFile := filepath.Join(dir, "bench_read")
	buf := make([]byte, blockSize)
	_, _ = rand.Read(buf)

	f, err := os.Create(testFile)
	if err != nil {
		run.Error = err.Error()
		return run
	}
	for i := 0; i < count; i++ {
		f.Write(buf)
	}
	f.Sync()
	f.Close()

	// Benchmark read
	f, err = os.Open(testFile)
	if err != nil {
		run.Error = err.Error()
		return run
	}

	readBuf := make([]byte, blockSize)
	start := time.Now()
	for i := 0; i < count; i++ {
		if _, err := f.Read(readBuf); err != nil {
			f.Close()
			run.Error = err.Error()
			return run
		}
	}
	f.Close()
	elapsed := time.Since(start)

	totalBytes := int64(blockSize) * int64(count)
	mbps := float64(totalBytes) / elapsed.Seconds() / (1024 * 1024)

	run.Duration = elapsed.String()
	run.DurationMs = float64(elapsed.Milliseconds())
	run.Result = fmt.Sprintf("%.1f MB/s", mbps)
	return run
}

func benchRandomWrite(baseDir string, blockSize, count int) BenchRun {
	run := BenchRun{Name: "random_write_4k"}

	dir := filepath.Join(baseDir, "bench_tmp")
	if err := os.MkdirAll(dir, 0755); err != nil {
		run.Error = err.Error()
		return run
	}
	defer os.RemoveAll(dir)

	buf := make([]byte, blockSize)
	_, _ = rand.Read(buf)

	start := time.Now()
	for i := 0; i < count; i++ {
		fname := filepath.Join(dir, fmt.Sprintf("rw_%d", i))
		if err := os.WriteFile(fname, buf, 0644); err != nil {
			run.Error = err.Error()
			return run
		}
	}
	elapsed := time.Since(start)

	iops := float64(count) / elapsed.Seconds()
	run.Duration = elapsed.String()
	run.DurationMs = float64(elapsed.Milliseconds())
	run.Result = fmt.Sprintf("%.0f IOPS", iops)
	return run
}

func benchMemAlloc(size, iterations int) BenchRun {
	name := fmt.Sprintf("mem_alloc_%s", benchHumanSize(size))
	run := BenchRun{Name: name}

	start := time.Now()
	for i := 0; i < iterations; i++ {
		buf := make([]byte, size)
		buf[0] = 1
		buf[len(buf)-1] = 1
		_ = buf
	}
	elapsed := time.Since(start)

	totalBytes := int64(size) * int64(iterations)
	gbps := float64(totalBytes) / elapsed.Seconds() / (1024 * 1024 * 1024)

	run.Duration = elapsed.String()
	run.DurationMs = float64(elapsed.Milliseconds())
	run.Result = fmt.Sprintf("%.2f GB/s", gbps)
	return run
}

func benchMemCopy(size, iterations int) BenchRun {
	run := BenchRun{Name: "mem_copy_4m"}

	src := make([]byte, size)
	dst := make([]byte, size)
	_, _ = rand.Read(src)

	start := time.Now()
	for i := 0; i < iterations; i++ {
		copy(dst, src)
	}
	elapsed := time.Since(start)

	totalBytes := int64(size) * int64(iterations)
	gbps := float64(totalBytes) / elapsed.Seconds() / (1024 * 1024 * 1024)

	run.Duration = elapsed.String()
	run.DurationMs = float64(elapsed.Milliseconds())
	run.Result = fmt.Sprintf("%.2f GB/s", gbps)
	return run
}

func benchDirCreate(baseDir string, count int) BenchRun {
	run := BenchRun{Name: "dir_create"}

	dir := filepath.Join(baseDir, "bench_dirs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		run.Error = err.Error()
		return run
	}
	defer os.RemoveAll(dir)

	start := time.Now()
	for i := 0; i < count; i++ {
		d := filepath.Join(dir, fmt.Sprintf("d_%d", i))
		if err := os.Mkdir(d, 0755); err != nil {
			run.Error = err.Error()
			return run
		}
	}
	elapsed := time.Since(start)

	ops := float64(count) / elapsed.Seconds()
	run.Duration = elapsed.String()
	run.DurationMs = float64(elapsed.Milliseconds())
	run.Result = fmt.Sprintf("%.0f ops/s", ops)
	return run
}

func benchFileStat(baseDir string, count int) BenchRun {
	run := BenchRun{Name: "file_stat"}

	dir := filepath.Join(baseDir, "bench_stat")
	if err := os.MkdirAll(dir, 0755); err != nil {
		run.Error = err.Error()
		return run
	}
	defer os.RemoveAll(dir)

	// Create files first
	for i := 0; i < count; i++ {
		fname := filepath.Join(dir, fmt.Sprintf("f_%d", i))
		os.WriteFile(fname, []byte("x"), 0644)
	}

	start := time.Now()
	for i := 0; i < count; i++ {
		fname := filepath.Join(dir, fmt.Sprintf("f_%d", i))
		os.Stat(fname)
	}
	elapsed := time.Since(start)

	ops := float64(count) / elapsed.Seconds()
	run.Duration = elapsed.String()
	run.DurationMs = float64(elapsed.Milliseconds())
	run.Result = fmt.Sprintf("%.0f ops/s", ops)
	return run
}

func benchGoroutineSpawn(count int) BenchRun {
	run := BenchRun{Name: "goroutine_spawn"}

	done := make(chan struct{}, count)

	start := time.Now()
	for i := 0; i < count; i++ {
		go func() {
			done <- struct{}{}
		}()
	}
	for i := 0; i < count; i++ {
		<-done
	}
	elapsed := time.Since(start)

	ops := float64(count) / elapsed.Seconds()
	run.Duration = elapsed.String()
	run.DurationMs = float64(elapsed.Milliseconds())
	run.Result = fmt.Sprintf("%.0f goroutines/s", ops)
	return run
}

func benchHumanSize(bytes int) string {
	switch {
	case bytes >= 1024*1024:
		return fmt.Sprintf("%dm", bytes/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%dk", bytes/1024)
	default:
		return fmt.Sprintf("%d", bytes)
	}
}
