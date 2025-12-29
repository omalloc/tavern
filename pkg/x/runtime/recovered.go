package runtime

import (
	"runtime"
	"strconv"
	"strings"
)

func PrintStackTrace(skip int) string {
	// Capture the stack trace
	pc := make([]uintptr, 10)
	n := runtime.Callers(skip, pc)
	frames := runtime.CallersFrames(pc[:n])

	// Iterate over the frames and print them
	sb := strings.Builder{}
	for {
		frame, more := frames.Next()
		if strings.HasPrefix(frame.Function, "runtime.panic") || strings.HasPrefix(frame.Function, "runtime.gopanic") {
			sb.WriteString("panic: ")
			continue
		}
		sb.WriteString(frame.Function)
		sb.WriteString("\n\t")
		sb.WriteString(frame.File)
		sb.WriteString(":")
		sb.WriteString(strconv.Itoa(frame.Line))
		sb.WriteString("\n")
		if !more {
			break
		}
	}

	return sb.String()
}
