package utils

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

type LogLevel int

var LogFile bool
var LogFunc bool

const (
	LogLevelError = LogLevel(1 << iota)
	LogLevelInfo
	LogLevelNotice
	LogLevelDebug
)

var GlobalLogLevel = LogLevelError | LogLevelInfo

var logBufPool sync.Pool

func init() {
	logBufPool.New = func() any {
		return make([]byte, 0, 512)
	}
}

func getLogBuf() []byte {
	return logBufPool.Get().([]byte)[:0]
}

func returnLogBuf(buf []byte) {
	logBufPool.Put(buf)
}

func Panic(v ...any) {
	buf := getLogBuf()
	defer returnLogBuf(buf)
	buf = fmt.Append(innerPrint(buf, "", "PANIC"), v...)
	_println(buf)
	panic(string(buf))
}

func Panicf(format string, v ...any) {
	buf := getLogBuf()
	defer returnLogBuf(buf)
	buf = fmt.Appendf(innerPrint(buf, "", "PANIC"), format, v...)
	_println(buf)
	panic(string(buf))
}

func Fatalf(format string, v ...any) {
	buf := getLogBuf()
	defer returnLogBuf(buf)
	_println(fmt.Appendf(innerPrint(buf, "", "FATAL"), format, v...))
	os.Exit(1)
}

func Error(v ...any) {
	if GlobalLogLevel&LogLevelError == 0 {
		return
	}
	buf := getLogBuf()
	defer returnLogBuf(buf)
	_println(fmt.Append(innerPrint(buf, "", "ERROR"), v...))
}

func Errorf(prefix, format string, v ...any) {
	if GlobalLogLevel&LogLevelError == 0 {
		return
	}
	buf := getLogBuf()
	defer returnLogBuf(buf)
	_println(fmt.Appendf(innerPrint(buf, prefix, "ERROR"), format, v...))
}

func Print(v ...any) {
	if GlobalLogLevel&LogLevelInfo == 0 {
		return
	}
	buf := getLogBuf()
	defer returnLogBuf(buf)
	_println(fmt.Append(innerPrint(buf, "", "INFO"), v...))
}

func Logf(prefix, format string, v ...any) {
	if GlobalLogLevel&LogLevelInfo == 0 {
		return
	}
	buf := getLogBuf()
	defer returnLogBuf(buf)
	_println(fmt.Appendf(innerPrint(buf, prefix, "INFO"), format, v...))
}

func Noticef(prefix, format string, v ...any) {
	if GlobalLogLevel&LogLevelNotice == 0 {
		return
	}
	buf := getLogBuf()
	defer returnLogBuf(buf)
	_println(fmt.Appendf(innerPrint(buf, prefix, "NOTICE"), format, v...))
}

func IsLogLevelDebug() bool {
	return GlobalLogLevel&LogLevelDebug > 0
}

func Debugf(prefix, format string, v ...any) {
	if GlobalLogLevel&LogLevelDebug == 0 {
		return
	}
	buf := getLogBuf()
	defer returnLogBuf(buf)
	_println(fmt.Appendf(innerPrint(buf, prefix, "DEBUG"), format, v...))
}

func _println(buf []byte) {
	buf = bytes.TrimSpace(buf)
	buf = append(buf, '\n')

	_, _ = os.Stdout.Write(buf)
}

func innerPrint(buf []byte, prefix, class string) []byte {
	buf = time.Now().UTC().AppendFormat(buf, "2006-01-02 15:04:05.000")
	if LogFile {
		var function string
		pc, file, line, ok := runtime.Caller(2)
		if !ok {
			file = "???"
			line = 0
			pc = 0
		}
		short := file
		for i := len(file) - 1; i > 0; i-- {
			if file[i] == '/' {
				short = file[i+1:]
				break
			}
		}

		if LogFunc {
			if pc != 0 {
				if details := runtime.FuncForPC(pc); details != nil {
					function = details.Name()
				}
			}
			shortFunc := function
			for i := len(function) - 1; i > 0; i-- {
				if function[i] == '/' {
					shortFunc = function[i+1:]
					break
				}
			}
			funcItems := strings.Split(shortFunc, ".")
			buf = fmt.Appendf(buf, " %s:%d:%s [%s] %s ", short, line, funcItems[len(funcItems)-1], prefix, class)
		} else {
			buf = fmt.Appendf(buf, " %s:%d [%s] %s ", short, line, prefix, class)
		}
	} else {
		buf = fmt.Appendf(buf, " [%s] %s ", prefix, class)
	}
	return buf
}
