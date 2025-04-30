package utils

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"go.uber.org/zap"
)

type LogEntry struct {
	Method           string    `json:"method"`
	URL              string    `json:"url"`
	RequestHeaders   string    `json:"requestHeaders"`
	RequestBody      string    `json:"requestBody"`
	QueryParams      string    `json:"queryParams"`
	ResponseStatus   string    `json:"responseStatus"`
	ResponseHeaders  string    `json:"responseHeaders"`
	ResponseBody     string    `json:"responseBody"`
	RequestID        string    `json:"requestID"`
	UserID           string    `json:"userID"`
	UserIP           string    `json:"userIP"`
	Host             string    `json:"host"`
	Agent            string    `json:"agent"`
	Origin           string    `json:"origin"`
	TraceID          string    `json:"traceID"`
	RequestTime      string    `json:"requestTime"`
	ResponseTime     string    `json:"responseTime"`
	BytesIn          string    `json:"bytesIn"`
	BytesOut         string    `json:"bytesOut"`
	Referer          string    `json:"referer"`
	Step             string    `json:"step"`
	Status           string    `json:"status"`
	ErrorDescription string    `json:"errorDescription"`
	MethodType       string    `json:"methodType"`
	CreatedAt        time.Time `json:"createdAt"`
}
type CloudWatchLogs struct {
	Method           string `json:"method"`
	URL              string `json:"url"`
	RequestHeaders   string `json:"requestHeaders"`
	RequestBody      string `json:"requestBody"`
	QueryParams      string `json:"queryParams"`
	ResponseStatus   string `json:"responseStatus"`
	ResponseHeaders  string `json:"responseHeaders"`
	ResponseBody     string `json:"responseBody"`
	RequestID        string `json:"requestID"`
	UserID           string `json:"userID"`
	UserIP           string `json:"userIP"`
	Host             string `json:"host"`
	Agent            string `json:"agent"`
	Origin           string `json:"origin"`
	TraceID          string `json:"traceID"`
	RequestTime      string `json:"requestTime"`
	ResponseTime     string `json:"responseTime"`
	BytesIn          string `json:"bytesIn"`
	BytesOut         string `json:"bytesOut"`
	Referer          string `json:"referer"`
	Step             string `json:"step"`
	Status           string `json:"status"`
	ErrorDescription string `json:"errorDescription"`
	MethodType       string `json:"methodType"`
}

func LoggerData(logger *zap.SugaredLogger, logData LogEntry) {

	//log combined request and response
	logFields := []interface{}{
		"method", logData.Method,
		"url", logData.URL,
		"requestHeaders", logData.RequestHeaders,
		"requestBody", logData.RequestBody,
		"queryParams", logData.QueryParams,
		"responseStatus", logData.ResponseStatus,
		"responseHeaders", logData.ResponseHeaders,
		"requestID", logData.RequestID,
		"userID", logData.UserID,
		"userIP", logData.UserIP,
		"host", logData.Host,
		"agent", logData.Agent,
		"origin", logData.Origin,
		"traceID", logData.TraceID,
		"requestTime", logData.RequestTime,
		"responseTime", logData.ResponseTime,
		"bytesIn", logData.BytesIn,
		"bytesOut", logData.BytesOut,
		"referer", logData.Referer,
		"step", logData.Step,
		"status", logData.Status,
		"errorDescription", logData.ErrorDescription,
		"methodType", logData.MethodType,
		"responseBody", logData.ResponseBody,
		"createdAt", logData.CreatedAt,
	}

	logger.Infow(logData.RequestID, logFields...)

}

func GetCleanStackTrace() string {
	depth := 5
	pc := make([]uintptr, depth)
	n := runtime.Callers(2, pc)
	if n == 0 {
		return "No stack trace available"
	}

	pc = pc[:n] // slice to the correct length
	frames := runtime.CallersFrames(pc)

	var stackTrace strings.Builder
	for i := 0; i < depth; i++ {
		frame, more := frames.Next()
		if !more {
			break
		}

		// Get just the function name without the package
		funcName := filepath.Base(frame.Function)

		// Get just the file name without the full path
		fileName := filepath.Base(frame.File)

		stackTrace.WriteString(fmt.Sprintf("%s\n\t%s:%d\n", funcName, fileName, frame.Line))
	}

	return stackTrace.String()
}
