package collectors

import "context"

func runGrokBillingProbe(ctx context.Context, binary string, debugPath string) ([]byte, error) {
	return runGrokBillingPTY(ctx, binary, debugPath)
}