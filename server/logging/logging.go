package logging

import (
	"fmt"
	"log"
	"os"
	"time"
)

const (
	LevelDebug = "DEBUG"
	LevelInfo  = "INFO"
	LevelError = "ERROR"
)

var (
	debugLogger = log.New(os.Stdout, "", 0)
	infoLogger  = log.New(os.Stdout, "", 0)
	errorLogger = log.New(os.Stderr, "", 0)
)

// Log formats and writes a log message
func Log(level, component, format string, v ...interface{}) {
	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	message := fmt.Sprintf(format, v...)
	logLine := fmt.Sprintf("[%s] %-5s [%s] %s", timestamp, level, component, message)

	switch level {
	case LevelDebug:
		debugLogger.Println(logLine)
	case LevelInfo:
		infoLogger.Println(logLine)
	case LevelError:
		errorLogger.Println(logLine)
	}
}

// Debug logs a debug message
func Debug(component, format string, v ...interface{}) {
	Log(LevelDebug, component, format, v...)
}

// Info logs an info message
func Info(component, format string, v ...interface{}) {
	Log(LevelInfo, component, format, v...)
}

// Error logs an error message
func Error(component, format string, v ...interface{}) {
	Log(LevelError, component, format, v...)
}
