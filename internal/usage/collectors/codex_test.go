package collectors

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

const codexRateResponse = `{"jsonrpc":"2.0","id":2,"result":{
  "rateLimits":{"planType":"pro","primary":{"usedPercent":25,"windowDurationMins":300,"resetsAt":1770648402},
    "secondary":{"usedPercent":4,"windowDurationMins":10080,"resetsAt":1771253202}},
  "rateLimitsByLimitId":{
    "codex":{"limitId":"codex","limitName":"Codex","planType":"pro",
      "primary":{"usedPercent":25,"windowDurationMins":300,"resetsAt":1770648402},
      "secondary":{"usedPercent":4,"windowDurationMins":10080,"resetsAt":1771253202}},
    "spark":{"limitId":"spark","limitName":"Codex Spark","planType":"pro",
      "primary":{"usedPercent":1,"windowDurationMins":300,"resetsAt":1770648500}}
  }
}}`

const codexUsageResponse = `{"jsonrpc":"2.0","id":3,"result":{
  "summary":{"currentStreakDays":3,"lifetimeTokens":0,"peakDailyTokens":9000},
  "dailyUsageBuckets":[{"startDate":"2026-07-09","tokens":1000},{"startDate":"2026-07-10","tokens":0}]
}}`

func TestCodexRequestsInitializeThenReadBothSources(t *testing.T) {
	input, err := codexRequests()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(input)), "\n")
	if len(lines) != 3 {
		t.Fatalf("request count = %d", len(lines))
	}
	wantMethods := []string{"initialize", "account/rateLimits/read", "account/usage/read"}
	for index, line := range lines {
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatal(err)
		}
		if message["method"] != wantMethods[index] {
			t.Fatalf("request %d method = %v", index, message["method"])
		}
	}
}

func TestExchangeCodexProtocolWaitsForInitialize(t *testing.T) {
	input, err := codexRequests()
	if err != nil {
		t.Fatal(err)
	}
	serverInput, clientInput := io.Pipe()
	clientOutput, serverOutput := io.Pipe()
	serverErr := make(chan error, 1)
	responses := codexLines(t, codexUsageResponse, codexRateResponse)
	go func() {
		defer func() { _ = serverOutput.Close() }()
		scanner := bufio.NewScanner(serverInput)
		if err := expectCodexMethod(scanner, "initialize"); err != nil {
			serverErr <- err
			return
		}
		_, _ = io.WriteString(serverOutput, "{\"jsonrpc\":\"2.0\",\"method\":\"status/changed\"}\n")
		_, _ = io.WriteString(serverOutput, "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n")
		for _, method := range []string{"initialized", "account/rateLimits/read", "account/usage/read"} {
			if err := expectCodexMethod(scanner, method); err != nil {
				serverErr <- err
				return
			}
		}
		_, _ = serverOutput.Write(responses)
		serverErr <- nil
	}()

	var output bytes.Buffer
	err = exchangeCodexProtocol(input, clientInput, clientOutput, &output)
	_ = clientInput.Close()
	_ = serverInput.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	parsed := parseCodexOutput(output.Bytes())
	if parsed.plan != "Pro" || len(parsed.windows) == 0 {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func expectCodexMethod(scanner *bufio.Scanner, want string) error {
	if !scanner.Scan() {
		return io.ErrUnexpectedEOF
	}
	var message map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
		return err
	}
	if message["method"] != want {
		return fmt.Errorf("method = %v, want %s", message["method"], want)
	}
	return nil
}

func TestCodexFetchParsesRateLimitsPlanAndUsage(t *testing.T) {
	var capturedBinary string
	var capturedInput []byte
	collector := CodexCollector{CodexBinary: "/opt/bin/codex", Run: func(
		_ context.Context, binary string, input []byte,
	) ([]byte, error) {
		capturedBinary, capturedInput = binary, append([]byte(nil), input...)
		return codexLines(t, codexRateResponse, codexUsageResponse), nil
	}}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{Label: "Codex"})
	if err != nil || result.Snapshot.Status != store.StatusOK {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if capturedBinary != "/opt/bin/codex" || !strings.Contains(string(capturedInput), "account/rateLimits/read") {
		t.Fatalf("binary=%q input=%q", capturedBinary, capturedInput)
	}
	if result.Snapshot.Plan != "Pro" || len(result.Snapshot.Windows) != 3 {
		t.Fatalf("plan=%q windows=%+v", result.Snapshot.Plan, result.Snapshot.Windows)
	}
	assertCodexWindow(t, result.Snapshot.Windows, "codex_codex_primary", 25)
	assertCodexWindow(t, result.Snapshot.Windows, "codex_spark_primary", 1)
	if result.Snapshot.Observed.TotalTokens != 1000 ||
		result.Snapshot.ObservedSourceLabel != "Codex app-server usage" {
		t.Fatalf("observed=%+v source=%q", result.Snapshot.Observed,
			result.Snapshot.ObservedSourceLabel)
	}
}

func assertCodexWindow(t *testing.T, windows []store.UsageWindow, id string, expected float64) {
	t.Helper()
	for _, window := range windows {
		if window.ID == id {
			if window.UsedPercent != nil {
				requireNumber(t, window.UsedPercent, expected)
			} else {
				requireNumber(t, window.Used, expected)
			}
			return
		}
	}
	t.Fatalf("window %q not found in %+v", id, windows)
}

func TestCodexPartialWhenOneReadFails(t *testing.T) {
	collector := CodexCollector{Run: func(context.Context, string, []byte) ([]byte, error) {
		usageError := `{"jsonrpc":"2.0","id":3,"error":{"code":-32000,"message":"usage unavailable"}}`
		return codexLines(t, codexRateResponse, usageError), nil
	}}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{})
	if err != nil || result.Snapshot.Status != store.StatusPartial || len(result.Snapshot.Windows) == 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if !strings.Contains(result.Snapshot.Error, "usage unavailable") {
		t.Fatalf("error = %q", result.Snapshot.Error)
	}
}

func TestCodexKeepsLegacyWindowMissingFromBucketMap(t *testing.T) {
	response := `{"jsonrpc":"2.0","id":2,"result":{
      "rateLimits":{"planType":"pro","primary":{"usedPercent":40,"windowDurationMins":300}},
      "rateLimitsByLimitId":{"spark":{"primary":{"usedPercent":1,"windowDurationMins":300}}}
    }}`
	parsed := parseCodexOutput(codexLines(t, response))
	if len(parsed.windows) != 2 {
		t.Fatalf("windows = %+v", parsed.windows)
	}
	assertCodexWindow(t, parsed.windows, "codex_primary", 40)
}

func codexLines(t *testing.T, values ...string) []byte {
	t.Helper()
	var result bytes.Buffer
	for _, value := range values {
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(value)); err != nil {
			t.Fatal(err)
		}
		result.Write(compact.Bytes())
		result.WriteByte('\n')
	}
	return result.Bytes()
}

func TestCodexBoundedFailuresRemainPartial(t *testing.T) {
	collector := CodexCollector{Run: func(context.Context, string, []byte) ([]byte, error) {
		return nil, errors.New(strings.Repeat("failure ", 100))
	}}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{})
	if err != nil || result.Snapshot.Status != store.StatusPartial {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(result.Snapshot.Error) > 280 || !strings.Contains(result.Snapshot.Error, "no allowance data") {
		t.Fatalf("error = %q", result.Snapshot.Error)
	}
}

func TestCappedBufferBoundsOutput(t *testing.T) {
	buffer := newCappedBuffer(4)
	written, err := buffer.Write([]byte("abcdefgh"))
	if err != nil || written != 8 || string(buffer.Bytes()) != "abcd" {
		t.Fatalf("written=%d err=%v bytes=%q", written, err, buffer.Bytes())
	}
}

func TestCappedBufferSupportsConcurrentReadsAndWrites(t *testing.T) {
	buffer := newCappedBuffer(1 << 20)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = buffer.Write([]byte("usage\n"))
				_ = buffer.Bytes()
			}
		}()
	}
	wg.Wait()
	if len(buffer.Bytes()) == 0 {
		t.Fatal("buffer remained empty")
	}
}
