package pkgtmpl

import (
	_ "embed"
	"strings"
)

//go:embed templates/greeting.txt
var greeting string

func Greeting() string { return strings.TrimSpace(greeting) }
