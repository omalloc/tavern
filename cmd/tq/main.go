package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

var marker = map[int]string{
	0:  "Client-Ip",
	1:  "Domain",
	2:  "Content-Type",
	3:  "RequestTime",
	4:  "",
	5:  "Method",
	6:  "ResponseStatus",
	7:  "SentBytes(header+body)",
	8:  "Referer",
	9:  "UserAgent",
	10: "ResponseTime(ms)",
	11: "BodySize",
	12: "ContentLength",
	13: "Range",
	14: "X-Forwarded-For",
	15: "CacheStatus",
	16: "RequestID",
}

func main() {
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return
		}

		sb := strings.Builder{}
		fields := strings.Split(string(line), " ")
		for i, field := range fields {
			mark := marker[i]
			if mark == "" {
				continue
			}

			sb.WriteString("(")
			sb.WriteString(strconv.Itoa(i))
			sb.WriteString(")")
			sb.WriteString(mark)
			sb.WriteString(": ")
			sb.WriteString(field)

			if i+1 < len(marker) && marker[i+1] == "" {
				sb.WriteString(fields[i+1])
			}

			sb.WriteString("\n")
		}

		fmt.Println(sb.String())
	}
}
