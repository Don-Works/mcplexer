package codemode

import (
	"context"
	"testing"
	"time"
)

// TestNumberToLocaleString covers the polyfill that replaces goja's broken
// Number.prototype.toLocaleString (which aliases to toString and parses a
// locale argument as a radix). Each case runs real sandbox code and asserts
// the printed output.
func TestNumberToLocaleString(t *testing.T) {
	cases := []struct {
		name string
		code string
		want string
	}{
		{
			name: "locale arg no longer throws and groups",
			code: `print((1234.5).toLocaleString("en-GB"))`,
			want: "1,234.5\n",
		},
		{
			name: "no argument groups thousands",
			code: `print((1234567).toLocaleString())`,
			want: "1,234,567\n",
		},
		{
			name: "plain integer unchanged",
			code: `print((42).toLocaleString())`,
			want: "42\n",
		},
		{
			name: "negative keeps grouping and sign",
			code: `print((-1234567.89).toLocaleString("en-GB"))`,
			want: "-1,234,567.89\n",
		},
		{
			name: "GBP currency",
			code: `print((1234.5).toLocaleString("en-GB", {style:"currency", currency:"GBP"}))`,
			want: "£1,234.50\n",
		},
		{
			name: "USD currency",
			code: `print((1234.5).toLocaleString("en-US", {style:"currency", currency:"USD"}))`,
			want: "$1,234.50\n",
		},
		{
			name: "JPY currency has no minor unit",
			code: `print((1234).toLocaleString("ja-JP", {style:"currency", currency:"JPY"}))`,
			want: "¥1,234\n",
		},
		{
			name: "unknown currency falls back to code plus space",
			code: `print((1234.5).toLocaleString("en-GB", {style:"currency", currency:"CHF"}))`,
			want: "CHF 1,234.50\n",
		},
		{
			name: "currencyDisplay code",
			code: `print((9.99).toLocaleString("en-US", {style:"currency", currency:"USD", currencyDisplay:"code"}))`,
			want: "USD 9.99\n",
		},
		{
			name: "percent",
			code: `print((0.2569).toLocaleString("en-US", {style:"percent"}))`,
			want: "26%\n",
		},
		{
			name: "percent with fraction digits",
			code: `print((0.2569).toLocaleString("en-US", {style:"percent", minimumFractionDigits:1}))`,
			want: "25.7%\n",
		},
		{
			name: "minimumFractionDigits pads",
			code: `print((1234.5).toLocaleString("en-GB", {minimumFractionDigits:2}))`,
			want: "1,234.50\n",
		},
		{
			name: "maximumFractionDigits rounds",
			code: `print((1234.567).toLocaleString("en-GB", {maximumFractionDigits:2}))`,
			want: "1,234.57\n",
		},
		{
			name: "useGrouping false",
			code: `print((1234567).toLocaleString("en-US", {useGrouping:false}))`,
			want: "1234567\n",
		},
		{
			name: "zero",
			code: `print((0).toLocaleString())`,
			want: "0\n",
		},
		{
			name: "undefined locale with options",
			code: `print((1234.5).toLocaleString(undefined, {minimumFractionDigits:2}))`,
			want: "1,234.50\n",
		},
		{
			name: "NaN",
			code: `print((0/0).toLocaleString())`,
			want: "NaN\n",
		},
		{
			name: "infinity",
			code: `print((1/0).toLocaleString())`,
			want: "∞\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandbox := NewSandbox(newMockCaller(), 5*time.Second)
			result, err := sandbox.Execute(context.Background(), tc.code, nil)
			if err != nil {
				t.Fatal(err)
			}
			if result.Error != "" {
				t.Fatalf("unexpected sandbox error: %s", result.Error)
			}
			if result.Output != tc.want {
				t.Errorf("code %q\n  got  %q\n  want %q", tc.code, result.Output, tc.want)
			}
		})
	}
}

// TestNumberToLocaleStringIsNonEnumerable guards that overriding the prototype
// method did not make it enumerable (which would leak it into for-in/Object.keys
// over numbers' inherited members and surprise agent code).
func TestNumberToLocaleStringIsNonEnumerable(t *testing.T) {
	sandbox := NewSandbox(newMockCaller(), 5*time.Second)
	result, err := sandbox.Execute(context.Background(),
		`print(Number.prototype.propertyIsEnumerable("toLocaleString"))`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected sandbox error: %s", result.Error)
	}
	if result.Output != "false\n" {
		t.Errorf("toLocaleString should be non-enumerable, got %q", result.Output)
	}
}
