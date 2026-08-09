package main

import (
	"fmt"
	"os"
)

const (
	colorReset = "\x1b[0m"
	colorDim   = "\x1b[38;2;120;130;140m"
	colorOk    = "\x1b[38;2;110;220;150m"
	colorBad   = "\x1b[38;2;240;110;110m"
	colorWarn  = "\x1b[38;2;240;190;90m"
)

func colorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}

	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func paintText(color, text string) string {
	if !colorEnabled() {
		return text
	}

	return color + text + colorReset
}

func fallbackText(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

func logLine(message string) {
	fmt.Println(message)
}
