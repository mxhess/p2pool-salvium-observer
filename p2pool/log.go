// Package p2pool provides types for parsing p2pool-salvium block data.
package p2pool

import (
	"fmt"
	"log"
	"os"
	"time"
)

// Logger provides simple logging with category prefixes.
var Logger = &logger{
	out: log.New(os.Stderr, "", 0),
}

type logger struct {
	out *log.Logger
}

// Logf logs a formatted message with a category prefix.
func Logf(category, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	Logger.out.Printf("[%s] [%s] %s", timestamp, category, msg)
}

// Errorf logs a formatted error message with a category prefix.
func Errorf(category, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	Logger.out.Printf("[%s] [%s] ERROR: %s", timestamp, category, msg)
}
