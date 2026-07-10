package collectors

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

var codexInitialized = []byte(`{"jsonrpc":"2.0","method":"initialized","params":{}}`)

// exchangeCodexProtocol observes the JSON-RPC initialization barrier before
// issuing account reads. Current app-server versions ignore account requests
// sent before initialize has completed.
func exchangeCodexProtocol(
	input []byte,
	stdin io.Writer,
	stdout io.Reader,
	output io.Writer,
) error {
	messages := splitCodexRequests(input)
	if len(messages) < 3 {
		return fmt.Errorf("expected initialize and two account requests")
	}
	if err := writeCodexMessages(stdin, messages[:1]); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	if err := scanCodexResponses(scanner, output, map[string]bool{"1": true}); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	remaining := append([][]byte{codexInitialized}, messages[1:]...)
	if err := writeCodexMessages(stdin, remaining); err != nil {
		return err
	}
	return scanCodexResponses(scanner, output, map[string]bool{"2": true, "3": true})
}

func splitCodexRequests(input []byte) [][]byte {
	lines := bytes.Split(bytes.TrimSpace(input), []byte{'\n'})
	result := make([][]byte, 0, len(lines))
	for _, line := range lines {
		if line = bytes.TrimSpace(line); len(line) > 0 {
			result = append(result, line)
		}
	}
	return result
}

func writeCodexMessages(writer io.Writer, messages [][]byte) error {
	for _, message := range messages {
		if _, err := writer.Write(append(append([]byte(nil), message...), '\n')); err != nil {
			return fmt.Errorf("write request: %w", err)
		}
	}
	return nil
}

func scanCodexResponses(
	scanner *bufio.Scanner,
	output io.Writer,
	wanted map[string]bool,
) error {
	for scanner.Scan() {
		line := append(append([]byte(nil), scanner.Bytes()...), '\n')
		if _, err := output.Write(line); err != nil {
			return fmt.Errorf("capture response: %w", err)
		}
		var envelope codexEnvelope
		if json.Unmarshal(scanner.Bytes(), &envelope) == nil {
			delete(wanted, rpcID(envelope.ID))
		}
		if len(wanted) == 0 {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	return io.ErrUnexpectedEOF
}
